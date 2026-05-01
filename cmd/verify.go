package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"coral_cli/internal/extractor"
	"coral_cli/internal/logging"
)

var (
	verifyLibDir string
)

func init() {
	verifyCmd.Flags().StringVar(&verifyLibDir, "lib-dir", "", "Directory to use for staging during verification (defaults to a temp dir)")

	verifyCmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		out, err := exec.Command("docker", "images", "--format", "{{.Repository}}:{{.Tag}}").Output()
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		var matches []string
		for _, image := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if strings.HasPrefix(image, toComplete) {
				matches = append(matches, image)
			}
		}
		return matches, cobra.ShellCompDirectiveNoFileComp
	}
}

var verifyCmd = &cobra.Command{
	Use:   "verify <image-name>",
	Short: "Checks if a component is compliant with Coral's standards",
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) < 1 {
			return fmt.Errorf("image name is required")
		}
		if err := verify(args[0], verifyLibDir); err != nil {
			return fmt.Errorf("%s: %w", logging.Failure("verification failed"), err)
		}
		fmt.Println(logging.Success("Image is compliant with Coral's standards"))
		return nil
	},
}

func verify(imageName string, libDir string) error {
	if err := exec.Command("docker", "image", "inspect", imageName).Run(); err != nil {
		return fmt.Errorf("docker image %q not found locally: %w", imageName, err)
	}

	// Use a temp lib directory for extraction so verify leaves no lasting state.
	tmpLib, err := os.MkdirTemp("", "coral-verify-*")
	if err != nil {
		return fmt.Errorf("creating temp lib dir: %w", err)
	}
	defer os.RemoveAll(tmpLib)
	if libDir != "" {
		tmpLib = libDir
	}

	_, _, err = extractor.ExtractLibraries(imageName, "verify", tmpLib)
	if err != nil {
		return fmt.Errorf("extraction failed — ensure LIB_PATH is set and contains behaviors/ and interfaces/: %w", err)
	}

	fmt.Println(logging.Info("LIB_PATH is set and library extraction succeeded"))
	return nil
}
