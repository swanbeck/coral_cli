package util

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func CheckSkopeo() error {
	if _, err := exec.LookPath("skopeo"); err != nil {
		return fmt.Errorf("skopeo is not installed or not in PATH (it can be installed via apt with `apt-get update && apt-get install skopeo`): %w", err)
	}
	return nil
}

// converts "registry/repo/image:tag" to "image-tag.tar".
func ImageToFilename(image string) string {
	base := filepath.Base(image)
	safe := strings.ReplaceAll(base, ":", "-")
	safe = strings.ReplaceAll(safe, "/", "-")
	return safe + ".tar"
}

// inspects an OCI archive and returns "name:version" derived from org.opencontainers.image.title and org.opencontainers.image.version labels
func GetTitleLabel(filePath string) (string, error) {
	cmd := exec.Command("skopeo", "inspect", "oci-archive:"+filePath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("skopeo inspect: %w", err)
	}
	var result struct {
		Labels map[string]string `json:"Labels"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return "", fmt.Errorf("parsing skopeo inspect output: %w", err)
	}
	name := result.Labels["org.opencontainers.image.title"]
	version := result.Labels["org.opencontainers.image.version"]
	if name == "" || version == "" {
		return "", fmt.Errorf("image is missing required labels (org.opencontainers.image.title=%q, org.opencontainers.image.version=%q); use --name to specify the target image:tag", name, version)
	}
	return name + ":" + version, nil
}
