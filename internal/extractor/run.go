package extractor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func ExtractImage(image, containerName, configPath string) (string, error) {
	libPath := filepath.Join(configPath, "lib")
	exportScript := filepath.Join(configPath, "export.sh")

	inspectCmd := exec.Command("docker", "inspect", "--format={{.Id}}", image)
	output, err := inspectCmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get image ID: %w", err)
	}
	imageID := strings.TrimSpace(string(output))

	cmd := exec.Command(
		"docker", "run", "--rm",
		"--name", containerName,
		"-e", fmt.Sprintf("IMAGE_ID=%s", imageID),
		"-e", "EXPORT_PATH=/export",
		"-v", fmt.Sprintf("%s:/export", libPath),
		"-v", fmt.Sprintf("%s:/export.sh", exportScript),
		"--entrypoint", "/export.sh",
		image,
	)

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return imageID, cmd.Run()
}
