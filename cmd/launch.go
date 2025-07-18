package cmd

import (
	"bufio"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/enescakir/emoji"
	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"coral_cli/internal/cleanup"
	"coral_cli/internal/compose"
	"coral_cli/internal/extractor"
	"coral_cli/internal/io"
	"coral_cli/internal/metadata"
)

//go:embed scripts/extract.sh
var defaultExtractionEntrypoint embed.FS

var (
	launchComposePath   string
	launchEnvFile       string
	launchHandle        string
	launchGroup         string
	launchDetached      bool
	launchKill          bool
	launchExecutorDelay float32
	launchProfiles      []string
)

func init() {
	launchCmd.Flags().StringVarP(&launchComposePath, "compose-file", "f", "", "Path to Docker Compose .yaml file to start services")
	launchCmd.Flags().StringVar(&launchEnvFile, "env-file", "", "Optional path to .env file to use for compose file substitutions")
	launchCmd.Flags().StringVar(&launchHandle, "handle", "", "Optional handle for this instance")
	launchCmd.Flags().StringVarP(&launchGroup, "group", "g", "coral", "Optional group for this instance")
	launchCmd.Flags().BoolVarP(&launchDetached, "detached", "d", false, "Launch in detached mode")
	launchCmd.Flags().BoolVar(&launchKill, "kill", true, "Forcefully kills instances before removing them")
	launchCmd.Flags().Float32Var(&launchExecutorDelay, "executor-delay", 1.0, "Delay in seconds before starting executors; used to provide small delay for drivers and skillsets to start before executors")
	launchCmd.Flags().StringSliceVarP(&launchProfiles, "profile", "p", []string{}, "List of profiles to launch (drivers, skillsets, executors); if not specified, all profiles will be launched")
}

var launchCmd = &cobra.Command{
	Use:   "launch",
	Short: "Extract and run Coral-compatible Docker Compose services",
	RunE: func(cmd *cobra.Command, args []string) error {
		return launch(launchComposePath, launchEnvFile, launchHandle, launchGroup, launchDetached, launchKill, launchExecutorDelay, launchProfiles)
	},
}

type containerInfo struct {
	ID      string
	Name    string
	Service string
}

func launch(composePath string, envFile string, handle string, group string, detached, kill bool, executorDelay float32, profilesToStart []string) error {
	// load and resolve environment
	env := make(map[string]string)
	resolvedEnvFile, err := io.ResolveEnvFile(envFile)
	if err != nil {
		return fmt.Errorf("resolving env file: %w", err)
	}
	if resolvedEnvFile != "" {
		var err error
		env, err = compose.LoadEnvFile(resolvedEnvFile)
		if err != nil {
			return fmt.Errorf("loading .env: %w", err)
		}
	}
	for _, e := range os.Environ() {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			if _, exists := env[parts[0]]; !exists {
				env[parts[0]] = parts[1]
			}
		}
	}

	// resolve and parse compose file
	resolvedComposePath, err := io.ResolveComposeFile(composePath)
	if err != nil {
		return err
	}
	parsedCompose, err := compose.ParseCompose(resolvedComposePath, env)
	if err != nil {
		return fmt.Errorf("parsing compose file: %w", err)
	}

	// validate required environment vars
	libPath, ok := env["CORAL_LIB"]
	if !ok || strings.TrimSpace(libPath) == "" {
		libPath, err = filepath.Abs("./lib")
		if err != nil {
			return fmt.Errorf("failed to resolve ./lib path: %w", err)
		}
		_, err = os.Stat(libPath)
		if os.IsNotExist(err) {
			err := os.Mkdir(libPath, 0755)
			if err != nil {
				return fmt.Errorf("creating directory: %v", err)
			}
		} else if err != nil {
			return fmt.Errorf("checking directory: %v", err)
		}
	} else {
		_, err = os.Stat(libPath)
		if os.IsNotExist(err) {
			return fmt.Errorf("CORAL_LIB path %q does not exist", libPath)
		}
		if err != nil {
			return fmt.Errorf("checking CORAL_LIB path %q: %v", libPath, err)
		}
	}

	isDocker := env["CORAL_IS_DOCKER"]
	var hostLibPath string
	if isDocker == "true" {
		var ok bool
		hostLibPath, ok = env["CORAL_HOST_LIB"]
		if !ok || strings.TrimSpace(hostLibPath) == "" {
			return fmt.Errorf("environment variable CORAL_HOST_LIB is required when running in Docker; it should be an absolute path in the host filesystem that points to the Docker mounted LIB_PATH")
		}
	}

	// get embedded extraction script
	content, err := defaultExtractionEntrypoint.ReadFile("scripts/extract.sh")
	if err != nil {
		return fmt.Errorf("failed to read embedded script: %w", err)
	}

	extractionEntrypoint := filepath.Join(libPath, "extract.sh")
	if err := os.WriteFile(extractionEntrypoint, content, 0755); err != nil {
		return fmt.Errorf("failed to write temp script: %w", err)
	}

	// save the path that was written for deletion at end
	deleteExtractionEntrypoint := extractionEntrypoint
	defer func() {
		// check if file exists before attempting to remove
		if _, err := os.Stat(deleteExtractionEntrypoint); os.IsNotExist(err) {
			return
		}
		if err := os.Remove(deleteExtractionEntrypoint); err != nil {
			fmt.Printf("failed to remove temp script: %v\n", err)
		}
	}()

	// if docker, the entrypoint must be provided wrt the host filesystem
	if isDocker == "true" {
		extractionEntrypoint = filepath.Join(hostLibPath, "extract.sh")
	}

	mergedCompose, profilesMap, err := buildMergedCompose(parsedCompose, libPath, hostLibPath, extractionEntrypoint, profilesToStart)
	if err != nil {
		return err
	}
	profiles := extractProfileNames(profilesMap)

	// make sure mergedCompose has a services section
	servicesRaw, ok := mergedCompose["services"]
	if !ok {
		return fmt.Errorf("merged compose file does not contain a 'services' section")
	}
	services, ok := servicesRaw.(map[string]interface{})
	if !ok || len(services) == 0 {
		return fmt.Errorf("merged compose file does not contain any valid services")
	}

	// write merged compose
	instanceName := fmt.Sprintf("coral-%d", time.Now().UnixNano())
	outputPath := filepath.Join(libPath, "compose", instanceName+".yaml")
	if err := writeComposeToDisk(outputPath, mergedCompose); err != nil {
		return err
	}

	// log metadata
	if err := writeInstanceMetadata(instanceName, outputPath, libPath, handle, group); err != nil {
		return err
	}

	fmt.Printf("Starting instance %s\n", instanceName)
	// logProfileSummary(profilesMap)

	profiles = orderedProfiles(profiles)

	if len(profiles) == 0 {
		return fmt.Errorf("no valid profiles to run")
	}

	if detached {
		return runDetached(profiles, instanceName, outputPath, executorDelay, profilesMap)
	}

	return runForeground(profiles, instanceName, outputPath, kill, executorDelay, profilesMap)
}

func buildMergedCompose(cf *compose.ComposeFile, lib string, hostLib string, extractionEntrypoint string, profilesToStart []string) (compose.RawCompose, map[string][]string, error) {
	rawCompose, err := cf.ToMap()
	if err != nil {
		return nil, nil, fmt.Errorf("converting to raw: %w", err)
	}
	rawServices := rawCompose["services"].(map[string]interface{})
	merged := compose.RawCompose{"services": map[string]interface{}{}}
	profiles := map[string][]string{}

	var imageLib string
	if hostLib != "" {
		imageLib = hostLib
	} else {
		imageLib = lib
	}

	for name, svc := range cf.Services {
		image := svc["image"].(string)

		profs := extractServiceProfiles(svc)

		if len(profilesToStart) > 0 && !hasIntersection(profs, profilesToStart) {
			continue
		}

		validProfiles := []string{"drivers", "skillsets", "executors"}
		if !hasIntersection(profs, validProfiles) {
			fmt.Printf("%s  Skipping service %s as it does not match any valid profiles %v\n", emoji.Warning, name, validProfiles)
			continue
		}

		for _, p := range profs {
			profiles[p] = append(profiles[p], name)
		}

		fmt.Printf("%s Extracting dependencies from image %s for service %s\n", emoji.Package, image, name)
		imageID, err := extractor.ExtractImage(image, name, imageLib, extractionEntrypoint)
		if err != nil {
			return nil, nil, fmt.Errorf("extracting image: %w", err)
		}

		extractedPath := filepath.Join(lib, "docker", imageID+".yaml")
		if _, err := os.Stat(extractedPath); err == nil {
			extracted, err := compose.LoadRawYAML(extractedPath)
			if err != nil {
				return nil, nil, fmt.Errorf("parsing extracted compose for %s: %w", name, err)
			}

			// replace relative volume paths with absolute paths
			mergedSvc := compose.MergeServiceConfigs(rawServices[name].(map[string]interface{}), extracted)
			if volumes, ok := mergedSvc["volumes"].([]interface{}); ok {
				for i, v := range volumes {
					volStr, ok := v.(string)
					if !ok {
						continue
					}
					parts := strings.SplitN(volStr, ":", 2)
					if len(parts) == 2 {
						hostPath := parts[0]
						if !filepath.IsAbs(hostPath) && !strings.HasPrefix(hostPath, "${") {
							absPath, err := filepath.Abs(hostPath)
							if err == nil {
								volumes[i] = fmt.Sprintf("%s:%s", absPath, parts[1])
							}
						}
					}
				}
			}
			merged["services"].(map[string]interface{})[name] = mergedSvc
		} else {
			merged["services"].(map[string]interface{})[name] = rawServices[name]
		}
	}

	return merged, profiles, nil
}

func hasIntersection(a, b []string) bool {
	set := make(map[string]struct{}, len(b))
	for _, item := range b {
		set[item] = struct{}{}
	}
	for _, item := range a {
		if _, ok := set[item]; ok {
			return true
		}
	}
	return false
}

func extractServiceProfiles(service map[string]interface{}) []string {
	var profiles []string
	if raw, ok := service["profiles"]; ok {
		if arr, ok := raw.([]interface{}); ok {
			for _, p := range arr {
				if str, ok := p.(string); ok {
					profiles = append(profiles, str)
				}
			}
		}
	}
	return profiles
}

func extractProfileNames(profiles map[string][]string) []string {
	var keys []string
	for k := range profiles {
		keys = append(keys, k)
	}
	return keys
}

func writeComposeToDisk(path string, compose_data compose.RawCompose) error {
	fmt.Printf("Writing compose file %s\n", path)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating output dir: %w", err)
	}
	return compose.SaveRawYAML(path, compose_data)
}

func writeInstanceMetadata(instanceName, path, lib, handle, group string) error {
	meta := metadata.InstanceMetadata{
		Name:        instanceName,
		ComposeFile: path,
		CreatedAt:   time.Now().Format(time.RFC3339),
		LibPath:     lib,
		Handle:      handle,
		Group:       group,
	}
	home, _ := os.UserHomeDir()
	storeDir := filepath.Join(home, ".coral_cli", "instances")
	os.MkdirAll(storeDir, 0755)
	data, _ := json.MarshalIndent(meta, "", "  ")
	return os.WriteFile(filepath.Join(storeDir, instanceName+".json"), data, 0644)
}

func orderedProfiles(input []string) []string {
	seen := make(map[string]bool)
	var result []string

	for _, key := range []string{"drivers", "skillsets", "executors"} {
		for _, profile := range input {
			if profile == key && !seen[profile] {
				result = append(result, profile)
				seen[profile] = true
			}
		}
	}

	return result
}

func runDetached(profiles []string, instanceName, composePath string, executorDelay float32, profilesMap map[string][]string) error {
	for _, profile := range profiles {
		symbol := emoji.Toolbox
		if profile == "drivers" {
			symbol = emoji.VideoGame
		} else if profile == "skillsets" {
			symbol = emoji.Brain
		} else if profile == "executors" {
			symbol = emoji.Rocket
		}
		fmt.Printf("%s Starting %s (%d): %v\n", symbol, profile, len(profilesMap[profile]), profilesMap[profile])

		// optional delay before executors
		if profile == "executors" {
			fmt.Printf("Delaying %.0fs before starting executors...\n", executorDelay)
			time.Sleep(time.Duration(executorDelay) * time.Second)
		}

		cmd := exec.Command("docker", "compose", "-p", instanceName, "-f", composePath, "--profile", profile, "up", "-d")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to start profile '%s' in detached mode: %w", profile, err)
		}
	}

	return nil
}

func runForeground(profiles []string, instanceName string, composePath string, kill bool, executorDelay float32, profilesMap map[string][]string) error {
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)

	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			// ignore further SIGINT/SIGTERM during cleanup
			signal.Ignore(syscall.SIGINT, syscall.SIGTERM)

			cleanup.StopCompose(instanceName, composePath, kill, profiles)
			fmt.Printf("Cleaning up instance %s\n", instanceName)
			cleanup.RemoveInstanceFiles(instanceName)
			fmt.Printf("%s Done\n", emoji.CheckMarkButton)
		})
	}
	defer cleanup()

	// start all profiles detached
	err := runDetached(profiles, instanceName, composePath, executorDelay, profilesMap)
	if err != nil {
		return fmt.Errorf("failed to start profiles in detached mode: %w", err)
	}

	// get all container IDs in the project
	args := []string{"compose", "-p", instanceName, "-f", composePath, "ps", "-q"}
	out, err := exec.Command("docker", args...).Output()
	if err != nil {
		return fmt.Errorf("failed to get container IDs: %w", err)
	}
	containerIDs := strings.Fields(string(out))
	if len(containerIDs) == 0 {
		return fmt.Errorf("no containers found for instance %s", instanceName)
	}

	// inspect containers to get service names
	prefix := instanceName + "-"
	suffixRegex := regexp.MustCompile(`-\d+$`)
	var containers []containerInfo
	for _, id := range containerIDs {
		nameOut, err := exec.Command("docker", "inspect", "-f", "{{.Name}}", id).Output()
		if err != nil {
			return fmt.Errorf("failed to inspect container %s: %w", id, err)
		}
		fullName := strings.Trim(strings.TrimSpace(string(nameOut)), "/")
		serviceName := fullName
		if strings.HasPrefix(fullName, prefix) {
			serviceName = fullName[len(prefix):]
		}
		serviceName = suffixRegex.ReplaceAllString(serviceName, "")

		containers = append(containers, containerInfo{
			ID:      id,
			Name:    fullName,
			Service: serviceName,
		})
	}

	// create color palette
	colors := []color.Attribute{
		color.FgHiRed,
		color.FgHiGreen,
		color.FgHiYellow,
		color.FgHiBlue,
		color.FgHiMagenta,
		color.FgHiCyan,
	}
	colorMap := make(map[string]*color.Color)
	colorIndex := 0

	// mutex to avoid interleaved output
	var printMu sync.Mutex

	// start tailing logs from all containers concurrently
	var wg sync.WaitGroup
	errCh := make(chan error, len(containers))

	for _, c := range containers {
		wg.Add(1)

		// assign color to service prefix (reuse if exists)
		clr, exists := colorMap[c.Service]
		if !exists {
			clr = color.New(colors[colorIndex%len(colors)]).Add(color.Bold)
			colorMap[c.Service] = clr
			colorIndex++
		}

		go func(c containerInfo, clr *color.Color) {
			defer wg.Done()

			cmd := exec.Command("docker", "logs", "-f", c.ID)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				errCh <- fmt.Errorf("failed to get stdout for container %s: %w", c.Name, err)
				return
			}
			stderr, err := cmd.StderrPipe()
			if err != nil {
				errCh <- fmt.Errorf("failed to get stderr for container %s: %w", c.Name, err)
				return
			}

			if err := cmd.Start(); err != nil {
				errCh <- fmt.Errorf("failed to start logs for container %s: %w", c.Name, err)
				return
			}

			go func() {
				scanner := bufio.NewScanner(stdout)
				for scanner.Scan() {
					line := scanner.Text()
					printMu.Lock()
					clr.Printf("%-20s | ", c.Service)
					fmt.Println(line)
					printMu.Unlock()
				}
			}()

			go func() {
				scanner := bufio.NewScanner(stderr)
				for scanner.Scan() {
					line := scanner.Text()
					printMu.Lock()
					clr.Printf("%-20s | ", c.Service)
					fmt.Println(line)
					printMu.Unlock()
				}
			}()

			if err := cmd.Wait(); err != nil {
				// exit code 130 = SIGINT, which is expected from the docker logs on interrupt and we don't want to treat it as an error
				if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 130 {
					return
				}
				errCh <- fmt.Errorf("logs command exited for container %s: %w", c.Name, err)
			}
		}(c, clr)
	}

	shutdownChan := make(chan struct{})
	go func() {
		<-signalChan
		close(shutdownChan)
	}()

	doneChan := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneChan)
	}()

	// wait for termination signal or errors from any log goroutine
	select {
	case <-shutdownChan:
		fmt.Printf("\nInterrupt received. Forcing shutdown...\n")
		// cleanup()
	case <-doneChan:
		fmt.Printf("All log tails completed. Shutting down...\n")
	case err := <-errCh:
		fmt.Printf("Error while streaming logs: %v\n", err)
	}

	return nil
}
