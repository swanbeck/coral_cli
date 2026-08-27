package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/spf13/cobra"

	"coral_cli/internal/logging"
	"coral_cli/internal/ociarchive"
	"coral_cli/internal/runtime"
)

var (
	loadName    string
	loadTimeout time.Duration
)

func init() {
	loadCmd.Flags().StringVarP(&loadName, "name", "n", "", "Override the target image name:tag (default: read from the org.opencontainers.image.title and .version labels)")
	loadCmd.Flags().DurationVar(&loadTimeout, "timeout", 10*time.Minute, "Give up if the container runtime has not finished loading within this long")

	loadCmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return []string{"tar"}, cobra.ShellCompDirectiveFilterFileExt
	}
}

var loadCmd = &cobra.Command{
	Use:   "load <file>",
	Short: "Load an OCI archive into the local container runtime (current platform only)",
	Long: `Load an image from an OCI archive (.tar) file.

Only the image variant matching the current platform is loaded: coral resolves it
from the archive itself and streams that one variant to the container runtime, so
nothing depends on how a particular runtime or version treats a multi-platform
archive. The target image name is read from the org.opencontainers.image.title and
org.opencontainers.image.version labels embedded in the archive. Use --name to
override, or if those labels are absent.

The image is verified to be present under that name before the command reports
success.`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return load(args[0], loadName, loadTimeout)
	},
}

func load(filePath, nameOverride string, timeout time.Duration) error {
	if err := runtime.Check(); err != nil {
		return err
	}
	if _, err := os.Stat(filePath); err != nil {
		return fmt.Errorf("file not found: %s", filePath)
	}

	archive, err := ociarchive.OpenIndexed(filePath)
	if err != nil {
		return err
	}
	img, err := archive.SelectHostPlatform()
	if err != nil {
		return err
	}

	targetName := nameOverride
	if targetName == "" {
		if targetName, err = img.NameFromLabels(); err != nil {
			return err
		}
	}

	rt := runtime.Current.Binary
	fmt.Println(logging.Info(fmt.Sprintf("Loading %s as %s (%s)", filePath, targetName, img.Platform)))

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	child := exec.CommandContext(ctx, rt, "load")
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	stdin, err := child.StdinPipe()
	if err != nil {
		return fmt.Errorf("preparing %s load: %w", rt, err)
	}
	if err := child.Start(); err != nil {
		return fmt.Errorf("starting %s load: %w", rt, err)
	}

	streamErr := make(chan error, 1)
	go func() {
		err := archive.StreamSinglePlatform(stdin, img, targetName)
		stdin.Close()
		streamErr <- err
	}()

	waitErr := child.Wait()
	if err := <-streamErr; err != nil && waitErr == nil {
		return err
	}
	if waitErr != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("%s load did not finish within %s", rt, timeout)
		}
		return fmt.Errorf("%s load: %w", rt, waitErr)
	}

	if err := verifyPresent(rt, targetName, img); err != nil {
		return err
	}

	fmt.Println(logging.Success(fmt.Sprintf("Loaded %s (%s)", targetName, img.Platform)))
	return nil
}

func verifyPresent(rt, name string, img ociarchive.Image) error {
	inspect := func() error {
		cmd := exec.Command(rt, "image", "inspect", name)
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		return cmd.Run()
	}

	err := inspect()
	if err == nil {
		return nil
	}

	for _, digest := range []string{img.ConfigDigest, img.ManifestDigest} {
		tag := exec.Command(rt, "tag", digest, name)
		tag.Stdout = io.Discard
		tag.Stderr = io.Discard
		if tag.Run() != nil {
			continue
		}
		if inspect() == nil {
			return nil
		}
	}

	return fmt.Errorf("%s reported the archive loaded, but does not hold an image named %s: %w", rt, name, err)
}
