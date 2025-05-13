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
	"strings"
	"bufio"

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
	ConfigPath  string `json:"config_path"`
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

		fmt.Printf("Extracting image %s for service %s\n", image, name)
		imageID, err := extractor.ExtractImage(image, "darwin-" + name, hostConfigPath)
		if err != nil {
			return fmt.Errorf("failed to extract image %s: %w", image, err)
		}
		fmt.Printf("Resolved image ID: %s\n", imageID)

		// now try to merge compose files
		extractedComposePath := filepath.Join(internalConfigPath, "lib", "docker", imageID + ".yaml")
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
		ConfigPath:  internalConfigPath,
		Handle:      handle,
		DeviceID:    deviceID,
	}
	data, _ := json.MarshalIndent(meta, "", "  ")
	err = os.WriteFile(filepath.Join(storeDir, instanceName+".json"), data, 0644)
	if err != nil {
		return fmt.Errorf("writing instance metadata: %w", err)
	}

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
		removeInstanceFiles(instanceName)
		fmt.Println("Done.")
	case err := <-done:
		if err != nil {
			fmt.Printf("Docker Compose exited with error: %v\n", err)
		}
		stopCompose(outputPath)
		removeInstanceFiles(instanceName)
		fmt.Println("Done.")
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

func removeInstanceFiles(instanceName string) {
	fmt.Println("Cleaning up instance files...")
	
	meta, metaPath, err := loadInstanceMetadata(instanceName)
	if err != nil {
		fmt.Printf("Error loading instance metadata: %v\n", err)
	}
	composeFile := meta.ComposeFile
	configPath := meta.ConfigPath

	fmt.Printf("Cleaning up files from config path %s with compose file %s\n", configPath, composeFile)

	cleanupFromCompose(composeFile, configPath)
	tryRemoveFileAndDirectory(composeFile)
	tryRemoveFileAndDirectory(metaPath)
}

func cleanFilesFromLog(logPath, baseDir string) error {
	file, err := os.Open(logPath)
	if err != nil {
		return fmt.Errorf("opening log file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		relPath := strings.TrimSpace(scanner.Text())
		if relPath == "./" || relPath == "" {
			continue
		}

		absPath := filepath.Join(baseDir, relPath)

		info, err := os.Stat(absPath)
		if err != nil {
			// skip non-existent files
			continue
		}

		if info.IsDir() {
			// try to remove if it's already a dir and empty
			tryRemoveDirIfEmpty(absPath)
		} else {
			// remove file and possibly its parent
			tryRemoveFileAndDirectory(absPath)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scanning log file: %w", err)
	}
	return nil
}

func tryRemoveFileAndDirectory(filePath string) bool {
	if err := os.Remove(filePath); err != nil {
		fmt.Printf("Failed to remove file %s: %v\n", filePath, err)
		return false
	}
	dir := filepath.Dir(filePath)
	return tryRemoveDirIfEmpty(dir)
}

func tryRemoveDirIfEmpty(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) > 0 {
		return false
	}
	if err := os.Remove(dir); err != nil {
		fmt.Printf("Failed to remove directory %s: %v\n", dir, err)
		return false
	}
	return true
}

func cleanupFromCompose(composePath, internalConfigPath string) error {
	rawCompose, err := compose.LoadRawYAML(composePath)
	if err != nil {
		return fmt.Errorf("loading compose: %w", err)
	}

	fmt.Printf("rawCompose: %#v\n", rawCompose)

	services, ok := rawCompose["services"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("compose file missing 'services'")
	}

	for name, svc := range services {
		fmt.Printf("Cleaning up service: %s\n", name)
		svcMap, ok := svc.(map[string]interface{})
		if !ok {
			continue
		}

		imageName, ok := svcMap["image"].(string)
		if !ok || imageName == "" {
			continue
		}
		fmt.Printf("Found image: %s for service: %s\n", imageName, name)

		fmt.Printf("Cleaning up image: %s for service: %s\n", imageName, name)
		
		// get the image ID (to locate the extracted docker and log files)
		imageID, err := extractor.GetImageID(imageName)
		if err != nil {
			fmt.Printf("Skipping cleanup for %s (could not resolve image ID): %v\n", name, err)
			continue
		}

		// locate and clean up extracted files
		dockerPath := filepath.Join(internalConfigPath, "lib", "docker", imageID + ".yaml")
		logPath := filepath.Join(internalConfigPath, "lib", "logs", imageID + ".log")

		fmt.Printf("Cleaning files for image ID: %s\n", imageID)
		if err := cleanFilesFromLog(logPath, filepath.Join(internalConfigPath, "lib")); err != nil {
			fmt.Printf("Error cleaning files for %s: %v\n", imageID, err)
		}

		tryRemoveFileAndDirectory(dockerPath)
		tryRemoveFileAndDirectory(logPath)
	}

	return nil
}

func loadInstanceMetadata(instanceName string) (*InstanceMetadata, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, "", fmt.Errorf("could not determine user home directory: %w", err)
	}

	metaPath := filepath.Join(home, ".darwin_cli", "instances", instanceName + ".json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, "", fmt.Errorf("could not read metadata file: %w", err)
	}

	var meta InstanceMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, "", fmt.Errorf("could not unmarshal metadata: %w", err)
	}

	return &meta, metaPath, nil
}
