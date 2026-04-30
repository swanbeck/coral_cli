package extractor

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/google/uuid"
	"coral_cli/internal/logging"
)

// ExtractLibraries probes an image by creating a stopped container, reading LIB_PATH
// from its environment, copying the library tree to staging/<imageID>/ under lib, then
// removing the probe container.  The docker.yaml found in the staging directory (if any)
// is moved to docker/<imageID>.yaml so buildMergedCompose can pick it up as before.
//
// If the staging directory for this imageID already exists the function is a no-op
// (idempotent — same image, same content).
//
// The caller is responsible for recording the extraction in the registry.
func ExtractLibraries(image, name, lib string) (stagingDir string, imageID string, err error) {
	imageID, err = GetImageID(image)
	if err != nil {
		return "", "", fmt.Errorf("getting image ID for %s: %w", image, err)
	}
	imageID = imageID + "-coral-" + name
	stagingDir = filepath.Join(lib, "staging", imageID)

	// Idempotent: skip if already extracted for this image.
	if _, statErr := os.Stat(stagingDir); statErr == nil {
		return stagingDir, imageID, nil
	}

	probeName := fmt.Sprintf("coral-probe-%s", uuid.New())
	createCmd := exec.Command("docker", "create", "--name", probeName, image)
	createCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := createCmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("creating probe container for %s: %w", image, err)
	}
	containerID := strings.TrimSpace(string(out))

	defer func() {
		rmCmd := exec.Command("docker", "rm", containerID)
		rmCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		rmCmd.Run() // best-effort
	}()

	libPath, err := readContainerEnv(containerID, "LIB_PATH")
	if err != nil {
		return "", "", fmt.Errorf("reading LIB_PATH from %s: %w", image, err)
	}
	if libPath == "" {
		return "", "", fmt.Errorf("image %s does not set LIB_PATH", image)
	}

	if err := os.MkdirAll(stagingDir, 0755); err != nil {
		return "", "", fmt.Errorf("creating staging dir: %w", err)
	}

	// docker cp streams through the socket — no host-path translation needed even when
	// CORAL itself is running inside a container.
	cpCmd := exec.Command("docker", "cp",
		fmt.Sprintf("%s:%s/.", containerID, libPath), // trailing "/." = copy contents
		stagingDir)
	cpCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cpCmd.Stdout = os.Stdout
	cpCmd.Stderr = os.Stderr
	if err := cpCmd.Run(); err != nil {
		os.RemoveAll(stagingDir)
		return "", "", fmt.Errorf("copying from probe container for %s: %w", image, err)
	}

	// Move docker.yaml to the conventional docker/<imageID>.yaml location so that
	// buildMergedCompose finds it exactly as before.
	srcYAML := filepath.Join(stagingDir, "docker.yaml")
	if _, statErr := os.Stat(srcYAML); statErr == nil {
		dstYAML := filepath.Join(lib, "docker", imageID+".yaml")
		if mkErr := os.MkdirAll(filepath.Dir(dstYAML), 0755); mkErr == nil {
			os.Rename(srcYAML, dstYAML)
		}
	}

	fmt.Println(logging.Info(fmt.Sprintf(
		"Extracted libraries from %s for %s", image, logging.BoldMagenta(name),
	)))
	return stagingDir, imageID, nil
}

// readContainerEnv inspects a stopped container and returns the value of the named
// environment variable, or "" if not set.
func readContainerEnv(containerID, varName string) (string, error) {
	cmd := exec.Command("docker", "inspect",
		"--format", "{{json .Config.Env}}",
		containerID)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("inspecting container env: %w", err)
	}
	var envs []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &envs); err != nil {
		return "", fmt.Errorf("parsing container env JSON: %w", err)
	}
	prefix := varName + "="
	for _, e := range envs {
		if strings.HasPrefix(e, prefix) {
			return e[len(prefix):], nil
		}
	}
	return "", nil
}

// GetImageID returns the full image digest for the named image, pulling it if absent.
func GetImageID(image string) (string, error) {
	inspectCmd := exec.Command("docker", "inspect", "--format={{.Id}}", image)
	inspectCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if out, err := inspectCmd.Output(); err == nil {
		return strings.TrimSpace(string(out)), nil
	}

	fmt.Println(logging.Info(fmt.Sprintf("Image %s not found locally — pulling...", image)))
	tmpFile, err := os.CreateTemp("", "compose-*.yml")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmpFile.Name())
	if _, err := fmt.Fprintf(tmpFile, "services:\n  coral:\n    image: %s\n", image); err != nil {
		return "", err
	}
	tmpFile.Close()

	pullCmd := exec.Command("docker", "compose", "-f", tmpFile.Name(), "pull")
	pullCmd.Stdout = os.Stdout
	pullCmd.Stderr = os.Stderr
	if err := pullCmd.Run(); err != nil {
		return "", fmt.Errorf("pulling image %s: %w", image, err)
	}

	inspectCmd = exec.Command("docker", "inspect", "--format={{.Id}}", image)
	inspectCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := inspectCmd.Output()
	if err != nil {
		return "", fmt.Errorf("inspecting image after pull: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
