package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"darwin_cli/internal/cleanup"
	"darwin_cli/internal/compose"
	"darwin_cli/internal/extractor"
	"darwin_cli/internal/io"
	"darwin_cli/internal/metadata"
)

var (
	launchComposePath string
	launchEnvFile     string
	launchHandle      string
	launchGroup       string
	launchDetached    bool
	launchKill        bool
)

func init() {
	launchCmd.Flags().StringVarP(&launchComposePath, "compose-file", "f", "", "Path to Docker Compose .yaml file")
	launchCmd.Flags().StringVar(&launchEnvFile, "env-file", "", "Path to .env file")
	launchCmd.Flags().StringVar(&launchHandle, "handle", "", "Optional handle for this instance")
	launchCmd.Flags().StringVarP(&launchGroup, "group", "g", "", "Optional group for this instance")
	launchCmd.Flags().BoolVarP(&launchDetached, "detached", "d", false, "Run in background (docker compose up -d)")
	launchCmd.Flags().BoolVar(&launchKill, "kill", false, "Forcefully kills instances before removing them")
}

var launchCmd = &cobra.Command{
	Use:   "launch",
	Short: "Extract and run docker-compose services",
	RunE: func(cmd *cobra.Command, args []string) error {
		return launch(launchComposePath, launchEnvFile, launchHandle, launchGroup, launchDetached, launchKill)
	},
}

func launch(composePath, envFile, handle, group string, detached, kill bool) error {
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
	mergedCompose, profilesMap, err := buildMergedCompose(parsedCompose, env, hostConfigPath, internalConfigPath)
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

	fmt.Printf("Starting instance with name: %s\n", instanceName)
	logProfileSummary(profilesMap)

	profiles = orderedProfiles(profiles)

	if len(profiles) == 0 {
		return fmt.Errorf("no valid profiles to run")
	}

	if detached {
		return runDetached(profiles, instanceName, outputPath)
	}

	return runForeground(profiles, instanceName, outputPath, kill)
}

func buildMergedCompose(cf *compose.ComposeFile, env map[string]string, hostCfg, internalCfg string) (compose.RawCompose, map[string][]string, error) {
	rawCompose, err := cf.ToMap()
	if err != nil {
		return nil, nil, fmt.Errorf("converting to raw: %w", err)
	}
	rawServices := rawCompose["services"].(map[string]interface{})
	merged := compose.RawCompose{"services": map[string]interface{}{}}
	profiles := map[string][]string{}

	for name, svc := range cf.Services {
		image := svc["image"].(string)
		fmt.Printf("Extracting image %s for service %s\n", image, name)
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
	fmt.Printf("Writing merged compose file to: %s\n", path)
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

func logProfileSummary(profiles map[string][]string) {
	for name, services := range profiles {
		fmt.Printf("%s (%d): %v\n", strings.Title(name), len(services), services)
	}
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

func runDetached(profiles []string, instanceName, composePath string) error {
	fmt.Println("Running in detached mode...")

	for _, profile := range profiles {
		fmt.Printf("Starting profile '%s'...\n", profile)

		// optional delay before executors
		if profile == "executors" {
			fmt.Println("Delaying before starting executors...")
			time.Sleep(1 * time.Second)
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

func runForeground(profiles []string, instanceName, composePath string, kill bool) error {
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)
	done := make(chan error, 1)

	go func() {
		// start all profiles with up -d
		for _, profile := range profiles {
			if profile == "executors" {
				fmt.Println("Delaying before starting executors...")
				time.Sleep(1 * time.Second)
			}
			fmt.Printf("Starting profile '%s'...\n", profile)
			cmd := exec.Command("docker", "compose", "-p", instanceName, "-f", composePath, "--profile", profile, "up", "-d")
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				done <- fmt.Errorf("failed to start profile '%s': %w", profile, err)
				return
			}
		}

		// attach to logs to keep the process running
		fmt.Println("Attaching to logs (non-detached mode)...")
		args := []string{"compose", "-p", instanceName, "-f", composePath}
		for _, profile := range profiles {
			args = append(args, "--profile", profile)
		}
		args = append(args, "up")
		cmd := exec.Command("docker", args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			done <- fmt.Errorf("error during log streaming: %w", err)
			return
		}

		done <- nil
	}()

	// wait for signal or done
	select {
	case <-signalChan:
		fmt.Println("\nInterrupt received. Shutting down...")
		cleanup.StopCompose(instanceName, composePath, kill, profiles)
		cleanup.RemoveInstanceFiles(instanceName)
		fmt.Println("Done.")
	case err := <-done:
		if err != nil {
			fmt.Printf("Docker Compose exited with error: %v\n", err)
		}
		cleanup.StopCompose(instanceName, composePath, kill, profiles)
		cleanup.RemoveInstanceFiles(instanceName)
		fmt.Println("Done.")
	}

	return nil
}

// func launch(composePath, envFile string, handle string, group string, detached bool, kill bool) error {
// 	env := make(map[string]string)
// 	resolvedEnvFile, err := io.ResolveEnvFile(envFile)
// 	if err != nil {
// 		return fmt.Errorf("resolving env file: %w", err)
// 	}
// 	if resolvedEnvFile != "" {
// 		var err error
// 		env, err = compose.LoadEnv(resolvedEnvFile)
// 		if err != nil {
// 			return fmt.Errorf("loading .env: %w", err)
// 		}
// 	}

// 	pathToUse, err := io.ResolveComposeFile(composePath)
// 	if err != nil {
// 		return err
// 	}

// 	cf, err := compose.ParseCompose(pathToUse, env)
// 	if err != nil {
// 		return fmt.Errorf("parsing compose file: %w", err)
// 	}

// 	hostConfigPath := env["HOST_CONFIG_PATH"]
// 	if hostConfigPath == "" {
// 		return fmt.Errorf("HOST_CONFIG_PATH is required to be set")
// 	}

// 	internalConfigPath := env["INTERNAL_CONFIG_PATH"]
// 	if internalConfigPath == "" {
// 		return fmt.Errorf("INTERNAL_CONFIG_PATH is required to be set")
// 	}

// 	mergedCompose := compose.RawCompose{
// 		// "version":  "3.8",
// 		"services": map[string]interface{}{},
// 	}

// 	rawCompose, err := cf.ToMap()
// 	if err != nil {
// 		return fmt.Errorf("converting compose to raw map: %w", err)
// 	}
// 	rawServices, ok := rawCompose["services"].(map[string]interface{})
// 	if !ok {
// 		return fmt.Errorf("rawCompose does not contain a valid 'services' block")
// 	}

// 	drivers := []string{}
// 	skillsets := []string{}
// 	executors := []string{}

// 	for name, service := range cf.Services {
// 		image, ok := service["image"].(string)
// 		if !ok {
// 			return fmt.Errorf("image field not found or not a string for service: %s", name)
// 		}

// 		profiles := []string{}
// 		if rawProfiles, ok := service["profiles"]; ok {
// 			switch p := rawProfiles.(type) {
// 			case []interface{}:
// 				for _, v := range p {
// 					if s, ok := v.(string); ok {
// 						profiles = append(profiles, s)
// 					}
// 				}
// 			}
// 		}

// 		for _, p := range profiles {
// 			if p == "drivers" {
// 				drivers = append(drivers, name)
// 			}
// 			if p == "skillsets" {
// 				skillsets = append(skillsets, name)
// 			}
// 			if p == "executors" {
// 				executors = append(executors, name)
// 			}
// 		}

// 		fmt.Printf("Extracting image %s for service %s\n", image, name)
// 		imageID, err := extractor.ExtractImage(image, "darwin-"+name, hostConfigPath)
// 		if err != nil {
// 			return fmt.Errorf("failed to extract image %s: %w", image, err)
// 		}
// 		fmt.Printf("Resolved image ID: %s\n", imageID)

// 		// now try to merge compose files
// 		extractedComposePath := filepath.Join(internalConfigPath, "lib", "docker", imageID+".yaml")
// 		if _, err := os.Stat(extractedComposePath); err == nil {
// 			extracted, err := compose.LoadRawYAML(extractedComposePath)
// 			if err != nil {
// 				return fmt.Errorf("parsing extracted compose for %s: %w", name, err)
// 			}

// 			// merge extracted into base
// 			baseSvc := rawServices[name].(map[string]interface{})
// 			merged := compose.MergeServiceConfigs(baseSvc, extracted)
// 			mergedCompose["services"].(map[string]interface{})[name] = merged
// 		} else {
// 			// just use the base service if no extracted fragment exists
// 			mergedCompose["services"].(map[string]interface{})[name] = rawCompose[name]
// 		}
// 	}

// 	instanceName := fmt.Sprintf("darwin-%d", time.Now().UnixNano())

// 	outputPath := filepath.Join(internalConfigPath, "lib", "compose", instanceName+".yaml")
// 	fmt.Printf("Writing merged compose file to: %s\n", outputPath)

// 	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
// 		return fmt.Errorf("creating output directory: %w", err)
// 	}

// 	if err := compose.SaveRawYAML(outputPath, mergedCompose); err != nil {
// 		return fmt.Errorf("writing merged compose file: %w", err)
// 	}

// 	// log the instance metadata
// 	home, err := os.UserHomeDir()
// 	if err != nil {
// 		return fmt.Errorf("could not determine user home directory: %w", err)
// 	}
// 	storeDir := filepath.Join(home, ".darwin_cli", "instances")
// 	os.MkdirAll(storeDir, 0755)
// 	meta := metadata.InstanceMetadata{
// 		Name:        instanceName,
// 		ComposeFile: outputPath,
// 		CreatedAt:   time.Now().Format(time.RFC3339),
// 		ConfigPath:  internalConfigPath,
// 		Handle:      handle,
// 		Group:       group,
// 	}
// 	data, _ := json.MarshalIndent(meta, "", "  ")
// 	err = os.WriteFile(filepath.Join(storeDir, instanceName+".json"), data, 0644)
// 	if err != nil {
// 		return fmt.Errorf("writing instance metadata: %w", err)
// 	}
// 	fmt.Printf("Starting instance with name: %s\n", instanceName)

// 	// check if the profiles are used
// 	hasDrivers := len(drivers) > 0
// 	hasSkillsets := len(skillsets) > 0
// 	hasExecutors := len(executors) > 0

// 	fmt.Printf("Drivers (%d): %v\n", len(drivers), drivers)
// 	fmt.Printf("Skillsets (%d): %v\n", len(skillsets), skillsets)
// 	fmt.Printf("Executors (%d): %v\n", len(executors), executors)

// 	// detached mode
// 	if detached {
// 		fmt.Println("Running in detached mode...")

// 		if hasDrivers {
// 			fmt.Println("Starting drivers...")
// 			upDrivers := exec.Command("docker", "compose", "-p", instanceName, "-f", outputPath, "--profile", "drivers", "up", "-d")
// 			upDrivers.Stdout = os.Stdout
// 			upDrivers.Stderr = os.Stderr
// 			if err := upDrivers.Run(); err != nil {
// 				return fmt.Errorf("failed to start drivers docker compose in detached mode: %w", err)
// 			}
// 		} else {
// 			fmt.Println("No drivers profile found. Skipping...")
// 		}

// 		if hasSkillsets {
// 			fmt.Println("Starting skillsets...")
// 			upSkillsets := exec.Command("docker", "compose", "-p", instanceName, "-f", outputPath, "--profile", "skillsets", "up", "-d")
// 			upSkillsets.Stdout = os.Stdout
// 			upSkillsets.Stderr = os.Stderr
// 			if err := upSkillsets.Run(); err != nil {
// 				return fmt.Errorf("failed to start skillsets docker compose in detached mode: %w", err)
// 			}
// 		} else {
// 			fmt.Println("No skillsets profile found. Skipping...")
// 		}

// 		if hasExecutors {
// 			fmt.Println("Delaying before starting executors...")
// 			time.Sleep(1 * time.Second)
// 			fmt.Println("Starting executors...")
// 			upExecutors := exec.Command("docker", "compose", "-p", instanceName, "-f", outputPath, "--profile", "executors", "up", "-d")
// 			upExecutors.Stdout = os.Stdout
// 			upExecutors.Stderr = os.Stderr
// 			if err := upExecutors.Run(); err != nil {
// 				return fmt.Errorf("failed to start executors docker compose in detached mode: %w", err)
// 			}
// 		} else {
// 			fmt.Println("No executors profile found. Skipping...")
// 		}

// 		return nil
// 	}

// 	// regular running mode
// 	signalChan := make(chan os.Signal, 1)
// 	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

// 	done := make(chan error, 1)
// 	go func() {
// 		var startErr error

// 		if hasDrivers {
// 			fmt.Println("Starting drivers...")
// 			cmd := exec.Command("docker", "compose", "-p", instanceName, "-f", outputPath, "--profile", "drivers", "up", "-d")
// 			cmd.Stdout = os.Stdout
// 			cmd.Stderr = os.Stderr
// 			if err := cmd.Run(); err != nil {
// 				startErr = fmt.Errorf("failed to start drivers: %w", err)
// 				done <- startErr
// 				return
// 			}
// 		}

// 		if hasSkillsets {
// 			fmt.Println("Starting skillsets...")
// 			cmd := exec.Command("docker", "compose", "-p", instanceName, "-f", outputPath, "--profile", "skillsets", "up", "-d")
// 			cmd.Stdout = os.Stdout
// 			cmd.Stderr = os.Stderr
// 			if err := cmd.Run(); err != nil {
// 				startErr = fmt.Errorf("failed to start skillsets: %w", err)
// 				done <- startErr
// 				return
// 			}
// 		}

// 		if hasExecutors {
// 			fmt.Println("Delaying before starting executors...")
// 			time.Sleep(1 * time.Second)

// 			fmt.Println("Starting executors...")
// 			cmd := exec.Command("docker", "compose", "-p", instanceName, "-f", outputPath, "--profile", "executors", "up", "-d")
// 			cmd.Stdout = os.Stdout
// 			cmd.Stderr = os.Stderr
// 			if err := cmd.Run(); err != nil {
// 				startErr = fmt.Errorf("failed to start executors: %w", err)
// 				done <- startErr
// 				return
// 			}
// 		}

// 		// call up with logs to attach to foreground
// 		if !detached {
// 			fmt.Println("Attaching to logs (non-detached mode)...")
// 			args := []string{"compose", "-p", instanceName, "-f", outputPath}
// 			if hasDrivers {
// 				args = append(args, "--profile", "drivers")
// 			}
// 			if hasSkillsets {
// 				args = append(args, "--profile", "skillsets")
// 			}
// 			if hasExecutors {
// 				args = append(args, "--profile", "executors")
// 			}
// 			args = append(args, "up")
// 			logCmd := exec.Command("docker", args...)
// 			logCmd.Stdout = os.Stdout
// 			logCmd.Stderr = os.Stderr
// 			if err := logCmd.Run(); err != nil {
// 				startErr = fmt.Errorf("error during log streaming: %w", err)
// 				done <- startErr
// 				return
// 			}
// 		}

// 		done <- nil
// 	}()

// 	select {
// 	case <-signalChan:
// 		fmt.Println("\nInterrupt received. Shutting down...")
// 		cleanup.StopCompose(instanceName, outputPath, kill)
// 		cleanup.RemoveInstanceFiles(instanceName)
// 		fmt.Println("Done.")
// 	case err := <-done:
// 		if err != nil {
// 			fmt.Printf("Docker Compose exited with error: %v\n", err)
// 		}
// 		cleanup.StopCompose(instanceName, outputPath, kill)
// 		cleanup.RemoveInstanceFiles(instanceName)
// 		fmt.Println("Done.")
// 	}

// 	return nil
// }
