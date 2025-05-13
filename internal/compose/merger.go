package compose

import (
	// "fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type RawCompose map[string]interface{}

// MergeServiceConfigs merges base and overlay service definitions
func MergeServiceConfigs(base, overlay map[string]interface{}) map[string]interface{} {
	for k, v := range overlay {
		if bv, ok := base[k]; ok {
			switch bvTyped := bv.(type) {
			case []interface{}:
				// Merge lists (e.g., volumes, environment)
				if ov, ok := v.([]interface{}); ok {
					base[k] = append(bvTyped, ov...)
				}
			case map[string]interface{}:
				// Merge maps recursively
				if ov, ok := v.(map[string]interface{}); ok {
					base[k] = MergeServiceConfigs(bvTyped, ov)
				}
			default:
				// Replace anything else
				base[k] = v
			}
		} else {
			base[k] = v
		}
	}
	return base
}

// LoadRawYAML loads a raw compose file into map[string]interface{}
func LoadRawYAML(path string) (RawCompose, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	return RawCompose(raw), nil
}

// SaveRawYAML saves the final merged YAML to disk
func SaveRawYAML(path string, content RawCompose) error {
	data, err := yaml.Marshal(content)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
