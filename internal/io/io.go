package io

import (
	"fmt"
	"os"
	"strconv"
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
	return "", nil
}

func GetUID() (int, error) {
	uid := os.Getenv("CORAL_UID")
	if uid == "" {
		return os.Getuid(), nil
	}
	return strconv.Atoi(uid)
}

func GetGID() (int, error) {
	gid := os.Getenv("CORAL_GID")
	if gid == "" {
		return os.Getgid(), nil
	}
	return strconv.Atoi(gid)
}
