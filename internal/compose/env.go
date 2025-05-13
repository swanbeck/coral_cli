package compose

import (
	"bufio"
	"os"
	"strings"
)

// LoadEnv loads a .env file and merges with system environment variables
func LoadEnv(envFile string) (map[string]string, error) {
	env := map[string]string{}

	if envFile != "" {
		file, err := os.Open(envFile)
		if err != nil {
			return nil, err
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			parts := strings.SplitN(line, "=", 2)
			if len(parts) != 2 {
				continue
			}
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			env[key] = val
		}
	}

	// add system environment variables (only for variables not present in .env file)
	for _, e := range os.Environ() {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			if _, exists := env[parts[0]]; !exists {
				env[parts[0]] = parts[1]
			}
		}
	}

	return env, nil
}
