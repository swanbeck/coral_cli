package cmd

import (
	"bufio"
	"fmt"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

var psCmd = &cobra.Command{
	Use:   "ps",
	Short: "List only running containers from darwin images",
	RunE: func(cmd *cobra.Command, args []string) error {
		return showDarwinContainers(args)
	},
}

func showDarwinContainers(args []string) error {
	allArgs := append([]string{"ps"}, args...)
	cmd := exec.Command("docker", allArgs...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		panic(err)
	}

	if err := cmd.Start(); err != nil {
		panic(err)
	}

	scanner := bufio.NewScanner(stdout)
	first := true
	for scanner.Scan() {
		line := scanner.Text()

		if first {
			fmt.Println(line)
			first = false
			continue
		}

		// split line by fields (based on at least 2 spaces)
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		image := fields[1]

		if strings.HasPrefix(image, "darwin") {
			fmt.Println(line)
		}
	}

	cmd.Wait()
	return nil
}
