package cmd

import (
	"bufio"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

var imagesCmd = &cobra.Command{
	Use:   "images",
	Short: "List only darwin-prefixed Docker images",
	Run: func(cmd *cobra.Command, args []string) {
		showDarwinImages(args)
	},
}

func showDarwinImages(args []string) {
	allArgs := append([]string{"images"}, args...)
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
		if first || strings.HasPrefix(line, "darwin") {
			// keep the header and lines starting with "darwin"
			println(line)
		}
		first = false
	}

	cmd.Wait()
}
