package compose

import (
	"os"

	"gopkg.in/yaml.v3"
)

type RawCompose map[string]interface{}

func MergeServiceConfigs(base, overlay map[string]interface{}) map[string]interface{} {
	for k, v := range overlay {
		if bv, ok := base[k]; ok {
			switch bvTyped := bv.(type) {
			case []interface{}:
				// merge lists
				if ov, ok := v.([]interface{}); ok {
					base[k] = append(bvTyped, ov...)
				}
			case map[string]interface{}:
				// merge maps recursively
				if ov, ok := v.(map[string]interface{}); ok {
					base[k] = MergeServiceConfigs(bvTyped, ov)
				}
			default:
				// replace anything else
				base[k] = v
			}
		} else {
			base[k] = v
		}
	}
	return base
}

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

func SaveRawYAML(path string, content RawCompose) error {
	data, err := yaml.Marshal(content)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
