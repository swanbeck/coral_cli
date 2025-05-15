package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
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

func launch(composePath, envFile string, handle string, group string, detached bool, kill bool) error {
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

	pathToUse, err := io.ResolveComposeFile(composePath)
	if err != nil {
		return err
	}

	cf, err := compose.ParseCompose(pathToUse, env)
	if err != nil {
		return fmt.Errorf("parsing compose file: %w", err)
	}

	hostConfigPath := env["HOST_CONFIG_PATH"]
	if hostConfigPath == "" {
		return fmt.Errorf("HOST_CONFIG_PATH is required to be set")
	}

	internalConfigPath := env["INTERNAL_CONFIG_PATH"]
	if internalConfigPath == "" {
		return fmt.Errorf("INTERNAL_CONFIG_PATH is required to be set")
	}

	mergedCompose := compose.RawCompose{
		// "version":  "3.8",
		"services": map[string]interface{}{},
	}

	rawCompose, err := cf.ToMap()
	if err != nil {
		return fmt.Errorf("converting compose to raw map: %w", err)
	}
	rawServices, ok := rawCompose["services"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("rawCompose does not contain a valid 'services' block")
	}

	for name, service := range cf.Services {
		image, ok := service["image"].(string)
		if !ok {
			return fmt.Errorf("image field not found or not a string for service: %s", name)
		}

		fmt.Printf("Extracting image %s for service %s\n", image, name)
		imageID, err := extractor.ExtractImage(image, "darwin-"+name, hostConfigPath)
		if err != nil {
			return fmt.Errorf("failed to extract image %s: %w", image, err)
		}
		fmt.Printf("Resolved image ID: %s\n", imageID)

		// now try to merge compose files
		extractedComposePath := filepath.Join(internalConfigPath, "lib", "docker", imageID+".yaml")
		if _, err := os.Stat(extractedComposePath); err == nil {
			extracted, err := compose.LoadRawYAML(extractedComposePath)
			if err != nil {
				return fmt.Errorf("parsing extracted compose for %s: %w", name, err)
			}

			// merge extracted into base
			baseSvc := rawServices[name].(map[string]interface{})
			merged := compose.MergeServiceConfigs(baseSvc, extracted)
			mergedCompose["services"].(map[string]interface{})[name] = merged
		} else {
			// just use the base service if no extracted fragment exists
			mergedCompose["services"].(map[string]interface{})[name] = rawCompose[name]
		}
	}

	instanceName := fmt.Sprintf("darwin-%d", time.Now().UnixNano())

	outputPath := filepath.Join(internalConfigPath, "lib", "compose", instanceName+".yaml")
	fmt.Printf("Writing merged compose file to: %s\n", outputPath)

	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	if err := compose.SaveRawYAML(outputPath, mergedCompose); err != nil {
		return fmt.Errorf("writing merged compose file: %w", err)
	}

	// log the instance metadata
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("could not determine user home directory: %w", err)
	}
	storeDir := filepath.Join(home, ".darwin_cli", "instances")
	os.MkdirAll(storeDir, 0755)
	meta := metadata.InstanceMetadata{
		Name:        instanceName,
		ComposeFile: outputPath,
		CreatedAt:   time.Now().Format(time.RFC3339),
		ConfigPath:  internalConfigPath,
		Handle:      handle,
		Group:       group,
	}
	data, _ := json.MarshalIndent(meta, "", "  ")
	err = os.WriteFile(filepath.Join(storeDir, instanceName+".json"), data, 0644)
	if err != nil {
		return fmt.Errorf("writing instance metadata: %w", err)
	}
	fmt.Printf("Starting instance with name: %s\n", instanceName)

	// detached mode
	if detached {
		fmt.Println("Running in detached mode...")
		startCmd := exec.Command("docker", "compose", "-p", instanceName, "-f", outputPath, "up", "-d")
		startCmd.Stdout = os.Stdout
		startCmd.Stderr = os.Stderr
		if err := startCmd.Run(); err != nil {
			return fmt.Errorf("failed to start docker compose in detached mode: %w", err)
		}
		return nil
	}

	// regular running mode
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

	done := make(chan error, 1)
	go func() {
		startCmd := exec.Command("docker", "compose", "-p", instanceName, "-f", outputPath, "up")
		startCmd.Stdout = os.Stdout
		startCmd.Stderr = os.Stderr
		err := startCmd.Run()
		done <- err
	}()

	select {
	case <-signalChan:
		fmt.Println("\nInterrupt received. Shutting down...")
		cleanup.StopCompose(instanceName, outputPath, kill)
		cleanup.RemoveInstanceFiles(instanceName)
		fmt.Println("Done.")
	case err := <-done:
		if err != nil {
			fmt.Printf("Docker Compose exited with error: %v\n", err)
		}
		cleanup.StopCompose(instanceName, outputPath, kill)
		cleanup.RemoveInstanceFiles(instanceName)
		fmt.Println("Done.")
	}

	return nil
}
