package extractor

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func ExtractImage(image string, name string, libPath string, extractionEntrypoint string) (string, error) {
	imageID, err := GetImageID(image)
	if err != nil {
		return "", fmt.Errorf("failed to get image ID: %w", err)
	}
	imageID = imageID + "-coral-" + name

	cmd := exec.Command(
		"docker", "run", "--rm",
		"--name", "coral-"+name,
		"-e", fmt.Sprintf("IMAGE_ID=%s", imageID),
		"-e", "EXPORT_PATH=/export",
		"-v", fmt.Sprintf("%s:/export", libPath),
		"-v", fmt.Sprintf("%s:/extract.sh", extractionEntrypoint),
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"--entrypoint", "/extract.sh",
		image,
	)

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return imageID, cmd.Run()
}

func GetImageID(image string) (string, error) {
	inspectCmd := exec.Command("docker", "inspect", "--format={{.Id}}", image)
	output, err := inspectCmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get image ID: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}
