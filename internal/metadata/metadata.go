package metadata

import (
	"fmt"
	"os"
	"encoding/json"
	"path/filepath"
)

type InstanceMetadata struct {
	Name        string `json:"name"`
	ComposeFile string `json:"compose_file"`
	CreatedAt   string `json:"created_at"`
	ConfigPath  string `json:"config_path"`
	Handle      string `json:"handle,omitempty"`
	DeviceID    string `json:"device_id,omitempty"`
}

func LoadInstanceMetadata(instanceName string) (*InstanceMetadata, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, "", fmt.Errorf("could not determine user home directory: %w", err)
	}

	metaPath := filepath.Join(home, ".darwin_cli", "instances", instanceName + ".json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, "", fmt.Errorf("could not read metadata file: %w", err)
	}

	var meta InstanceMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, "", fmt.Errorf("could not unmarshal metadata: %w", err)
	}

	return &meta, metaPath, nil
}
