package cmd

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"gopkg.in/yaml.v3"

	"coral_cli/internal/cleanup"
	"coral_cli/internal/compose"
	"coral_cli/internal/extractor"
	"coral_cli/internal/io"
	"coral_cli/internal/logging"
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
	launchCmd.Args = cobra.NoArgs

	launchCmd.Flags().StringVarP(&launchComposePath, "compose-file", "f", "", "Path to Docker Compose .yaml file to start services")
	launchCmd.Flags().StringVar(&launchEnvFile, "env-file", "", "Optional path to .env file to use for compose file substitutions")
	launchCmd.Flags().StringVar(&launchHandle, "handle", "", "Optional handle for this instance")
	launchCmd.Flags().StringVarP(&launchGroup, "group", "g", "coral", "Optional group for this instance")
	launchCmd.Flags().BoolVarP(&launchDetached, "detached", "d", false, "Launch in detached mode")
	launchCmd.Flags().BoolVar(&launchKill, "kill", true, "Forcefully kills instances before removing them")
	launchCmd.Flags().Float32Var(&launchExecutorDelay, "executor-delay", 0.0, "Delay in seconds before starting executors; used to provide small delay for drivers and skillsets to start before executors")
	launchCmd.Flags().StringSliceVarP(&launchProfiles, "profile", "p", []string{}, "List of profiles to launch (drivers, skillsets, executors); if not specified, all profiles will be launched")

	launchCmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if toComplete == "" {
			var out []string
			cmd.Flags().VisitAll(func(f *pflag.Flag) {
				if f.Shorthand != "" {
					out = append(out, fmt.Sprintf("#   --%s,-%s", f.Name, f.Shorthand))
				} else {
					out = append(out, fmt.Sprintf("#   --%s", f.Name))
				}
			})
			return out, cobra.ShellCompDirectiveNoFileComp
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	launchCmd.RegisterFlagCompletionFunc("compose-file", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		dir := "."

		if strings.Contains(toComplete, string(os.PathSeparator)) {
			dir = filepath.Dir(toComplete)
			if dir == "" {
				dir = "."
			}
		}

		files, err := os.ReadDir(dir)
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}

		var suggestions []string
		for _, f := range files {
			entry := filepath.Join(dir, f.Name())
			display := entry

			if f.IsDir() {
				display += string(os.PathSeparator)
			}

			if !strings.HasPrefix(display, toComplete) {
				continue
			}

			if f.IsDir() {
				suggestions = append(suggestions, display)
				continue
			}

			if !strings.HasSuffix(f.Name(), ".yaml") && !strings.HasSuffix(f.Name(), ".yml") {
				continue
			}

			content, err := os.ReadFile(entry)
			if err != nil {
				continue
			}

			var doc map[string]any
			if err := yaml.Unmarshal(content, &doc); err != nil {
				continue
			}

			if _, ok := doc["services"]; ok {
				suggestions = append(suggestions, display)
			}
		}

		return suggestions, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
	})

	launchCmd.RegisterFlagCompletionFunc("profile", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		profiles := []string{"drivers", "skillsets", "executors"}
		var matches []string
		for _, profile := range profiles {
			if strings.HasPrefix(profile, toComplete) {
				matches = append(matches, profile)
			}
		}
		return matches, cobra.ShellCompDirectiveNoFileComp
	})
}

var launchCmd = &cobra.Command{
	Use:   "launch",
	Short: "Launches Coral instances",
	RunE: func(cmd *cobra.Command, args []string) error {
		return launch(launchComposePath, launchEnvFile, launchHandle, launchGroup, launchDetached, launchKill, launchExecutorDelay, launchProfiles)
	},
}

func launch(composePath string, envFile string, handle string, group string, detached, kill bool, executorDelay float32, profilesToStart []string) error {
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	var receivedSignal atomic.Bool
	go func() {
		<-signalChan
		receivedSignal.Store(true)
	}()

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

	instanceName := fmt.Sprintf("coral-%s", uuid.New())

	extractionEntrypoint := filepath.Join(libPath, fmt.Sprintf("%s-extract.sh", instanceName))
	if err := os.WriteFile(extractionEntrypoint, content, 0755); err != nil {
		return fmt.Errorf("failed to write temp script: %w", err)
	}

	// save the path that was written for deletion at end
	deleteExtractionEntrypoint := extractionEntrypoint
	defer func() {
		if _, err := os.Stat(deleteExtractionEntrypoint); os.IsNotExist(err) {
			return
		}
		if err := os.Remove(deleteExtractionEntrypoint); err != nil {
			fmt.Println(logging.Warning(fmt.Sprintf("failed to remove temp script: %v", err)))
		}
	}()

	// if docker, the entrypoint must be provided wrt the host filesystem
	if isDocker == "true" {
		extractionEntrypoint = filepath.Join(hostLibPath, fmt.Sprintf("%s-extract.sh", instanceName))
	}

	err = checkImagesLocal(parsedCompose, profilesToStart)
	if err != nil {
		return fmt.Errorf("checking images: %w", err)
	}

	fmt.Println(logging.Info("Launching new instance " + logging.BoldMagentaHi(instanceName)))

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
	outputPath := filepath.Join(libPath, "compose", instanceName+".yaml")
	if err := writeComposeToDisk(outputPath, mergedCompose); err != nil {
		return err
	}

	// log metadata
	if err := writeInstanceMetadata(instanceName, outputPath, libPath, handle, group, detached); err != nil {
		return err
	}

	profiles = orderedProfiles(profiles)
	if len(profiles) == 0 {
		return fmt.Errorf("no valid profiles to run")
	}

	if receivedSignal.Load() {
		fmt.Printf("\n%s\n", logging.Warning(fmt.Sprintf("Interrupt received during initialization. Cleaning up %s...", logging.BoldMagenta(instanceName))))
		err := cleanup.RemoveInstanceFiles(instanceName)
		if err != nil {
			return fmt.Errorf("cleaning up instance files: %w", err)
		}
		fmt.Println(logging.Success("Done"))
		return nil
	}

	signal.Reset(os.Interrupt, syscall.SIGINT, syscall.SIGTERM)

	if detached {
		return runDetached(profiles, instanceName, outputPath, executorDelay, profilesMap)
	}

	return runForeground(profiles, instanceName, outputPath, kill, executorDelay, profilesMap)
}

func checkImagesLocal(cf *compose.ComposeFile, profilesToStart []string) error {
	for name, svc := range cf.Services {
		raw, ok := svc["image"]
		if !ok || raw == nil {
			return fmt.Errorf("missing required field 'image' in service: %#v", svc)
		}

		image, ok := raw.(string)
		if !ok {
			return fmt.Errorf("expected string for 'image', got %T (%#v)", raw, raw)
		}

		profs := extractServiceProfiles(svc)

		if len(profilesToStart) > 0 && !hasIntersection(profs, profilesToStart) {
			continue
		}

		_, err := extractor.GetImageID(image)
		if err != nil {
			return fmt.Errorf("checking image %s used in service %s: %w", image, name, err)
		}
	}
	return nil
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
			fmt.Println(logging.Warning(fmt.Sprintf("Skipping service %s as it does not match any valid profiles %v", name, validProfiles)))
			continue
		}

		for _, p := range profs {
			profiles[p] = append(profiles[p], name)
		}

		imageID, err := extractor.ExtractImage(image, name, imageLib, extractionEntrypoint)
		if err != nil {
			return nil, nil, fmt.Errorf("extracting image %s for service %s: %w", image, name, err)
		}

		fmt.Println(logging.Info(fmt.Sprintf("Extracted interfaces from %s for %s", image, logging.BoldMagenta(name))))

		extractedPath := filepath.Join(lib, "docker", imageID+".yaml")
		var mergedSvc map[string]interface{}
		baseSvc := rawServices[name].(map[string]interface{})

		if _, err := os.Stat(extractedPath); err == nil {
			extracted, err := compose.LoadRawYAML(extractedPath)
			if err != nil {
				return nil, nil, fmt.Errorf("parsing extracted compose for %s: %w", name, err)
			}

			mergedSvc = compose.MergeServiceConfigs(rawServices[name].(map[string]interface{}), extracted)
		} else {
			mergedSvc = baseSvc
		}

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
	var profileNames []string
	for k := range profiles {
		profileNames = append(profileNames, k)
	}
	return profileNames
}

func writeComposeToDisk(path string, compose_data compose.RawCompose) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating output dir: %w", err)
	}
	return compose.SaveRawYAML(path, compose_data)
}

func writeInstanceMetadata(instanceName, path, lib, handle, group string, detached bool) error {
	meta := metadata.InstanceMetadata{
		Name:        instanceName,
		ComposeFile: path,
		CreatedAt:   time.Now().Format(time.RFC3339),
		LibPath:     lib,
		Handle:      handle,
		Group:       group,
		Detached:    detached,
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
		fmt.Println(logging.Info(fmt.Sprintf("Starting %s (%d): %s", logging.BoldMagenta(profile), len(profilesMap[profile]), logging.BoldMagenta(fmt.Sprintf("%v", profilesMap[profile])))))

		// optional delay before executors
		if profile == "executors" && executorDelay > 0 {
			fmt.Println(logging.Info(fmt.Sprintf("Delaying %.0fs before starting executors...", executorDelay)))
			time.Sleep(time.Duration(executorDelay) * time.Second)
		}

		cmd := exec.Command("docker", "compose", "-p", instanceName, "-f", composePath, "--profile", profile, "up", "-d")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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

			err := cleanup.StopCompose(instanceName, composePath, kill, profiles)
			if err != nil {
				fmt.Println(logging.Warning(fmt.Sprintf("Unable to stop compose: %v", err)))
			}

			err = cleanup.RemoveInstanceFiles(instanceName)
			if err != nil {
				fmt.Println(logging.Failure(fmt.Sprintf("Failed to clean up files: %v", err)))
			}

			fmt.Println(logging.Success("Done"))
		})
	}
	defer cleanup()

	// start all profiles detached
	err := runDetached(profiles, instanceName, composePath, executorDelay, profilesMap)
	if err != nil {
		return fmt.Errorf("failed to start profiles in detached mode: %w", err)
	}

	containers, err := logging.GetContainerInfo(instanceName, composePath)
	if err != nil {
		return fmt.Errorf("getting container info: %w", err)
	}

	shutdownChan := make(chan struct{})
	go func() {
		<-signalChan
		signal.Stop(signalChan)
		close(shutdownChan)
	}()

	// start log tailing
	doneChan, errCh := logging.TailLogs(containers, shutdownChan, true)

	// wait for completion or interrupt
	select {
	case <-shutdownChan:
		fmt.Printf("\n%s\n", logging.Warning(fmt.Sprintf("Interrupt received. Shutting down %s...", logging.BoldMagenta(instanceName))))
	case <-doneChan:
		fmt.Println(logging.Info("All log tails completed. Shutting down..."))
	case err := <-errCh:
		fmt.Println(logging.Failure(fmt.Sprintf("Error while streaming logs: %v", err)))
	}

	return nil
}
