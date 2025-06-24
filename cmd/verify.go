package cmd

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/enescakir/emoji"
	"github.com/spf13/cobra"

	"darwin_cli/internal/compose"
	"darwin_cli/internal/extractor"
	"darwin_cli/internal/io"
)

//go:embed scripts/extract.sh
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
		err := verify(imageName, verifyEnvFile)
		if err != nil {
			return fmt.Errorf("%s verification failed: %w", emoji.CrossMark, err)
		}
		fmt.Printf("%s Verification completed successfully.\n", emoji.CheckMarkButton)
		return nil
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
		env, err = compose.LoadEnvFile(resolvedEnvFile)
		if err != nil {
			return fmt.Errorf("loading .env: %w", err)
		}
	}
	for _, e := range os.Environ() {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			if _, exists := env[parts[0]]; !exists {
				env[parts[0]] = parts[1]
			}
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
	content, err := defaultVerifyEntrypoint.ReadFile("scripts/extract.sh")
	if err != nil {
		return fmt.Errorf("failed to read embedded script: %w", err)
	}

	verifyEntrypoint := filepath.Join(libPath, "extract.sh")
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

	// if docker, the entrypoint must be provided wrt the host filesystem
	if isDocker == "true" {
		verifyEntrypoint = filepath.Join(hostLibPath, "extract.sh")
	}

	// make temporary directory
	tempDir, err := os.MkdirTemp(libPath, "verify-export-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	deleteTempDir := tempDir
	defer func() {
		if err := os.RemoveAll(deleteTempDir); err != nil {
			fmt.Printf("failed to remove tempDir: %v\n", err)
		}
	}()

	// if docker, the temp dir must be provided wrt the host filesystem
	if isDocker == "true" {
		// if running in Docker, we need to mount the tempDir to the container
		relPath, err := filepath.Rel(libPath, tempDir)
		if err != nil {
			return fmt.Errorf("failed to get relative path from libPath to tempDir: %w", err)
		}
		tempDir = filepath.Join(hostLibPath, relPath)
	}

	// hardcoding these to be 1000 because some containers that will run the verify command do need to be run as root (0:0)
	uid := 1000 //os.Getuid()
	gid := 1000 //os.Getgid()

	// make sure /ws exists and has proper ownership
	checkCmd := exec.Command("docker", "run", "--rm",
		"--entrypoint", "stat",
		imageName,
		"-c", "%u:%g", "/ws")
	output, err := checkCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to stat /ws in container: %w\nOutput: %s", err, string(output))
	}
	owner := strings.TrimSpace(string(output))
	if owner != fmt.Sprintf("%d:%d", uid, gid) {
		return fmt.Errorf("/ws in container %s has wrong ownership: got %s, expected %d:%d", imageName, owner, uid, gid)
	}
	checkDirCmd := exec.Command("docker", "run", "--rm",
		"--entrypoint", "sh",
		imageName,
		"-c", "test -d /ws")
	if err := checkDirCmd.Run(); err != nil {
		return fmt.Errorf("/ws does not exist or is not a directory in image %q", imageName)
	}

	// run the extraction step
	_, err = extractor.ExtractImage(imageName, "darwin", tempDir, verifyEntrypoint)
	if err != nil {
		return fmt.Errorf("failed to extract export dependencies from image; recommend checking ownership of export files inside the container: %w", err)
	}

	// walk the temp dir and make sure the files are owned by the user (usiing local file system temp dir)
	var mismatches []string
	err = filepath.Walk(deleteTempDir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("unable to get raw stat info for %s", path)
		}

		if stat.Uid != uint32(uid) || stat.Gid != uint32(gid) {
			msg := fmt.Sprintf("MISMATCH: %s | UID: %d (expected %d), GID: %d (expected %d)", path, stat.Uid, uid, stat.Gid, gid)
			mismatches = append(mismatches, msg)
		} else {
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to walk temporary directory: %w", err)
	}

	if len(mismatches) > 0 {
		return fmt.Errorf("ownership verification failed for %d file(s):\n%s",
			len(mismatches), strings.Join(mismatches, "\n"))
	}

	return nil
}
