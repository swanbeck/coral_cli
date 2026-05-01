package cleanup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"coral_cli/internal/compose"
	"coral_cli/internal/extractor"
	"coral_cli/internal/health"
	"coral_cli/internal/metadata"
	"coral_cli/internal/registry"
)

func StopCompose(instanceName string, composePath string, kill bool, profiles []string) error {
	args := []string{"compose", "-p", instanceName, "-f", composePath}
	for _, profile := range profiles {
		args = append(args, "--profile", profile)
	}

	if kill {
		killArgs := append(args, "kill")
		killCmd := exec.Command("docker", killArgs...)
		killCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		killCmd.Stdout = os.Stdout
		killCmd.Stderr = os.Stderr
		if err := killCmd.Run(); err != nil {
			return fmt.Errorf("killing compose: %w", err)
		}
	}

	downCmd := exec.Command("docker", append(args, "down")...)
	downCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	downCmd.Stdout = os.Stdout
	downCmd.Stderr = os.Stderr
	return downCmd.Run()
}

func RemoveInstanceFiles(instanceName string) error {
	meta, metaPath, err := metadata.LoadInstanceMetadata(instanceName)
	if err != nil {
		fmt.Printf("Error loading instance metadata: %v\n", err)
	}
	composeFile := meta.ComposeFile
	libPath := meta.LibPath

	defer tryRemoveFileAndDirectory(composeFile)
	defer tryRemoveFileAndDirectory(metaPath)

	// Load registry to remove extraction + injection records and staging dirs.
	reg, regErr := registry.Load(libPath)
	if regErr != nil {
		fmt.Printf("Warning: could not load registry, skipping registry cleanup: %v\n", regErr)
		// Fall back to legacy docker-inspect-based cleanup.
		cleanErr := legacyCleanupFromCompose(composeFile, libPath)
		tryRemoveDirIfEmpty(filepath.Join(libPath, "staging"))
		return cleanErr
	}

	cleanErr := cleanupFromCompose(instanceName, reg)
	tryRemoveDirIfEmpty(filepath.Join(libPath, "staging"))
	if err := reg.CleanupIfEmpty(); err != nil {
		fmt.Printf("Warning: cleaning up registry: %v\n", err)
	}
	return cleanErr
}

// removes staging directories and records using the registry for fast lookups rather than re-inspecting images
func cleanupFromCompose(instanceName string, reg *registry.Registry) error {
	// Remove injection records for all containers in this instance.
	containerIDs, _ := health.GetContainerIDsForProject(instanceName)
	for _, cid := range containerIDs {
		if err := reg.RemoveInjection(cid); err != nil {
			fmt.Printf("Warning: removing injection record for %s: %v\n", cid[:12], err)
		}
	}
	// Belt-and-suspenders: also sweep by instanceID in case containers are already gone.
	if err := reg.RemoveInjectionsForInstance(instanceName); err != nil {
		fmt.Printf("Warning: sweeping injection records for %s: %v\n", instanceName, err)
	}

	// Remove extraction records and staging directories (docker.yaml lives inside the
	// staging dir, so os.RemoveAll handles it without a separate pass).
	stagingDirs, err := reg.RemoveExtractionsForInstance(instanceName)
	if err != nil {
		fmt.Printf("Warning: removing extraction records for %s: %v\n", instanceName, err)
	}
	var warn string
	for _, dir := range stagingDirs {
		if err := os.RemoveAll(dir); err != nil {
			warn += fmt.Sprintf("removing staging dir %s: %v\n", dir, err)
		}
	}

	if warn != "" {
		return fmt.Errorf("%s", strings.TrimSpace(warn))
	}
	return nil
}

// pre-registry fallback used when the registry file cannot be loaded; re-inspects each image and removes the staging directory if found, without touching any registry
func legacyCleanupFromCompose(composePath, libPath string) error {
	rawCompose, err := compose.LoadRawYAML(composePath)
	if err != nil {
		return fmt.Errorf("loading compose: %w", err)
	}
	services, ok := rawCompose["services"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("compose file missing 'services'")
	}
	var warn string
	for name, svc := range services {
		svcMap, ok := svc.(map[string]interface{})
		if !ok {
			continue
		}
		imageName, ok := svcMap["image"].(string)
		if !ok || imageName == "" {
			continue
		}
		imageID, err := extractor.GetImageID(imageName)
		if err != nil {
			fmt.Printf("Skipping cleanup for %s: %v\n", name, err)
			continue
		}
		imageID = imageID + "-coral-" + name
		stagingDir := filepath.Join(libPath, "staging", imageID)
		if err := os.RemoveAll(stagingDir); err != nil && !os.IsNotExist(err) {
			warn += fmt.Sprintf("removing %s: %v\n", stagingDir, err)
		}
	}
	if warn != "" {
		return fmt.Errorf("%s", strings.TrimSpace(warn))
	}
	return nil
}

func tryRemoveFileAndDirectory(filePath string) bool {
	if err := os.Remove(filePath); err != nil {
		return false
	}
	return tryRemoveDirIfEmpty(filepath.Dir(filePath))
}

func tryRemoveDirIfEmpty(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) > 0 {
		return false
	}
	if err := os.Remove(dir); err != nil {
		fmt.Printf("Failed to remove directory %s: %v\n", dir, err)
		return false
	}
	return true
}
