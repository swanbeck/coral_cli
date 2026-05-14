package compose

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"coral_cli/internal/device"
	"coral_cli/internal/logging"
)

type usbDeviceSpec struct {
	VendorID    string   `yaml:"vendor_id"`
	ProductID   string   `yaml:"product_id"`
	Description string   `yaml:"description"`
	Subsystems  []string `yaml:"subsystems"`
}

type DevicesFile struct {
	USB []usbDeviceSpec `yaml:"usb"`
}

func LoadDevicesFile(path string) (*DevicesFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var df DevicesFile
	if err := yaml.Unmarshal(data, &df); err != nil {
		return nil, fmt.Errorf("parsing devices.yaml: %w", err)
	}
	return &df, nil
}

// ResolveDevicePaths resolves a DevicesFile to concrete host device paths for
// Docker Compose's devices: list. Each returned entry is "<path>:<path>".
// Returns an error if any declared USB device is not present on the host.
func ResolveDevicePaths(df *DevicesFile, serviceName string) ([]string, error) {
	var paths []string

	for _, spec := range df.USB {
		matches, err := device.FindByVIDPID(spec.VendorID, spec.ProductID, spec.Subsystems)
		if err != nil {
			return nil, fmt.Errorf("discovering USB %s:%s (%s): %w",
				spec.VendorID, spec.ProductID, spec.Description, err)
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("service %s requires USB device %s:%s (%s) but it was not found on this host",
				serviceName, spec.VendorID, spec.ProductID, spec.Description)
		}
		if len(matches) > 1 {
			fmt.Println(logging.Warning(fmt.Sprintf(
				"%s: %d devices match USB %s:%s (%s) — mapping all",
				serviceName, len(matches), spec.VendorID, spec.ProductID, spec.Description)))
		}
		for _, group := range matches {
			for _, d := range group {
				paths = append(paths, d.DevPath+":"+d.DevPath)
			}
		}
	}

	return paths, nil
}
