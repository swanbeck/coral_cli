package cmd

import (
	"bufio"
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

	"darwin_cli/internal/cleanup"
	"darwin_cli/internal/compose"
	"darwin_cli/internal/extractor"
	"darwin_cli/internal/io"
	"darwin_cli/internal/metadata"
)

var (
	launchComposePath   string
	launchEnvFile       string
	launchHandle        string
	launchGroup         string
	launchDetached      bool
	launchKill          bool
	launchExecutorDelay float32
)

func init() {
	launchCmd.Flags().StringVarP(&launchComposePath, "compose-file", "f", "", "Path to Docker Compose .yaml file")
	launchCmd.Flags().StringVar(&launchEnvFile, "env-file", "", "Path to .env file")
	launchCmd.Flags().StringVar(&launchHandle, "handle", "", "Optional handle for this instance")
	launchCmd.Flags().StringVarP(&launchGroup, "group", "g", "", "Optional group for this instance")
	launchCmd.Flags().BoolVarP(&launchDetached, "detached", "d", false, "Run in background (docker compose up -d)")
	launchCmd.Flags().BoolVar(&launchKill, "kill", false, "Forcefully kills instances before removing them")
	launchCmd.Flags().Float32VarP(&launchExecutorDelay, "executor-delay", "q", 1.0, "Delay in seconds before starting executors")
}

var launchCmd = &cobra.Command{
	Use:   "launch",
	Short: "Extract and run docker-compose services",
	RunE: func(cmd *cobra.Command, args []string) error {
		return launch(launchComposePath, launchEnvFile, launchHandle, launchGroup, launchDetached, launchKill, launchExecutorDelay)
	},
}

type containerInfo struct {
	ID      string
	Name    string
	Service string
}

func launch(composePath, envFile, handle, group string, detached, kill bool, executorDelay float32) error {
	// load and resolve environment
	env := make(map[string]string)
	resolvedEnvFile, err := io.ResolveEnvFile(envFile)
	if err != nil {
		return fmt.Errorf("resolving env file: %w", err)
	}
	if resolvedEnvFile != "" {
		var err error
		env, err = compose.LoadEnv(resolvedEnvFile)
		if err != nil {
			return fmt.Errorf("loading .env: %w", err)
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
	hostConfigPath := env["HOST_CONFIG_PATH"]
	internalConfigPath := env["INTERNAL_CONFIG_PATH"]
	if hostConfigPath == "" || internalConfigPath == "" {
		return fmt.Errorf("HOST_CONFIG_PATH and INTERNAL_CONFIG_PATH must be set")
	}

	// process services
	mergedCompose, profilesMap, err := buildMergedCompose(parsedCompose, hostConfigPath, internalConfigPath)
	if err != nil {
		return err
	}
	profiles := extractProfileNames(profilesMap)

	// write merged compose
	instanceName := fmt.Sprintf("darwin-%d", time.Now().UnixNano())
	outputPath := filepath.Join(internalConfigPath, "lib", "compose", instanceName+".yaml")
	if err := writeComposeToDisk(outputPath, mergedCompose); err != nil {
		return err
	}

	// log metadata
	if err := writeInstanceMetadata(instanceName, outputPath, internalConfigPath, handle, group); err != nil {
		return err
	}

	fmt.Printf("%s Starting instance with name: %s\n", emoji.Rocket, instanceName)
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

func buildMergedCompose(cf *compose.ComposeFile, hostCfg, internalCfg string) (compose.RawCompose, map[string][]string, error) {
	rawCompose, err := cf.ToMap()
	if err != nil {
		return nil, nil, fmt.Errorf("converting to raw: %w", err)
	}
	rawServices := rawCompose["services"].(map[string]interface{})
	merged := compose.RawCompose{"services": map[string]interface{}{}}
	profiles := map[string][]string{}

	for name, svc := range cf.Services {
		image := svc["image"].(string)
		fmt.Printf("%s Extracting dependencies from image %s for service %s\n", emoji.Package, image, name)
		imageID, err := extractor.ExtractImage(image, "darwin-"+name, hostCfg)
		if err != nil {
			return nil, nil, fmt.Errorf("extracting image: %w", err)
		}

		profs := extractServiceProfiles(svc)
		for _, p := range profs {
			profiles[p] = append(profiles[p], name)
		}

		extractedPath := filepath.Join(internalCfg, "lib", "docker", imageID+".yaml")
		if _, err := os.Stat(extractedPath); err == nil {
			extracted, err := compose.LoadRawYAML(extractedPath)
			if err != nil {
				return nil, nil, fmt.Errorf("parsing extracted compose for %s: %w", name, err)
			}
			merged["services"].(map[string]interface{})[name] = compose.MergeServiceConfigs(rawServices[name].(map[string]interface{}), extracted)
		} else {
			merged["services"].(map[string]interface{})[name] = rawServices[name]
		}
	}

	return merged, profiles, nil
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
	fmt.Printf("%s Writing merged compose file to: %s\n", emoji.Memo, path)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating output dir: %w", err)
	}
	return compose.SaveRawYAML(path, compose_data)
}

func writeInstanceMetadata(instanceName, path, config, handle, group string) error {
	meta := metadata.InstanceMetadata{
		Name:        instanceName,
		ComposeFile: path,
		CreatedAt:   time.Now().Format(time.RFC3339),
		ConfigPath:  config,
		Handle:      handle,
		Group:       group,
	}
	home, _ := os.UserHomeDir()
	storeDir := filepath.Join(home, ".darwin_cli", "instances")
	os.MkdirAll(storeDir, 0755)
	data, _ := json.MarshalIndent(meta, "", "  ")
	return os.WriteFile(filepath.Join(storeDir, instanceName+".json"), data, 0644)
}

func orderedProfiles(input []string) []string {
	seen := make(map[string]bool)
	var result []string

	// append in defined order if present
	for _, key := range []string{"drivers", "skillsets", "executors"} {
		for _, profile := range input {
			if profile == key && !seen[profile] {
				result = append(result, profile)
				seen[profile] = true
			}
		}
	}

	// // for now excluding other profiles
	// for _, profile := range input {
	// 	if !seen[profile] {
	// 		result = append(result, profile)
	// 	}
	// }

	return result
}

func runDetached(profiles []string, instanceName, composePath string, executorDelay float32, profilesMap map[string][]string) error {
	for _, profile := range profiles {
		symbol := emoji.Toolbox
		if profile == "drivers" {
			symbol = emoji.ElectricPlug
		} else if profile == "skillsets" {
			symbol = emoji.Toolbox
		} else if profile == "executors" {
			symbol = emoji.Gear + " "
		}
		fmt.Printf("%s Starting %s (%d): %v\n", symbol, profile, len(profilesMap[profile]), profilesMap[profile])

		// optional delay before executors
		if profile == "executors" {
			fmt.Printf("%s Delaying %.2fs before starting executors...\n", emoji.HourglassNotDone, executorDelay)
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

func runForeground(profiles []string, instanceName, composePath string, kill bool, executorDelay float32, profilesMap map[string][]string) error {
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

	// start all profiles detached
	err := runDetached(profiles, instanceName, composePath, executorDelay, profilesMap)
	if err != nil {
		return fmt.Errorf("failed to start profiles in detached mode: %w", err)
	}

	// get all container IDs in the project
	fmt.Println("Fetching container list...")
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
				errCh <- fmt.Errorf("logs command exited for container %s: %w", c.Name, err)
			}
		}(c, clr)
	}

	// wait for termination signal or errors from any log goroutine
	select {
	case <-signalChan:
		fmt.Printf("\n%s  Interrupt received. Shutting down...\n", emoji.Warning)
		cleanup.StopCompose(instanceName, composePath, kill, profiles)
		fmt.Printf("%s Cleaning up files for instance %s...\n", emoji.Broom, instanceName)
		cleanup.RemoveInstanceFiles(instanceName)
		fmt.Printf("%s Done.\n", emoji.CheckMarkButton)
	case err := <-errCh:
		fmt.Printf("Error while streaming logs: %v\n", err)
		cleanup.StopCompose(instanceName, composePath, kill, profiles)
		fmt.Printf("%s Cleaning up files for instance %s...\n", emoji.Broom, instanceName)
		cleanup.RemoveInstanceFiles(instanceName)
		fmt.Printf("%s Done.\n", emoji.CheckMarkButton)
	}

	// wait for all log tails to finish
	wg.Wait()

	return nil
}
