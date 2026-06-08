package cmd

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"coral_cli/internal/libs"
	"coral_cli/internal/logging"
	"coral_cli/internal/runtime"
)

var verifyLibDir string

func init() {
	verifyCmd.Flags().StringVar(&verifyLibDir, "lib-dir", "", "Directory to use for staging during verification (defaults to a temp dir)")

	verifyCmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		out, err := exec.Command(runtime.Current.Binary, "images", "--format", "{{.Repository}}:{{.Tag}}").Output()
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
	if err := runtime.Check(); err != nil {
		return err
	}
	if err := exec.Command(runtime.Current.Binary, "image", "inspect", imageName).Run(); err != nil {
		return fmt.Errorf("docker image %q not found locally: %w", imageName, err)
	}

	labels, err := libs.GetImageLabels(imageName)
	if err != nil {
		return fmt.Errorf("reading image labels: %w", err)
	}
	profile, ok := labels["coral.profile"]
	if !ok || profile == "" {
		return fmt.Errorf("missing required label 'coral.profile'; must be one of: drivers, skillsets, executors")
	}
	if !validProfiles[profile] {
		return fmt.Errorf("invalid coral.profile=%q; must be one of: drivers, skillsets, executors", profile)
	}
	fmt.Println(logging.Info(fmt.Sprintf("coral.profile=%q", profile)))

	title := labels["org.opencontainers.image.title"]
	if title == "" {
		return fmt.Errorf("missing required label 'org.opencontainers.image.title'")
	}
	if !strings.HasPrefix(title, "coral-") {
		return fmt.Errorf("org.opencontainers.image.title=%q must be prefixed with \"coral-\"", title)
	}
	fmt.Println(logging.Info(fmt.Sprintf("org.opencontainers.image.title=%q", title)))

	ociVersion := labels["org.opencontainers.image.version"]
	coralVersion := labels["coral.version"]
	if ociVersion == "" {
		return fmt.Errorf("missing required label 'org.opencontainers.image.version'")
	}
	if coralVersion == "" {
		return fmt.Errorf("missing required label 'coral.version'")
	}
	if ociVersion != coralVersion {
		return fmt.Errorf("label mismatch: org.opencontainers.image.version=%q does not match coral.version=%q", ociVersion, coralVersion)
	}
	fmt.Println(logging.Info(fmt.Sprintf("org.opencontainers.image.version=%q (matches coral.version)", ociVersion)))

	libPath, err := readImageEnv(imageName, "CORAL_EXPORT_LIB")
	if err != nil {
		return fmt.Errorf("reading CORAL_EXPORT_LIB from image: %w", err)
	}

	if libPath == "" {
		if profile == "skillsets" {
			return fmt.Errorf("skillsets profile requires CORAL_EXPORT_LIB to be set in the image")
		}
		fmt.Println(logging.Info("CORAL_EXPORT_LIB not set"))
		return nil
	}

	tmpDir, err := os.MkdirTemp("", "coral-verify-*")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	if libDir != "" {
		tmpDir = libDir
	}

	uid := uuid.New()
	probeName := fmt.Sprintf("coral-probe-%x", uid[:4])
	createCmd := exec.Command(runtime.Current.Binary, "create", "--name", probeName, imageName)
	createCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	createOut, err := createCmd.Output()
	if err != nil {
		return fmt.Errorf("creating probe container: %w", err)
	}
	containerID := strings.TrimSpace(string(createOut))
	defer func() {
		rmCmd := exec.Command(runtime.Current.Binary, "rm", containerID)
		rmCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		rmCmd.Run()
	}()

	cpCmd := exec.Command(runtime.Current.Binary, "cp",
		fmt.Sprintf("%s:%s", containerID, libPath),
		tmpDir)
	cpCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if cpOut, err := cpCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("copying CORAL_EXPORT_LIB from container: %w\n%s", err, cpOut)
	}

	exportedDir := filepath.Join(tmpDir, filepath.Base(libPath))

	info, err := os.Stat(exportedDir)
	if err != nil {
		return fmt.Errorf("stating exported lib dir: %w", err)
	}
	mode := info.Mode().Perm()
	if mode&0005 != 0005 {
		return fmt.Errorf(
			"CORAL_EXPORT_LIB=%s lacks world read+execute permissions (mode %04o); add 'RUN chmod o+rx %s' to the Dockerfile",
			libPath, mode, libPath,
		)
	}
	fmt.Println(logging.Info(fmt.Sprintf("CORAL_EXPORT_LIB=%s permissions OK (%04o)", libPath, mode)))

	entries, err := os.ReadDir(exportedDir)
	if err != nil {
		return fmt.Errorf("reading exported lib dir: %w", err)
	}
	if len(entries) == 0 {
		return fmt.Errorf(
			"CORAL_EXPORT_LIB=%s is empty; ensure the Dockerfile populates it before the image is built",
			libPath,
		)
	}
	fmt.Println(logging.Info(fmt.Sprintf("CORAL_EXPORT_LIB=%s is populated (%d entries)", libPath, len(entries))))

	if profile == "skillsets" {
		var behaviorLibs []string
		if err := filepath.WalkDir(exportedDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			name := d.Name()
			if !d.IsDir() && strings.HasPrefix(name, "lib") && strings.HasSuffix(name, "behaviors.so") {
				behaviorLibs = append(behaviorLibs, name)
			}
			return nil
		}); err != nil {
			return fmt.Errorf("scanning CORAL_EXPORT_LIB for behavior libraries: %w", err)
		}
		if len(behaviorLibs) == 0 {
			return fmt.Errorf(
				"skillsets profile requires at least one lib*behaviors.so in CORAL_EXPORT_LIB=%s; found none",
				libPath,
			)
		}
		fmt.Println(logging.Info(fmt.Sprintf("Found behavior lib(s): %s", strings.Join(behaviorLibs, ", "))))
	}

	return nil
}

// reads an environment variable baked into an image without creating a container
func readImageEnv(image, varName string) (string, error) {
	out, err := exec.Command(runtime.Current.Binary, "inspect", "--format", "{{json .Config.Env}}", image).Output()
	if err != nil {
		return "", fmt.Errorf("inspecting image env: %w", err)
	}
	var envs []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &envs); err != nil {
		return "", fmt.Errorf("parsing image env JSON: %w", err)
	}
	prefix := varName + "="
	for _, e := range envs {
		if strings.HasPrefix(e, prefix) {
			return e[len(prefix):], nil
		}
	}
	return "", nil
}
