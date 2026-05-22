package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"coral_cli/internal/inspect"
	"coral_cli/internal/libs"
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

	allLabels, err := libs.GetImageLabels(image)
	if err != nil {
		return fmt.Errorf("reading image labels for %s: %w", image, err)
	}

	coralVersion, ok := allLabels["coral.version"]
	if !ok || coralVersion == "" {
		return fmt.Errorf("image %s does not set coral.version", image)
	}
	if !coralVersionAtLeast(coralVersion, 2, 1, 1) {
		fmt.Println(logging.Warning(fmt.Sprintf(
			"image coral.version %s predates v2.1.1; some inspection features may be unavailable", coralVersion)))
	}

	meta := inspect.ImageMetadata{
		OCI:    make(map[string]string),
		Labels: make(map[string]string),
	}
	const ociPrefix = "org.opencontainers.image."
	for k, v := range allLabels {
		switch {
		case strings.HasPrefix(k, ociPrefix):
			meta.OCI[strings.TrimPrefix(k, ociPrefix)] = v
		case strings.HasPrefix(k, "coral."):
			meta.Labels[k] = v
		}
	}

	behaviors := extractBehaviors(image)

	var out []byte
	switch format {
	case "json":
		out, err = inspect.FormatJSON(image, meta, behaviors)
		if err != nil {
			return fmt.Errorf("formatting JSON: %w", err)
		}
		out = append(out, '\n')
	case "markdown":
		out = []byte(inspect.FormatMarkdown(image, meta, behaviors))
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

// extractBehaviors runs coral-inspect inside the image and returns the parsed behavior map
func extractBehaviors(image string) map[string]json.RawMessage {
	runCmd := exec.Command("docker", "run", "--rm", "--entrypoint", "/ros_entrypoint.sh", image,
		"bash", "-c", "LD_LIBRARY_PATH=${CORAL_EXPORT_LIB}/interfaces:${LD_LIBRARY_PATH} coral-inspect")
	runCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	runCmd.Stderr = os.Stderr

	var stdout bytes.Buffer
	runCmd.Stdout = &stdout

	if err := runCmd.Run(); err != nil {
		fmt.Println(logging.Warning(fmt.Sprintf("behavior extraction unavailable for this image: %v", err)))
		return map[string]json.RawMessage{}
	}

	behaviors, err := inspect.ParseBehaviors(stdout.Bytes())
	if err != nil {
		fmt.Println(logging.Warning(fmt.Sprintf("could not parse behavior output: %v", err)))
		return map[string]json.RawMessage{}
	}

	return behaviors
}

// coralVersionAtLeast reports whether v (e.g. "v2.1.1" or "2.1.1") is >= major.minor.patch.
func coralVersionAtLeast(v string, major, minor, patch int) bool {
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 3 {
		return false
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return false
		}
		nums[i] = n
	}
	if nums[0] != major {
		return nums[0] > major
	}
	if nums[1] != minor {
		return nums[1] > minor
	}
	return nums[2] >= patch
}
