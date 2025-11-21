package extractor

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"coral_cli/internal/io"
	"coral_cli/internal/logging"
)

func ExtractImage(image string, name string, libPath string, extractionEntrypoint string) (string, error) {
	imageID, err := GetImageID(image)
	if err != nil {
		return "", fmt.Errorf("failed to get image ID: %w", err)
	}
	imageID = imageID + "-coral-" + name

	uid, err := io.GetUID()
	if err != nil {
		return "", fmt.Errorf("failed to get UID: %w", err)
	}
	gid, err := io.GetGID()
	if err != nil {
		return "", fmt.Errorf("failed to get GID: %w", err)
	}

	cmd := exec.Command(
		"docker", "run", "--rm",
		"--name", "coral-"+name,
		"-e", fmt.Sprintf("IMAGE_ID=%s", imageID),
		"-e", "EXPORT_PATH=/export",
		"-v", fmt.Sprintf("%s:/export", libPath),
		"-v", fmt.Sprintf("%s:/extract.sh", extractionEntrypoint),
		"--user", fmt.Sprintf("%d:%d", uid, gid),
		"--entrypoint", "/extract.sh",
		image,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return imageID, cmd.Run()
}

func GetImageID(image string) (string, error) {
	inspectCmd := exec.Command("docker", "inspect", "--format={{.Id}}", image)
	inspectCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	output, err := inspectCmd.Output()
	if err == nil {
		return strings.TrimSpace(string(output)), nil
	}

	fmt.Println(logging.Info(fmt.Sprintf("Image %s not found locally. Attempting to pull...", image)))
	tmpFile, err := os.CreateTemp("", "compose-*.yml")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmpFile.Name())

	composeYAML := fmt.Sprintf("services:\n  coral:\n    image: %s\n", image)
	if _, err := tmpFile.WriteString(composeYAML); err != nil {
		return "", err
	}
	tmpFile.Close()

	pullCmd := exec.Command("docker", "compose", "-f", tmpFile.Name(), "pull")
	pullCmd.Stdout = os.Stdout
	pullCmd.Stderr = os.Stderr
	if err := pullCmd.Run(); err != nil {
		return "", fmt.Errorf("failed to pull image %s: %w", image, err)
	}

	inspectCmd = exec.Command("docker", "inspect", "--format={{.Id}}", image)
	inspectCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	output, err = inspectCmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to inspect image after pull: %w", err)
	}

	return strings.TrimSpace(string(output)), nil
}
