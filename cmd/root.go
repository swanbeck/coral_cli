package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "coral",
	Short: "Coral wraps Docker for the Coral ecosystem",
	// disable cobra's built-in subcommand parsing to allow coral kill or anything like that that is not overwritten to directly call docker kill or whatever
	DisableFlagParsing: true,
	Args:               cobra.ArbitraryArgs,
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) == 0 {
			_ = cmd.Help()
			return
		}
		// we need to handle base docker commands (or commands we want to treat like docker commands but with extra pre- or post-processing) explictly so all flags are passed along to those docker commands rather than being parsed by cobra
		var err error

		switch args[0] {
		case "images":
			err = imagesCmd.RunE(cmd, args[1:])
		case "ps":
			err = psCmd.RunE(cmd, args[1:])
		default: // forward everything else that didn't come here or get parsed by cobra to docker
			runDockerCommand(args...)
			return
		}

		if err != nil {
			os.Exit(1)
		}
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func runDockerCommand(args ...string) {
	dockerCmd := exec.Command("docker", args...)
	dockerCmd.Stdin = os.Stdin
	dockerCmd.Stdout = os.Stdout
	dockerCmd.Stderr = os.Stderr

	if err := dockerCmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "docker %v failed: %v\n", args, err)
		os.Exit(1)
	}
}

func init() {
	// this makes the default docker behavior not work
	// rootCmd.AddCommand(imagesCmd)

	// these do not overload default docker commands so they belong here instead of above
	rootCmd.AddCommand(launchCmd)
	rootCmd.AddCommand(shutdownCmd)
	rootCmd.AddCommand(verifyCmd)
}
