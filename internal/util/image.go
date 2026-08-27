package util

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
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
