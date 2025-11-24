package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "coral",
	Short: "Coral provides and manages an ecosystem of compositional robotics software",
	// disable cobra's built-in subcommand parsing to allow anything that is not overwritten to directly call docker
	DisableFlagParsing: true,
	Args:               cobra.ArbitraryArgs,
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) == 0 {
			_ = cmd.Help()
			return
		}
		// we need to handle base docker commands (or commands we want to treat like docker commands but with extra pre- or post-processing) explictly so all flags are passed along to docker rather than being parsed by cobra
		var err error

		switch args[0] {
		case "images":
			err = imagesCmd.RunE(cmd, args[1:])
		case "ps":
			err = psCmd.RunE(cmd, args[1:])
		default:
			err = runDockerCommand(args...)
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

func runDockerCommand(args ...string) error {
	dockerCmd := exec.Command("docker", args...)
	dockerCmd.Stdin = os.Stdin
	dockerCmd.Stdout = os.Stdout
	dockerCmd.Stderr = os.Stderr

	if err := dockerCmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "coral %v failed: %v\n", args, err)
		return err
	}

	return nil
}

func init() {
	// commands that do not overload docker commands belong here
	rootCmd.AddCommand(completionCmd)
	rootCmd.AddCommand(launchCmd)
	rootCmd.AddCommand(shutdownCmd)
	rootCmd.AddCommand(tailCmd)
	rootCmd.AddCommand(verifyCmd)
}
