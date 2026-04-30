package health

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"coral_cli/internal/logging"
	"coral_cli/internal/registry"
)

type EventType int

const (
	EventContainerUnhealthy EventType = iota
	EventContainerExited
	// EventLibraryDegraded fires when a payload that contributed libraries to an executor
	// is no longer running its skillset/driver backend. The executor itself is unaffected
	// (libraries are already inside the container), but the behaviors may fail at runtime.
	EventLibraryDegraded
)

// HealthEvent is emitted by the Monitor when a container or library dependency degrades.
type HealthEvent struct {
	Type        EventType
	ContainerID string
	ServiceName string
	PayloadID   string // set for EventLibraryDegraded
	Detail      string
}

const pollInterval = 5 * time.Second

// Monitor polls Docker container health for a compose instance and emits HealthEvents
// when containers become unhealthy or library backends are lost.
type Monitor struct {
	instanceName string
	reg          *registry.Registry
}

func NewMonitor(instanceName string, reg *registry.Registry) *Monitor {
	return &Monitor{instanceName: instanceName, reg: reg}
}

// Start launches the polling goroutine and returns a channel of health events.
// The channel is closed when ctx is cancelled.
func (m *Monitor) Start(ctx context.Context) <-chan HealthEvent {
	events := make(chan HealthEvent, 16)
	go func() {
		defer close(events)
		flagged := make(map[string]bool)
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.poll(events, flagged)
			}
		}
	}()
	return events
}

func (m *Monitor) poll(events chan<- HealthEvent, flagged map[string]bool) {
	ids, err := GetContainerIDsForProject(m.instanceName)
	if err != nil || len(ids) == 0 {
		return
	}
	for _, id := range ids {
		if flagged[id] {
			continue
		}
		status, svcName := containerStatus(id)
		switch status {
		case "unhealthy":
			flagged[id] = true
			events <- HealthEvent{Type: EventContainerUnhealthy, ContainerID: id, ServiceName: svcName, Detail: "health check failing"}
			fmt.Println(logging.Warning(fmt.Sprintf("Container %s (%s) is unhealthy", shortID(id), svcName)))
		case "exited", "dead":
			flagged[id] = true
			events <- HealthEvent{Type: EventContainerExited, ContainerID: id, ServiceName: svcName, Detail: "container exited"}
			fmt.Println(logging.Warning(fmt.Sprintf("Container %s (%s) has exited unexpectedly", shortID(id), svcName)))
			// Check whether any executor injections depended on this container's payload.
			m.checkLibraryDegradation(id, svcName, events)
		}
	}
}

func (m *Monitor) checkLibraryDegradation(deadContainerID, svcName string, events chan<- HealthEvent) {
	// Use the service name as a proxy for payload ID (Phase 1; Phase 5 will use crex payload IDs).
	affected := m.reg.GetExecutorsForPayload(svcName)
	for _, rec := range affected {
		if rec.ContainerID == deadContainerID {
			continue
		}
		events <- HealthEvent{
			Type:        EventLibraryDegraded,
			ContainerID: rec.ContainerID,
			ServiceName: svcName,
			PayloadID:   svcName,
			Detail:      fmt.Sprintf("backend service %s exited; injected libraries may fail at runtime", svcName),
		}
		fmt.Println(logging.Warning(fmt.Sprintf(
			"Executor %s has libraries from %s which has exited — behaviors may fail at runtime",
			shortID(rec.ContainerID), svcName,
		)))
	}
}

// WaitForHealthy blocks until all containers for the given services are healthy (or
// running without a health check), or until timeout expires. A cancelled context
// returns immediately.
func WaitForHealthy(ctx context.Context, instanceName string, services []string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		allReady := true
		for _, svc := range services {
			id, err := GetContainerIDForService(instanceName, svc)
			if err != nil || id == "" {
				allReady = false
				break
			}
			status, _ := containerStatus(id)
			if !isReady(status) {
				allReady = false
				break
			}
		}
		if allReady {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("timed out waiting for services to become healthy: %v", services)
}

func isReady(status string) bool {
	return status == "healthy" || status == "running_no_healthcheck"
}

// containerStatus returns a normalised status and the compose service name.
func containerStatus(containerID string) (status, serviceName string) {
	// Use {{if .State.Health}} to emit a single token "none" instead of the
	// two-token "<no value>" that the plain .State.Health.Status template
	// produces for containers without a healthcheck. A multi-token output
	// shifts all subsequent fields and causes isReady to never return true.
	cmd := exec.Command("docker", "inspect",
		"--format", `{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}} {{.State.Status}} {{index .Config.Labels "com.docker.compose.service"}}`,
		containerID)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.Output()
	if err != nil {
		return "unknown", ""
	}
	parts := strings.Fields(strings.TrimSpace(string(out)))
	var health, state, svc string
	if len(parts) > 0 {
		health = parts[0]
	}
	if len(parts) > 1 {
		state = parts[1]
	}
	if len(parts) > 2 {
		svc = parts[2]
	}
	if health == "" || health == "none" {
		if state == "running" {
			return "running_no_healthcheck", svc
		}
		return state, svc
	}
	return health, svc
}

// GetContainerIDForService returns the container ID for a named service in a compose project.
func GetContainerIDForService(instanceName, serviceName string) (string, error) {
	cmd := exec.Command("docker", "ps", "-a",
		"--filter", fmt.Sprintf("label=com.docker.compose.project=%s", instanceName),
		"--filter", fmt.Sprintf("label=com.docker.compose.service=%s", serviceName),
		"--filter", "label=com.docker.compose.oneoff=False",
		"-q")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// GetContainerIDsForProject returns all container IDs for a compose project.
func GetContainerIDsForProject(instanceName string) ([]string, error) {
	cmd := exec.Command("docker", "ps", "-a",
		"--filter", fmt.Sprintf("label=com.docker.compose.project=%s", instanceName),
		"--filter", "label=com.docker.compose.oneoff=False",
		"-q")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			ids = append(ids, line)
		}
	}
	return ids, nil
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
