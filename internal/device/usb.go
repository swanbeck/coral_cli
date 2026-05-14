package device

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var mediaRe = regexp.MustCompile(`^media\d+$`)

const usbDevicesRoot = "/sys/bus/usb/devices"

type ResolvedDevice struct {
	DevPath   string // e.g. /dev/video49
	Subsystem string // e.g. video4linux, media, usb
}

// walks /sys/bus/usb/devices/ and returns one device group per physical USB device matching vendorID:productID; each group contains all associated /dev nodes; if subsystems is non-empty, only nodes whose subsystem is in the list are included; otherwise all are included
func FindByVIDPID(vendorID, productID string, subsystems []string) ([][]ResolvedDevice, error) {
	entries, err := os.ReadDir(usbDevicesRoot)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", usbDevicesRoot, err)
	}

	filter := make(map[string]bool, len(subsystems))
	for _, s := range subsystems {
		filter[s] = true
	}

	var results [][]ResolvedDevice

	for _, e := range entries {
		// entries with ":" are USB interface descriptors, not device nodes
		if strings.Contains(e.Name(), ":") {
			continue
		}
		devDir := filepath.Join(usbDevicesRoot, e.Name())

		vid, err := readTrimmed(filepath.Join(devDir, "idVendor"))
		if err != nil || !strings.EqualFold(vid, vendorID) {
			continue
		}
		pid, err := readTrimmed(filepath.Join(devDir, "idProduct"))
		if err != nil || !strings.EqualFold(pid, productID) {
			continue
		}

		realDevDir, err := filepath.EvalSymlinks(devDir)
		if err != nil {
			realDevDir = devDir
		}

		group, err := collectDevNodes(realDevDir, filter)
		if err != nil {
			return nil, fmt.Errorf("collecting devices for %s:%s at %s: %w", vendorID, productID, devDir, err)
		}
		if len(group) > 0 {
			results = append(results, group)
		}
	}

	return results, nil
}

// walks the sysfs subtree for a USB device and returns all /dev nodes found in uevent files, filtered by subsystem if filter is set
func collectDevNodes(usbDevRoot string, subsystemFilter map[string]bool) ([]ResolvedDevice, error) {
	var devs []ResolvedDevice

	err := filepath.WalkDir(usbDevRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "uevent" {
			return err
		}

		devName, err := readUeventField(path, "DEVNAME")
		if err != nil || devName == "" {
			return nil
		}

		subsystem := subsystemFromPath(path, usbDevRoot)
		if len(subsystemFilter) > 0 && !subsystemFilter[subsystem] {
			return nil
		}

		devs = append(devs, ResolvedDevice{
			DevPath:   "/dev/" + devName,
			Subsystem: subsystem,
		})
		return nil
	})

	return devs, err
}

// derives the Linux kernel subsystem name from the path of a uevent file within a USB device's sysfs subtree; the USB device's own uevent is at <root>/uevent; child device uevent files sit under directories named after their subsystem class (e.g. video4linux/video49/uevent) or, for media controller nodes, directly under a mediaX directory
func subsystemFromPath(ueventPath, usbDevRoot string) string {
	rel := strings.TrimPrefix(ueventPath, usbDevRoot+"/")
	for _, part := range strings.Split(rel, "/") {
		switch {
		case part == "video4linux":
			return "video4linux"
		case part == "sound":
			return "sound"
		case part == "input":
			return "input"
		case part == "hidraw":
			return "hidraw"
		case part == "tty":
			return "tty"
		case mediaRe.MatchString(part):
			return "media"
		}
	}
	return "usb"
}

func readUeventField(ueventPath, field string) (string, error) {
	data, err := os.ReadFile(ueventPath)
	if err != nil {
		return "", err
	}
	prefix := field + "="
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix)), nil
		}
	}
	return "", nil
}

func readTrimmed(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
