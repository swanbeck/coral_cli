package logging

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

var colors = []string{
	"\033[31m", // red
	"\033[32m", // green
	"\033[33m", // yellow
	"\033[34m", // blue
	"\033[35m", // magenta
	"\033[36m", // cyan
}

const colorReset = "\033[0m"

func padName(name string, width int) string {
	if len(name) >= width {
		return name
	}
	return name + strings.Repeat(" ", width-len(name))
}

func streamLogs(name, color string, wg *sync.WaitGroup) {
	defer wg.Done()

	cmd := exec.Command("docker", "logs", "-f", name)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error piping stdout for %s: %v\n", name, err)
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error piping stderr for %s: %v\n", name, err)
		return
	}

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Error starting logs for %s: %v\n", name, err)
		return
	}

	printStream := func(scanner *bufio.Scanner) {
		for scanner.Scan() {
			line := scanner.Text()
			fmt.Printf("%s%s |%s %s\n", color, padName(name, 16), colorReset, line)
		}
	}

	go printStream(bufio.NewScanner(stdout))
	printStream(bufio.NewScanner(stderr))

	cmd.Wait()
}

func main() {
	services := []string{"pddl_planner", "payload_manager"} // or dynamically discover from Compose
	var wg sync.WaitGroup

	for i, service := range services {
		idBytes, err := exec.Command("docker", "compose", "ps", "-q", service).Output()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to get container ID for %s: %v\n", service, err)
			continue
		}
		containerID := strings.TrimSpace(string(idBytes))
		if containerID == "" {
			fmt.Fprintf(os.Stderr, "No container ID for %s\n", service)
			continue
		}

		// Get actual container name (Compose may append suffixes)
		nameBytes, err := exec.Command("docker", "inspect", "-f", "{{.Name}}", containerID).Output()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to inspect %s: %v\n", containerID, err)
			continue
		}
		containerName := strings.Trim(strings.TrimSpace(string(nameBytes)), "/")
		color := colors[i%len(colors)]

		wg.Add(1)
		go streamLogs(containerName, color, &wg)
	}

	wg.Wait()
	fmt.Println("All logs completed.")
}
