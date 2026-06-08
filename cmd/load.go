package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/spf13/cobra"

	"coral_cli/internal/logging"
	"coral_cli/internal/util"
)

var loadName string

func init() {
	loadCmd.Flags().StringVarP(&loadName, "name", "n", "", "Override the target image name:tag (default: read from org.containers.image.title label)")

	loadCmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return nil, cobra.ShellCompDirectiveDefault
	}
}

var loadCmd = &cobra.Command{
	Use:   "load <file>",
	Short: "Load an OCI archive into the local Docker daemon (current platform only)",
	Long: `Load a Docker image from an OCI archive (.tar) file using skopeo.

Only the image variant matching the current platform is loaded. The target
image name is read from the org.containers.image.title label embedded in the
archive. Use --name to override if the label is absent.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return load(args[0], loadName)
	},
}

func load(filePath, nameOverride string) error {
	if err := util.CheckSkopeo(); err != nil {
		return err
	}

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return fmt.Errorf("file not found: %s", filePath)
	}

	targetName := nameOverride
	if targetName == "" {
		title, err := util.GetTitleLabel(filePath)
		if err != nil {
			return fmt.Errorf("inspecting archive: %w", err)
		}
		targetName = title
	}

	fmt.Println(logging.Info(fmt.Sprintf("Loading %s as %s (current platform)", filePath, targetName)))

	skopeoCmd := exec.Command("skopeo", "copy", "oci-archive:"+filePath, "docker-daemon:"+targetName)
	skopeoCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	skopeoCmd.Stdout = os.Stdout
	skopeoCmd.Stderr = os.Stderr
	if err := skopeoCmd.Run(); err != nil {
		return fmt.Errorf("skopeo copy: %w", err)
	}

	fmt.Println(logging.Success(fmt.Sprintf("Loaded %s", targetName)))
	return nil
}
