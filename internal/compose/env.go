package compose

import (
	"bufio"
	"os"
	"strings"
)

// loads a .env file and merges with system environment variables
func LoadEnvFile(envFile string) (map[string]string, error) {
	env := map[string]string{}

	if envFile != "" {
		file, err := os.Open(envFile)
		if err != nil {
			return nil, err
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			key, value, success := parseEnvLine(scanner.Text())
			if success {
				env[key] = value
			}
		}
	}

	return env, nil
}

func parseEnvLine(line string) (string, string, bool) {
	line = strings.TrimSpace(line)
	// remove commented lines and empty lines
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}

	// strip inline comments
	if idx := strings.Index(line, "#"); idx != -1 {
		line = strings.TrimSpace(line[:idx])
	}

	// split key and value
	parts := strings.SplitN(line, "=", 2)
	if len(parts) != 2 {
		return "", "", false
	}

	key := strings.TrimSpace(parts[0])
	value := strings.TrimSpace(parts[1])
	return key, value, true
}
