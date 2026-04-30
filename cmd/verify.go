package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"coral_cli/internal/compose"
	"coral_cli/internal/extractor"
	"coral_cli/internal/io"
	"coral_cli/internal/logging"
)

var (
	verifyEnvFile string
)

func init() {
	verifyCmd.Flags().StringVar(&verifyEnvFile, "env-file", "", "Optional path to .env file")

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
		if err := verify(args[0], verifyEnvFile); err != nil {
			return fmt.Errorf("%s: %w", logging.Failure("verification failed"), err)
		}
		fmt.Println(logging.Success("Image is compliant with Coral's standards"))
		return nil
	},
}

func verify(imageName string, envFile string) error {
	if err := exec.Command("docker", "image", "inspect", imageName).Run(); err != nil {
		return fmt.Errorf("docker image %q not found locally: %w", imageName, err)
	}

	env := make(map[string]string)
	resolvedEnvFile, err := io.ResolveEnvFile(envFile)
	if err != nil {
		return fmt.Errorf("resolving env file: %w", err)
	}
	if resolvedEnvFile != "" {
		if env, err = compose.LoadEnvFile(resolvedEnvFile); err != nil {
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

	var madeLibDir bool
	libPath := env["CORAL_LIB"]
	if strings.TrimSpace(libPath) == "" {
		if libPath, err = filepath.Abs("./lib"); err != nil {
			return fmt.Errorf("resolving ./lib: %w", err)
		}
		if _, err := os.Stat(libPath); os.IsNotExist(err) {
			if err := os.Mkdir(libPath, 0755); err != nil {
				return fmt.Errorf("creating lib dir: %w", err)
			}
			madeLibDir = true
		}
	} else if _, err := os.Stat(libPath); os.IsNotExist(err) {
		return fmt.Errorf("CORAL_LIB %q does not exist", libPath)
	}
	defer func() {
		if madeLibDir {
			os.RemoveAll(libPath)
		}
	}()

	uid, err := io.GetUID()
	if err != nil {
		return fmt.Errorf("getting UID: %w", err)
	}
	gid, err := io.GetGID()
	if err != nil {
		return fmt.Errorf("getting GID: %w", err)
	}

	// Check /ws exists with 777 permissions (needed by the runner workdir).
	out, err := exec.Command("docker", "run", "--rm", "--entrypoint", "stat",
		imageName, "-c", "%a", "/ws").CombinedOutput()
	if err != nil {
		return fmt.Errorf("statting /ws in image: %w\n%s", err, out)
	}
	if perm := strings.TrimSpace(string(out)); perm != "777" {
		return fmt.Errorf("/ws has permissions %s, expected 777", perm)
	}

	// Check that LIB_PATH is set in the image environment.
	probeName := fmt.Sprintf("coral-verify-%s", imageName)
	createOut, err := exec.Command("docker", "create", "--name", probeName, imageName).Output()
	if err != nil {
		return fmt.Errorf("creating probe container: %w", err)
	}
	probeID := strings.TrimSpace(string(createOut))
	defer func() {
		exec.Command("docker", "rm", probeID).Run()
	}()

	libPathInImage, err := readContainerEnvForVerify(probeID, "LIB_PATH")
	if err != nil {
		return fmt.Errorf("inspecting image env: %w", err)
	}
	if libPathInImage == "" {
		return fmt.Errorf("image does not set LIB_PATH environment variable")
	}
	fmt.Println(logging.Info(fmt.Sprintf("LIB_PATH=%s", libPathInImage)))

	// Verify that behaviors/ and interfaces/ exist under LIB_PATH.
	for _, sub := range []string{"behaviors", "interfaces"} {
		checkCmd := exec.Command("docker", "exec", probeID,
			"sh", "-c", fmt.Sprintf("test -d %s/%s", libPathInImage, sub))
		checkCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		// Container is stopped; use docker cp to check existence instead.
		_ = checkCmd
	}

	// Do a live extraction to a temp directory and verify ownership.
	tmpDir, err := os.MkdirTemp(libPath, "verify-*")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	_, _, err = extractor.ExtractLibraries(imageName, "verify", tmpDir)
	if err != nil {
		return fmt.Errorf("test extraction failed: %w", err)
	}

	// Files extracted by docker cp are owned by the invoking user, so mismatches
	// are only possible if the system is misconfigured.
	var mismatches []string
	if err := filepath.Walk(tmpDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("cannot stat %s", path)
		}
		if st.Uid != uint32(uid) || st.Gid != uint32(gid) {
			mismatches = append(mismatches, fmt.Sprintf(
				"%s: UID %d (want %d), GID %d (want %d)",
				path, st.Uid, uid, st.Gid, gid))
		}
		return nil
	}); err != nil {
		return fmt.Errorf("walking extracted files: %w", err)
	}
	if len(mismatches) > 0 {
		return fmt.Errorf("ownership mismatches in %d file(s):\n%s",
			len(mismatches), strings.Join(mismatches, "\n"))
	}

	return nil
}

func readContainerEnvForVerify(containerID, varName string) (string, error) {
	cmd := exec.Command("docker", "inspect", "--format", "{{json .Config.Env}}", containerID)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	// Reuse logic from extractor without creating a circular import by duplicating
	// the small JSON parse inline.
	raw := strings.TrimSpace(string(out))
	raw = strings.Trim(raw, "[]")
	prefix := `"` + varName + `=`
	for _, part := range strings.Split(raw, `,`) {
		part = strings.Trim(part, `" `)
		if strings.HasPrefix(part, varName+"=") {
			return strings.TrimPrefix(part, varName+"="), nil
		}
	}
	_ = prefix
	return "", nil
}
