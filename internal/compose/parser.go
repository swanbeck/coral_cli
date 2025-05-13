package compose

import (
	"fmt"
	"io/ioutil"
	"regexp"

	"gopkg.in/yaml.v3"
)

type ComposeFile struct {
	Services map[string]Service `yaml:"services"`
}

type Service struct {
	Image string `yaml:"image"`
	// add more fields as needed
}

func ParseCompose(path string, env map[string]string) (*ComposeFile, error) {
	raw, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read compose file: %w", err)
	}

	content := expandEnv(string(raw), env)

	var cf ComposeFile
	err = yaml.Unmarshal([]byte(content), &cf)
	if err != nil {
		return nil, fmt.Errorf("unmarshal compose yaml: %w", err)
	}

	return &cf, nil
}

// expand ${VAR} and $VAR in the string using env map
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
