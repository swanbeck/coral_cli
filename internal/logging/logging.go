package logging

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"

	"github.com/fatih/color"

	"coral_cli/internal/metadata"
)

var (
	Red    = color.New(color.FgRed).SprintFunc()
	Green  = color.New(color.FgGreen).SprintFunc()
	Yellow = color.New(color.FgYellow).SprintFunc()
	Blue   = color.New(color.FgBlue).SprintFunc()
)

var (
	WhiteOnMagenta   = color.New(color.FgWhite, color.BgMagenta).SprintFunc()
	BoldMagenta      = color.New(color.FgMagenta, color.Bold).SprintFunc()
	BoldMagentaHi    = color.New(color.FgHiMagenta, color.Bold).SprintFunc()
	UnderlineMagenta = color.New(color.FgMagenta, color.Underline).SprintFunc()
)

func Info(msg string) string {
	return Blue("[INFO] ") + msg
}

func Success(msg string) string {
	return Green("[SUCCESS] ") + msg
}

func Warning(msg string) string {
	return Yellow("[WARNING] ") + msg
}

func Failure(msg string) string {
	return Red("[FAILURE] ") + msg
}

func TailLogs(containers []metadata.ContainerInfo, doneChan <-chan struct{}, tailAll bool) (<-chan struct{}, <-chan error) {
	colors := []color.Attribute{
		color.FgHiRed,
		color.FgHiGreen,
		color.FgHiYellow,
		color.FgHiBlue,
		color.FgHiMagenta,
		color.FgHiCyan,
		color.FgHiWhite,
		color.FgHiBlack,
	}
	colorMap := make(map[string]*color.Color)
	serviceCounts := make(map[string]int)
	colorIndex := 0

	printMu := &sync.Mutex{}
	errCh := make(chan error, len(containers))
	finished := make(chan struct{})

	var wg sync.WaitGroup
	for _, c := range containers {
		key := c.Service
		count := serviceCounts[key]
		if count > 0 {
			key = fmt.Sprintf("%s-%d", key, count)
		}
		serviceCounts[c.Service] = count + 1

		clr, exists := colorMap[key]
		if !exists {
			clr = color.New(colors[colorIndex%len(colors)]).Add(color.Bold)
			colorMap[key] = clr
			colorIndex++
		}

		wg.Add(1)
		go func(c metadata.ContainerInfo, clr *color.Color) {
			defer wg.Done()

			cmd := exec.Command("docker", "logs", "-f", c.ID)
			if !tailAll {
				cmd = exec.Command("docker", "logs", "-f", "--since", "0s", "--tail", "0", c.ID)
			}
			stdout, _ := cmd.StdoutPipe()
			stderr, _ := cmd.StderrPipe()

			if err := cmd.Start(); err != nil {
				errCh <- fmt.Errorf("failed to start logs for %s: %w", c.Name, err)
				return
			}

			printStream := func(r io.Reader) {
				scanner := bufio.NewScanner(r)
				for scanner.Scan() {
					line := scanner.Text()
					printMu.Lock()
					clr.Printf("%-15s | ", key)
					fmt.Println(line)
					printMu.Unlock()
				}
			}

			go printStream(stdout)
			go printStream(stderr)

			if err := cmd.Wait(); err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 130 {
					return
				}
				errCh <- fmt.Errorf("logs exited for %s: %w", c.Name, err)
			}
		}(c, clr)
	}

	go func() {
		wg.Wait()
		close(finished)
	}()

	return finished, errCh

}

func GetContainerInfo(instanceName string, composePath string) ([]metadata.ContainerInfo, error) {
	var containers []metadata.ContainerInfo

	args := []string{"compose", "-p", instanceName, "-f", composePath, "ps", "-q"}
	out, err := exec.Command("docker", args...).Output()
	if err != nil {
		return containers, fmt.Errorf("failed to get container IDs: %w", err)
	}
	containerIDs := strings.Fields(string(out))
	if len(containerIDs) == 0 {
		return containers, fmt.Errorf("no containers found for instance %s", instanceName)
	}

	// inspect containers to get service names
	prefix := instanceName + "-"
	suffixRegex := regexp.MustCompile(`-\d+$`)
	for _, id := range containerIDs {
		nameOut, err := exec.Command("docker", "inspect", "-f", "{{.Name}}", id).Output()
		if err != nil {
			return containers, fmt.Errorf("failed to inspect container %s: %w", id, err)
		}
		fullName := strings.Trim(strings.TrimSpace(string(nameOut)), "/")
		serviceName := fullName
		if strings.HasPrefix(fullName, prefix) {
			serviceName = fullName[len(prefix):]
		}
		serviceName = suffixRegex.ReplaceAllString(serviceName, "")

		containers = append(containers, metadata.ContainerInfo{
			ID:      id,
			Name:    fullName,
			Service: serviceName,
		})
	}

	return containers, nil
}
