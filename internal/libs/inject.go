package libs

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"coral_cli/internal/logging"
	"coral_cli/internal/registry"
)

type libEntry struct {
	srcPath   string
	mtime     time.Time
	payloadID string
}

// merges behavior and interface libraries from all active staging directories into the executor container at /home/loading/lib (the path set by LOADING_LIBRARY_PATH in the coral_runner image); when two payloads provide a file with the same name in the same subdirectory (behaviors/ or interfaces/), the file with the newer modification timestamp wins and the losing entry is recorded as shadowed in the returned slice
func InjectLibraries(containerID string, stagingDirs map[string]string) ([]registry.InjectedLib, error) {
	if len(stagingDirs) == 0 {
		return nil, nil
	}

	tmpDir, err := os.MkdirTemp("", "coral-inject-*")
	if err != nil {
		return nil, fmt.Errorf("creating merge dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	var result []registry.InjectedLib

	for _, subDir := range []string{"behaviors", "interfaces"} {
		winners := make(map[string]libEntry) // filename → current best
		var shadowedLibs []registry.InjectedLib

		for payloadID, stagingDir := range stagingDirs {
			srcSubDir := filepath.Join(stagingDir, subDir)
			entries, err := os.ReadDir(srcSubDir)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("reading %s from %s: %w", subDir, stagingDir, err)
			}
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				info, err := e.Info()
				if err != nil {
					continue
				}
				name := e.Name()
				entry := libEntry{
					srcPath:   filepath.Join(srcSubDir, name),
					mtime:     info.ModTime(),
					payloadID: payloadID,
				}
				existing, exists := winners[name]
				if !exists {
					winners[name] = entry
					continue
				}
				// newer modification timestamp wins; this cannot exist long-term but is simple and deterministic for now
				if entry.mtime.After(existing.mtime) {
					shadowedLibs = append(shadowedLibs, registry.InjectedLib{
						PayloadID:  existing.payloadID,
						LibName:    name,
						SubDir:     subDir,
						Shadowed:   true,
						ShadowedBy: payloadID,
					})
					winners[name] = entry
					fmt.Println(logging.Warning(fmt.Sprintf(
						"Library conflict: %s/%s — %s (newer, %.0fs) overrides %s",
						subDir, name, payloadID, entry.mtime.Sub(existing.mtime).Seconds(), existing.payloadID,
					)))
				} else {
					shadowedLibs = append(shadowedLibs, registry.InjectedLib{
						PayloadID:  payloadID,
						LibName:    name,
						SubDir:     subDir,
						Shadowed:   true,
						ShadowedBy: existing.payloadID,
					})
					fmt.Println(logging.Warning(fmt.Sprintf(
						"Library conflict: %s/%s — %s (newer, %.0fs) overrides %s",
						subDir, name, existing.payloadID, existing.mtime.Sub(entry.mtime).Seconds(), payloadID,
					)))
				}
			}
		}

		if len(winners) == 0 {
			continue
		}

		// copy winning files to the merge directory
		destSubDir := filepath.Join(tmpDir, subDir)
		if err := os.MkdirAll(destSubDir, 0755); err != nil {
			return nil, err
		}
		for name, entry := range winners {
			if err := copyFile(entry.srcPath, filepath.Join(destSubDir, name)); err != nil {
				return nil, fmt.Errorf("merging %s/%s: %w", subDir, name, err)
			}
			result = append(result, registry.InjectedLib{
				PayloadID: entry.payloadID,
				LibName:   name,
				SubDir:    subDir,
			})
		}
		result = append(result, shadowedLibs...)
	}

	if len(result) == 0 {
		return result, nil
	}

	// docker cp into the executor container
	cpCmd := exec.Command("docker", "cp",
		tmpDir+"/.",
		fmt.Sprintf("%s:/home/loading/lib", containerID))
	cpCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if out, err := cpCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("injecting libraries into %s: %w\n%s", shortContainerID(containerID), err, out)
	}

	return result, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	// preserve modification timestamp so conflict resolution is stable on re-injection
	return os.Chtimes(dst, info.ModTime(), info.ModTime())
}

func shortContainerID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
