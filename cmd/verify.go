package cmd

import (
	"embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/enescakir/emoji"
	"github.com/spf13/cobra"

	"darwin_cli/internal/compose"
	"darwin_cli/internal/io"
)

//go:embed scripts/verify.sh
var defaultVerifyEntrypoint embed.FS

var (
	verifyEnvFile string
)

func init() {
	verifyCmd.Flags().StringVar(&verifyEnvFile, "env-file", "", "Optional path to .env file")
}

var verifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Checks if a Docker image is compliant with Darwin's standards",
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) < 1 {
			return fmt.Errorf("image name is required")
		}
		imageName := args[0]
		return verify(imageName, verifyEnvFile)
	},
}

// get entrypoint
func verify(imageName string, envFile string) error {
	inspectCmd := exec.Command("docker", "image", "inspect", imageName)
	if err := inspectCmd.Run(); err != nil {
		return fmt.Errorf("docker image %q not found locally: %w", imageName, err)
	}

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

	// validate required environment vars
	libPath, ok := env["DARWIN_LIB"]
	if !ok || strings.TrimSpace(libPath) == "" {
		return fmt.Errorf("environment variable DARWIN_LIB is not set or empty")
	}

	isDocker := env["DARWIN_IS_DOCKER"]
	var hostLibPath string
	if isDocker == "true" {
		var ok bool
		hostLibPath, ok = env["DARWIN_HOST_LIB"]
		if !ok || strings.TrimSpace(hostLibPath) == "" {
			return fmt.Errorf("environment variable DARWIN_HOST_LIB is required when running in Docker; it should be an absolute path in the host filesystem that points to the Docker mounted LIB_PATH")
		}
	}

	// get embedded verification script
	content, err := defaultVerifyEntrypoint.ReadFile("scripts/verify.sh")
	if err != nil {
		return fmt.Errorf("failed to read embedded script: %w", err)
	}

	verifyEntrypoint := filepath.Join(libPath, "verify.sh")
	if err := os.WriteFile(verifyEntrypoint, content, 0755); err != nil {
		return fmt.Errorf("failed to write temp script: %w", err)
	}

	// save the path that was written for deletion at end
	deleteVerifyEntrypoint := verifyEntrypoint
	defer func() {
		if err := os.Remove(deleteVerifyEntrypoint); err != nil {
			fmt.Printf("failed to remove temp script: %v\n", err)
		}
	}()

	// if docker, the entrypint must be provided wrt the host filesystem
	if isDocker == "true" {
		verifyEntrypoint = filepath.Join(hostLibPath, "verify.sh")
	}

	// now run with the entrypoint and see if it works
	cmd := exec.Command(
		"docker", "run", "--rm",
		"-v", fmt.Sprintf("%s:/verify.sh", verifyEntrypoint),
		"--entrypoint", "/verify.sh",
		imageName,
	)

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err = cmd.Run()
	if err != nil {
		fmt.Printf("%s Verification failed for image %s: %v\n", emoji.CrossMark, imageName, err)
	} else {
		fmt.Printf("%s Verification succeeded for image %s\n", emoji.CheckMarkButton, imageName)
	}

	return err
}
