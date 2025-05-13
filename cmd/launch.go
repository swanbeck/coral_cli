package cmd

import (
	"fmt"
	"os"
	"time"
	"encoding/json"
	"path/filepath"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"darwin_cli/internal/compose"
	"darwin_cli/internal/extractor"
)

func init() {
	rootCmd.AddCommand(launchCmd)
}

var (
	composePath string
	envFile     string
	handle      string
	deviceID    string
)

var launchCmd = &cobra.Command{
	Use:   "launch",
	Short: "Extract and run docker-compose services",
	RunE: func(cmd *cobra.Command, args []string) error {
		return launch(composePath, envFile)
	},
}

func init() {
	launchCmd.Flags().StringVarP(&composePath, "compose-file", "f", "", "Path to Docker Compose .yaml file")
	launchCmd.Flags().StringVarP(&envFile, "env-file", "e", "", "Path to .env file")
	launchCmd.Flags().StringVar(&handle, "handle", "", "Optional handle for this instance")
	launchCmd.Flags().StringVar(&deviceID, "device-id", "", "Optional device ID for this instance")
}

type InstanceMetadata struct {
	Name        string `json:"name"`
	ComposeFile string `json:"compose_file"`
	CreatedAt   string `json:"created_at"`
	Handle      string `json:"handle,omitempty"`
	DeviceID    string `json:"device_id,omitempty"`
}

func launch(composePath, envFile string) error {
	env := make(map[string]string)
	resolvedEnvFile, err := resolveEnvFile(envFile)
	if resolvedEnvFile != "" {
		var err error
		env, err = compose.LoadEnv(resolvedEnvFile)
		if err != nil {
			return fmt.Errorf("loading .env: %w", err)
		}
	}

	pathToUse, err := resolveComposeFile(composePath)
	if err != nil {
		return err
	}

	cf, err := compose.ParseCompose(pathToUse, env)
	if err != nil {
		return fmt.Errorf("parsing compose file: %w", err)
	}

	hostConfigPath := env["HOST_CONFIG_PATH"]
	if hostConfigPath == "" {
		return fmt.Errorf("HOST_CONFIG_PATH is required to be set!")
	}

	internalConfigPath := env["INTERNAL_CONFIG_PATH"]
	if internalConfigPath == "" {
		return fmt.Errorf("INTERNAL_CONFIG_PATH is required to be set!")
	}

	mergedCompose := compose.RawCompose{
		// "version":  "3.8",
		"services": map[string]interface{}{},
	}

	rawCompose, err := cf.ToMap()
	rawServices, ok := rawCompose["services"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("rawCompose does not contain a valid 'services' block")
	}

	for name, service := range cf.Services {
		image, ok := service["image"].(string)
		if !ok {
			return fmt.Errorf("image field not found or not a string for service: %s", name)
		}

		fmt.Printf("Extracting image for service: %s with corresponding image: %s\n", name, image)
		image_id, err := extractor.ExtractImage(image, "darwin-"+name, hostConfigPath)
		if err != nil {
			return fmt.Errorf("failed to extract image %s: %w", image, err)
		}

		// now try to merge compose files
		extractedComposePath := filepath.Join(internalConfigPath, "lib", "docker", image_id + ".yaml")
		fmt.Printf("Extracted compose path: %s\n", extractedComposePath)
		if _, err := os.Stat(extractedComposePath); err == nil {
			extracted, err := compose.LoadRawYAML(extractedComposePath)
			if err != nil {
				return fmt.Errorf("parsing extracted compose for %s: %w", name, err)
			}
	
			// merge extracted into base
			baseSvc := rawServices[name].(map[string]interface{})
			merged := compose.MergeServiceConfigs(baseSvc, extracted)
			mergedCompose["services"].(map[string]interface{})[name] = merged
			fmt.Printf("Merged compose for %s with extracted compose\n", name)
		} else {
			// just use the base service if no extracted fragment exists
			mergedCompose["services"].(map[string]interface{})[name] = rawCompose[name]
			fmt.Printf("No extracted compose found for %s, using base service\n", name)
		}
	}
	
	fmt.Printf("Merged compose file: %v\n", mergedCompose)

	instanceName := fmt.Sprintf("darwin-%d", time.Now().UnixNano())

	outputPath := filepath.Join(internalConfigPath, "lib", "compose", instanceName + ".yaml")
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
	meta := InstanceMetadata{
		Name:        instanceName,
		ComposeFile: outputPath,
		CreatedAt:   time.Now().Format(time.RFC3339),
		Handle:      handle,
		DeviceID:    deviceID,
	}
	data, _ := json.MarshalIndent(meta, "", "  ")
	err = os.WriteFile(filepath.Join(storeDir, instanceName+".json"), data, 0644)
	if err != nil {
		return fmt.Errorf("writing instance metadata: %w", err)
	}

	// now run the compose file (run in background if -d flag is set)
	// startCmd := exec.Command("docker", "compose", "-f", outputPath, "up")
	// startCmd.Stdout = os.Stdout
	// startCmd.Stderr = os.Stderr
	// startCmd.Run()

	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

	done := make(chan error, 1)
	go func() {
		startCmd := exec.Command("docker", "compose", "-f", outputPath, "up")
		startCmd.Stdout = os.Stdout
		startCmd.Stderr = os.Stderr
		err := startCmd.Run()
		done <- err
	}()

	select {
	case <-signalChan:
		fmt.Println("\nInterrupt received. Shutting down...")
		stopCompose(outputPath)
		removeInstanceFiles(outputPath, instanceName)
		fmt.Println("All files cleaned up.")
	case err := <-done:
		if err != nil {
			fmt.Printf("Docker Compose exited with error: %v\n", err)
		}
		stopCompose(outputPath)
		removeInstanceFiles(outputPath, instanceName)
	}

	return nil
}

func resolveComposeFile(userPath string) (string, error) {
	if userPath != "" {
		return userPath, nil
	}
	candidates := []string{"docker-compose.yaml", "compose.yaml", "docker-compose.yml", "compose.yml"}
	for _, f := range candidates {
		if _, err := os.Stat(f); err == nil {
			return f, nil
		}
	}
	return "", fmt.Errorf("no compose file found (tried: docker-compose.yaml, compose.yaml, docker-compose.yml, compose.yml)")
}

func resolveEnvFile(userPath string) (string, error) {
	if userPath != "" {
		return userPath, nil
	}
	if _, err := os.Stat(".env"); err == nil {
		return ".env", nil
	}
	// just returning empty string if no .env file
	return "", nil
}

func stopCompose(composePath string) {
	fmt.Println("Stopping Compose...")
	cmd := exec.Command("docker", "compose", "-f", composePath, "down")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}

func removeInstanceFiles(composePath, instanceName string) {
	fmt.Println("Cleaning up instance files...")
	_ = os.Remove(composePath)

	home, _ := os.UserHomeDir()
	metaPath := filepath.Join(home, ".darwin_cli", "instances", instanceName + ".json")
	_ = os.Remove(metaPath)
}
