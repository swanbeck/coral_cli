package health

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"coral_cli/internal/logging"
	"coral_cli/internal/registry"
	"coral_cli/internal/runtime"
)

type Status int

const (
	StatusHealthy   Status = 0 // all containers ready
	StatusStarting  Status = 1 // at least one container still initializing; none failed
	StatusUnhealthy Status = 2 // at least one container failed or exited unexpectedly
)

// returns the health of all containers for a single compose project
func InstanceHealth(instanceName string) Status {
	ids, err := GetContainerIDsForProject(instanceName)
	if err != nil || len(ids) == 0 {
		return StatusUnhealthy
	}
	return assessContainers(ids)
}

// returns the aggregate health across all named compose instances (result is worst status among the instances)
func GroupHealth(instanceNames []string) Status {
	if len(instanceNames) == 0 {
		return StatusUnhealthy
	}
	worst := StatusHealthy
	for _, name := range instanceNames {
		s := InstanceHealth(name)
		if s > worst {
			worst = s
		}
		if worst == StatusUnhealthy {
			return StatusUnhealthy
		}
	}
	return worst
}

// returns the aggregate health of the given container IDs
func assessContainers(ids []string) Status {
	worst := StatusHealthy
	for _, id := range ids {
		cs := containerStatus(id)
		switch {
		case isReady(cs):
			// healthy, running_no_healthcheck, or transient-exited-0
		case cs.status == "starting":
			if StatusStarting > worst {
				worst = StatusStarting
			}
		default:
			return StatusUnhealthy
		}
	}
	return worst
}

type EventType int

const (
	EventContainerUnhealthy EventType = iota
	EventContainerExited
	// fires when a payload that contributed libraries to an executor is no longer running its skillset/driver backend; the executor itself is unaffected(libraries are already inside the container), but the behaviors may fail at runtime
	EventLibraryDegraded
)

// emitted by the Monitor when a container or library dependency degrades
type HealthEvent struct {
	Type        EventType
	ContainerID string
	ServiceName string
	PayloadID   string // set for EventLibraryDegraded
	Detail      string
}

const pollInterval = 5 * time.Second

// polls Docker container health for a compose instance and emits HealthEvents when containers become unhealthy or library backends are lost
type Monitor struct {
	instanceName string
	reg          *registry.Registry
}

func NewMonitor(instanceName string, reg *registry.Registry) *Monitor {
	return &Monitor{instanceName: instanceName, reg: reg}
}

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
		cs := containerStatus(id)
		switch cs.status {
		case "unhealthy":
			flagged[id] = true
			events <- HealthEvent{Type: EventContainerUnhealthy, ContainerID: id, ServiceName: cs.serviceName, Detail: "health check failing"}
			fmt.Println(logging.Warning(fmt.Sprintf("Container %s (%s) is unhealthy", shortID(id), cs.serviceName)))
		case "exited", "dead":
			flagged[id] = true
			if cs.transient && cs.exitCode == 0 {
				continue
			}
			events <- HealthEvent{Type: EventContainerExited, ContainerID: id, ServiceName: cs.serviceName, Detail: "container exited"}
			fmt.Println(logging.Warning(fmt.Sprintf("Container %s (%s) has exited unexpectedly", shortID(id), cs.serviceName)))
			// check whether any executor injections depended on this container's payload
			m.checkLibraryDegradation(id, cs.serviceName, events)
		}
	}
}

func (m *Monitor) checkLibraryDegradation(deadContainerID, svcName string, events chan<- HealthEvent) {
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

// blocks until all containers for the given services are healthy (or running without a health check), or until timeout expires; a cancelled context returns immediately
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
			cs := containerStatus(id)
			if (cs.status == "exited" || cs.status == "dead") && !isReady(cs) {
				return fmt.Errorf("service %s exited unexpectedly (exit code %d)", svc, cs.exitCode)
			}
			if !isReady(cs) {
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

type containerState struct {
	status      string
	serviceName string
	exitCode    int
	transient   bool
}

func isReady(cs containerState) bool {
	switch cs.status {
	case "healthy", "running_no_healthcheck":
		return true
	case "exited", "dead":
		return cs.transient && cs.exitCode == 0
	}
	return false
}

// returns normalised state for a container, including exit code and whether it bears the coral.transient label
func containerStatus(containerID string) containerState {
	cmd := exec.Command(runtime.Current.Binary, "inspect",
		"--format", `{{with .State.Health}}{{if .Status}}{{.Status}}{{else}}none{{end}}{{else}}none{{end}} {{.State.Status}} {{index .Config.Labels "com.docker.compose.service"}} {{.State.ExitCode}} {{index .Config.Labels "coral.transient"}}`,
		containerID)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.Output()
	if err != nil {
		return containerState{status: "unknown"}
	}
	parts := strings.Fields(strings.TrimSpace(string(out)))
	var health, state, svc string
	var exitCode int
	var transient bool
	if len(parts) > 0 {
		health = parts[0]
	}
	if len(parts) > 1 {
		state = parts[1]
	}
	if len(parts) > 2 {
		svc = parts[2]
	}
	if len(parts) > 3 {
		if n, err := strconv.Atoi(parts[3]); err == nil {
			exitCode = n
		}
	}
	if len(parts) > 4 {
		transient = parts[4] == "true"
	}
	var status string
	if health == "" || health == "none" {
		if state == "running" {
			status = "running_no_healthcheck"
		} else {
			status = state
		}
	} else {
		status = health
	}
	return containerState{status: status, serviceName: svc, exitCode: exitCode, transient: transient}
}

func GetContainerIDForService(instanceName, serviceName string) (string, error) {
	args := []string{"ps", "-a",
		"--filter", fmt.Sprintf("label=com.docker.compose.project=%s", instanceName),
		"--filter", fmt.Sprintf("label=com.docker.compose.service=%s", serviceName),
	}
	if runtime.Current.Binary == "docker" {
		args = append(args, "--filter", "label=com.docker.compose.oneoff=False")
	}
	args = append(args, "-q")
	cmd := exec.Command(runtime.Current.Binary, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	// ps -a can return multiple IDs (e.g. stopped containers from a prior run); use the first
	id, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(id), nil
}

func GetContainerIDsForProject(instanceName string) ([]string, error) {
	args := []string{"ps", "-a",
		"--filter", fmt.Sprintf("label=com.docker.compose.project=%s", instanceName),
	}
	if runtime.Current.Binary == "docker" {
		args = append(args, "--filter", "label=com.docker.compose.oneoff=False")
	}
	args = append(args, "-q")
	cmd := exec.Command(runtime.Current.Binary, args...)
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
