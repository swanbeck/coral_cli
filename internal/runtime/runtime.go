package runtime

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// holds the container runtime configuration resolved from CORAL_RUNTIME
type Runtime struct {
	Binary          string // "docker" or "podman"
	DaemonTransport string // skopeo transport for local storage: "docker-daemon:" or "containers-storage:"
}

// resolved runtime for the process
var Current *Runtime

func init() {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CORAL_RUNTIME"))) {
	case "podman":
		Current = &Runtime{Binary: "podman", DaemonTransport: "containers-storage:"}
	default:
		Current = &Runtime{Binary: "docker", DaemonTransport: "docker-daemon:"}
	}
}

// verifies CORAL_RUNTIME is a supported value and the binary is in PATH
func Check() error {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("CORAL_RUNTIME")))
	if v != "" && v != "docker" && v != "podman" {
		return fmt.Errorf("unsupported CORAL_RUNTIME=%q: must be \"docker\" or \"podman\"", os.Getenv("CORAL_RUNTIME"))
	}
	if _, err := exec.LookPath(Current.Binary); err != nil {
		return fmt.Errorf("%s not found in PATH; install %s or set CORAL_RUNTIME=docker|podman", Current.Binary, Current.Binary)
	}
	return nil
}
