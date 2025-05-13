package cmd

import (
	"fmt"
	"os"
	// "path/filepath"

	"github.com/spf13/cobra"

	"darwin_cli/internal/compose"
	// "darwin_cli/internal/extractor"
)

func init() {
	rootCmd.AddCommand(launchCmd)
}

var (
	composePath string
	envFile     string
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

	configPath := env["HOST_CONFIG_PATH"]
	if configPath == "" {
		return fmt.Errorf("HOST_CONFIG_PATH is required to be set!")
	}

	for name, service := range cf.Services {
		fmt.Printf("Extracting image for service: %s with corresponding image: %s\n", name, service.Image)
		err := extractor.ExtractImage(service.Image, "darwin-"+name, configPath)
		// if err != nil {
		// 	return fmt.Errorf("failed to extract image %s: %w", service.Image, err)
		// }
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
