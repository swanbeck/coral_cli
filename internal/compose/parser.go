package compose

import (
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

type ComposeFile struct {
	Services map[string]map[string]interface{} `yaml:"services"`
	CommonConfigs map[string]map[string]interface{} `yaml:",inline"`
}

func (cf *ComposeFile) ToMap() (map[string]interface{}, error) {
	data, err := yaml.Marshal(cf)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func ParseCompose(path string, env map[string]string) (*ComposeFile, error) {
	// read the file into raw data
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read compose file: %w", err)
	}

	// perform environment variable substitution
	content := expandEnv(string(raw), env)

	// unmarshal the content into a ComposeFile struct
	var cf ComposeFile
	err = yaml.Unmarshal([]byte(content), &cf)
	if err != nil {
		return nil, fmt.Errorf("unmarshal compose yaml: %w", err)
	}

	// merge the common configs into the services
	for name, service := range cf.Services {
		fmt.Printf("Processing service: %s\n", name)
		if commonConfigName, exists := service["<<"]; exists {
			// get the name of the common config being referenced
			configName := commonConfigName.(string)

			// check if the common config exists in the CommonConfigs map
			if commonConfig, exists := cf.CommonConfigs[configName]; exists {
				// merge the common config with the service
				mergeServiceConfig(service, commonConfig)
			} else {
				return nil, fmt.Errorf("common config '%s' not found", configName)
			}
		}
	}

	return &cf, nil
}

func mergeServiceConfig(service map[string]interface{}, commonConfig map[string]interface{}) {
	// iterate over each key in the common config and merge it into the service
	for key, value := range commonConfig {
		if serviceValue, exists := service[key]; exists {
			// if the key exists in both, append the values
			// assuming both values are slices (this may need to be adjusted), definitely won't work for duplicated environment variables and maybe some other cases
			switch v := serviceValue.(type) {
			case []interface{}:
				if vv, ok := value.([]interface{}); ok {
					service[key] = append(v, vv...)
				}
			}
		} else {
			// if the key does not exist in the service add it
			service[key] = value
		}
	}
}

func expandEnv(s string, env map[string]string) string {
	// replace ${VAR}
	re := regexp.MustCompile(`\$\{([^}]+)\}`)
	s = re.ReplaceAllStringFunc(s, func(sub string) string {
		key := re.FindStringSubmatch(sub)[1]
		if val, ok := env[key]; ok {
			return val
		}
		return sub
	})

	// replace $VAR
	re2 := regexp.MustCompile(`\$(\w+)`)
	s = re2.ReplaceAllStringFunc(s, func(sub string) string {
		key := re2.FindStringSubmatch(sub)[1]
		if val, ok := env[key]; ok {
			return val
		}
		return sub
	})

	return s
}
