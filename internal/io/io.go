package io
import (
	"fmt"
	"os"
)

func ResolveComposeFile(userPath string) (string, error) {
	if userPath != "" {
		return userPath, nil
	}
	candidates := []string{"docker-compose.yaml", "compose.yaml", "docker-compose.yml", "compose.yml"}
	for _, f := range candidates {
		if _, err := os.Stat(f); err == nil {
			return f, nil
		}
	}
	return "", fmt.Errorf("no compose file found (tried: docker-compose.yaml, compose.yaml, docker-compose.yml, compose.yml)")
}

func ResolveEnvFile(userPath string) (string, error) {
	if userPath != "" {
		return userPath, nil
	}
	if _, err := os.Stat(".env"); err == nil {
		return ".env", nil
	}
	// just returning empty string if no .env file
	return "", nil
}
