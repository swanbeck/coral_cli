package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"coral_cli/internal/logging"
	"coral_cli/internal/runtime"
	"coral_cli/internal/util"
)

var (
	saveOutput   string
	saveRegistry bool
)

func init() {
	saveCmd.Flags().StringVarP(&saveOutput, "output", "o", "", "Output file path (default: derived from image name and tag)")
	saveCmd.Flags().BoolVarP(&saveRegistry, "registry", "r", false, "Pull from registry instead of local docker daemon (required for true multi-arch images built with docker buildx --push)")

	saveCmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		out, err := exec.Command(runtime.Current.Binary, "images", "--format", "{{.Repository}}:{{.Tag}}").Output()
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

var saveCmd = &cobra.Command{
	Use:   "save <image>[:<tag>]",
	Short: "Save a multi-arch image to an OCI archive (.tar) file",
	Long: `Save a Docker image to an OCI archive (.tar) file using skopeo.

Unlike docker save, this preserves all architectures in a multi-arch manifest.
The resulting archive can be loaded on any supported platform with coral load.

By default the image is read from the local Docker daemon. Use --registry to
pull directly from a registry (required for images built with docker buildx --push
that have not been pulled locally).`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return save(args[0], saveOutput, saveRegistry)
	},
}

func save(image, output string, fromRegistry bool) error {
	if err := util.CheckSkopeo(); err != nil {
		return err
	}

	if output == "" {
		output = util.ImageToFilename(image)
	}

	var source string
	if fromRegistry {
		source = "docker://" + image
	} else {
		source = runtime.Current.DaemonTransport + image
	}

	fmt.Println(logging.Info(fmt.Sprintf("Saving %s → %s", image, output)))

	var saveCmd *exec.Cmd
	if runtime.Current.Binary == "podman" {
		// containers-storage: transport calls unshare internally, which fails when skopeo is run outside Podman's user namespace
		saveCmd = exec.Command("podman", "unshare", "--", "skopeo", "copy", "--all", source, "oci-archive:"+output)
	} else {
		saveCmd = exec.Command("skopeo", "copy", "--all", source, "oci-archive:"+output)
	}
	saveCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	saveCmd.Stdout = os.Stdout
	saveCmd.Stderr = os.Stderr
	if err := saveCmd.Run(); err != nil {
		return fmt.Errorf("skopeo copy: %w", err)
	}

	fmt.Println(logging.Success(fmt.Sprintf("Saved to %s", output)))
	return nil
}
