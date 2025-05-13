package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	// "gopkg.in/yaml.v3"

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

	// var cf compose.ComposeFile
	// data, err := os.ReadFile(pathToUse)
	// err = yaml.Unmarshal(data, &cf)
	// if err != nil {
	// 	return fmt.Errorf("unmarshalling compose file: %w", err)
	// }

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
	
			// Merge extracted into base
			baseSvc := rawServices[name].(map[string]interface{})
			merged := compose.MergeServiceConfigs(baseSvc, extracted)
			mergedCompose["services"].(map[string]interface{})[name] = merged
			fmt.Printf("Merged compose for %s with extracted compose\n", name)
		} else {
			// Just use the base service if no extracted fragment exists
			mergedCompose["services"].(map[string]interface{})[name] = rawCompose[name]
			fmt.Printf("No extracted compose found for %s, using base service\n", name)
		}
	}
	
	fmt.Printf("Merged compose file: %v\n", mergedCompose)

	outputPath := filepath.Join(internalConfigPath, "lib", "compose", "merged-compose.yaml")
	fmt.Printf("Writing merged compose file to: %s\n", outputPath)

	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	if err := compose.SaveRawYAML(outputPath, mergedCompose); err != nil {
		return fmt.Errorf("writing merged compose file: %w", err)
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
