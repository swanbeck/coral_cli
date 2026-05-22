package cmd

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"coral_cli/internal/inspect"
	"coral_cli/internal/logging"
)

var (
	inspectFormat string
	inspectOutput string
)

func init() {
	inspectCmd.Flags().StringVar(&inspectFormat, "format", "markdown", "Output format: json or markdown")
	inspectCmd.Flags().StringVarP(&inspectOutput, "output", "o", "", "Write output to a file instead of stdout")

	inspectCmd.RegisterFlagCompletionFunc("format", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		options := []string{"json", "markdown"}
		var matches []string
		for _, o := range options {
			if strings.HasPrefix(o, toComplete) {
				matches = append(matches, o)
			}
		}
		return matches, cobra.ShellCompDirectiveNoFileComp
	})

	inspectCmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		out, err := exec.Command("docker", "images", "--format", "{{.Repository}}:{{.Tag}}").Output()
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		var matches []string
		for _, image := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if strings.HasPrefix(image, toComplete) {
				matches = append(matches, image)
			}
		}
		return matches, cobra.ShellCompDirectiveNoFileComp
	}
}

var inspectCmd = &cobra.Command{
	Use:   "inspect <image>",
	Short: "Displays the behavior manifest exported by a component image",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if inspectFormat != "json" && inspectFormat != "markdown" {
			return fmt.Errorf("--format must be json or markdown, got %q", inspectFormat)
		}
		return inspectImage(args[0], inspectFormat, inspectOutput)
	},
}

func inspectImage(image, format, outputFile string) error {
	fmt.Println(logging.Info(fmt.Sprintf("Inspecting %s...", logging.BoldMagenta(image))))

	runCmd := exec.Command("docker", "run", "--rm", "--entrypoint", "/ros_entrypoint.sh", image,
		"bash", "-c", "LD_LIBRARY_PATH=${CORAL_EXPORT_LIB}/interfaces:${LD_LIBRARY_PATH} coral-inspect")
	runCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	runCmd.Stderr = os.Stderr

	var stdout bytes.Buffer
	runCmd.Stdout = &stdout

	if err := runCmd.Run(); err != nil {
		return fmt.Errorf("running coral-inspect on %s: %w", image, err)
	}

	behaviors, err := inspect.ParseBehaviors(stdout.Bytes())
	if err != nil {
		return fmt.Errorf("parsing output from %s: %w", image, err)
	}

	var out []byte
	switch format {
	case "json":
		out, err = inspect.FormatJSON(image, behaviors)
		if err != nil {
			return fmt.Errorf("formatting JSON: %w", err)
		}
		out = append(out, '\n')
	case "markdown":
		out = []byte(inspect.FormatMarkdown(image, behaviors))
	}

	if outputFile != "" {
		if err := os.WriteFile(outputFile, out, 0644); err != nil {
			return fmt.Errorf("writing to %s: %w", outputFile, err)
		}
		fmt.Println(logging.Success(fmt.Sprintf("Written to %s", outputFile)))
		return nil
	}

	_, err = os.Stdout.Write(out)
	return err
}
