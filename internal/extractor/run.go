package extractor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func ExtractImage(image, containerName, configPath string) error {
	libPath := filepath.Join(configPath, "lib")
	exportScript := filepath.Join(configPath, "export.sh")

	cmd := exec.Command(
		"docker", "run", "--rm",
		"--name", containerName,
		"-v", fmt.Sprintf("%s:/export", libPath),
		"-e", "EXPORT_PATH=/export",
		"-v", fmt.Sprintf("%s:/export.sh", exportScript),
		"--entrypoint", "/export.sh",
		"-u", "0:0",
		image,
	)

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}
