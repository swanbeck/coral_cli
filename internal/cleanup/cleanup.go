package cleanup

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"darwin_cli/internal/compose"
	"darwin_cli/internal/extractor"
	"darwin_cli/internal/metadata"
)

func StopCompose(instanceName string, composePath string, kill bool, profiles []string) error {
	args := []string{"compose", "-p", instanceName, "-f", composePath}
	for _, profile := range profiles {
		args = append(args, "--profile", profile)
	}

	if kill {
		kill_args := append(args, "kill")
		kill_cmd := exec.Command("docker", kill_args...)
		kill_cmd.Stdout = os.Stdout
		kill_cmd.Stderr = os.Stderr
		if err := kill_cmd.Run(); err != nil {
			return fmt.Errorf("killing compose: %w", err)
		}
	}

	args = append(args, "down")
	cmd := exec.Command("docker", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func RemoveInstanceFiles(instanceName string) error {
	meta, metaPath, err := metadata.LoadInstanceMetadata(instanceName)
	if err != nil {
		fmt.Printf("Error loading instance metadata: %v\n", err)
	}
	composeFile := meta.ComposeFile
	libPath := meta.LibPath

	cleanupFromCompose(composeFile, libPath)
	tryRemoveFileAndDirectory(composeFile)
	tryRemoveFileAndDirectory(metaPath)

	return nil
}

func tryRemoveFileAndDirectory(filePath string) bool {
	if err := os.Remove(filePath); err != nil {
		// fmt.Printf("Failed to remove file %s: %v\n", filePath, err)
		return false
	}
	dir := filepath.Dir(filePath)
	return tryRemoveDirIfEmpty(dir)
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

func cleanFilesFromLog(logPath, baseDir string) error {
	file, err := os.Open(logPath)
	if err != nil {
		return fmt.Errorf("opening log file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		relPath := strings.TrimSpace(scanner.Text())
		if relPath == "./" || relPath == "" {
			continue
		}

		absPath := filepath.Join(baseDir, relPath)

		info, err := os.Stat(absPath)
		if err != nil {
			// skip non-existent files
			continue
		}

		if info.IsDir() {
			// try to remove if it's already a dir and empty
			tryRemoveDirIfEmpty(absPath)
		} else {
			// remove file and possibly its parent
			tryRemoveFileAndDirectory(absPath)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scanning log file: %w", err)
	}
	return nil
}

func cleanupFromCompose(composePath, libPath string) error {
	rawCompose, err := compose.LoadRawYAML(composePath)
	if err != nil {
		return fmt.Errorf("loading compose: %w", err)
	}

	services, ok := rawCompose["services"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("compose file missing 'services'")
	}

	for name, svc := range services {
		svcMap, ok := svc.(map[string]interface{})
		if !ok {
			continue
		}

		imageName, ok := svcMap["image"].(string)
		if !ok || imageName == "" {
			continue
		}
		// fmt.Printf("Found image %s for service %s\n", imageName, name)

		// get the image ID (to locate the extracted docker and log files)
		imageID, err := extractor.GetImageID(imageName)
		if err != nil {
			fmt.Printf("Skipping cleanup for %s (could not resolve image ID): %v\n", name, err)
			continue
		}
		imageID = imageID + "-darwin-" + name

		// locate and clean up extracted files
		dockerPath := filepath.Join(libPath, "docker", imageID+".yaml")
		logPath := filepath.Join(libPath, "logs", imageID+".log")

		if err := cleanFilesFromLog(logPath, libPath); err != nil {
			fmt.Printf("Error cleaning files for %s: %v\n", imageID, err)
		}

		tryRemoveFileAndDirectory(dockerPath)
		tryRemoveFileAndDirectory(logPath)
	}

	return nil
}
