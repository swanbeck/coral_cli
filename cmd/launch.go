package cmd

import (
	"fmt"
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
	launchCmd.Flags().StringVarP(&composePath, "compose-file", "f", "", "Path to compose.yaml")
	launchCmd.Flags().StringVarP(&envFile, "env-file", "e", "", "Path to .env file")
	launchCmd.MarkFlagRequired("compose-file")
}

func launch(composePath, envFile string) error {
	env, err := compose.LoadEnv(envFile)
	if err != nil {
		return fmt.Errorf("loading .env: %w", err)
	}

	cf, err := compose.ParseCompose(composePath, env)
	if err != nil {
		return fmt.Errorf("parsing compose file: %w", err)
	}

	configPath := env["HOST_CONFIG_PATH"]
	if configPath == "" {
		return fmt.Errorf("HOST_CONFIG_PATH is required to be set!")
	}

	for name, service := range cf.Services {
		fmt.Printf("Extracting image for service: %s\n", name)
		fmt.Printf("Corresponding image: %s\n", service.Image)
		// err := extractor.ExtractImage(service.Image, "darwin-"+name, configPath)
		// if err != nil {
		// 	return fmt.Errorf("failed to extract image %s: %w", service.Image, err)
		// }
	}

	return nil
}
