package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"coral_cli/internal/runtime"
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
		case "-h", "--help":
			_ = cmd.Help()
		case "-v", "--version":
			fmt.Printf("coral version %s\n", strings.TrimPrefix(Version, "v"))
		// case "images":
		// 	err = imagesCmd.RunE(cmd, args[1:])
		// case "ps":
		// 	err = psCmd.RunE(cmd, args[1:])
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
	if err := runtime.Check(); err != nil {
		return err
	}
	dockerCmd := exec.Command(runtime.Current.Binary, args...)
	dockerCmd.Stdin = os.Stdin
	dockerCmd.Stdout = os.Stdout
	dockerCmd.Stderr = os.Stderr

	if err := dockerCmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "coral %v failed: %v\n", args, err)
		return err
	}

	return nil
}

// decodes the output of `runtime __complete` into the completions and directive that Cobra expects from a ValidArgsFunction.
func parseRuntimeCompletion(out []byte) ([]string, cobra.ShellCompDirective) {
	directive := cobra.ShellCompDirectiveNoFileComp
	var completions []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if strings.HasPrefix(line, ":") {
			if n, err := strconv.Atoi(line[1:]); err == nil {
				directive = cobra.ShellCompDirective(n)
			}
		} else if line != "" {
			completions = append(completions, line)
		}
	}
	return completions, directive
}

// printRuntimeCommands appends a "Runtime Commands (<binary>):" section to
// the root help output listing commands from the active container runtime that
// are not already provided as first-class coral commands.
func printRuntimeCommands(cmd *cobra.Command) {
	rt := runtime.Current.Binary
	out, _ := exec.Command(rt, "__complete", "--", "").Output()
	if len(out) == 0 {
		return
	}

	native := make(map[string]bool)
	for _, c := range cmd.Commands() {
		native[c.Name()] = true
	}

	type entry struct{ name, desc string }
	var entries []entry
	maxLen := 0
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" || strings.HasPrefix(line, ":") || strings.HasPrefix(line, "-") {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		name := parts[0]
		if native[name] {
			continue
		}
		desc := ""
		if len(parts) == 2 {
			desc = parts[1]
		}
		entries = append(entries, entry{name, desc})
		if len(name) > maxLen {
			maxLen = len(name)
		}
	}
	if len(entries) == 0 {
		return
	}

	w := cmd.OutOrStdout()
	title := strings.ToUpper(rt[:1]) + rt[1:]
	fmt.Fprintf(w, "\n%s Commands (%s):\n", title, rt)
	for _, e := range entries {
		if e.desc != "" {
			fmt.Fprintf(w, "  %-*s  %s\n", maxLen, e.name, e.desc)
		} else {
			fmt.Fprintf(w, "  %s\n", e.name)
		}
	}
	fmt.Fprintln(w)
}

func init() {
	// Append the active runtime's command list to the root help output.
	defaultHelp := rootCmd.HelpFunc()
	rootCmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		defaultHelp(cmd, args)
		printRuntimeCommands(cmd)
	})

	// Delegate completion for pass-through subcommands to the active container runtime.
	rootCmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		completionArgs := make([]string, 0, len(args)+3)
		completionArgs = append(completionArgs, "__complete", "--")
		completionArgs = append(completionArgs, args...)
		completionArgs = append(completionArgs, toComplete)
		out, _ := exec.Command(runtime.Current.Binary, completionArgs...).Output()
		return parseRuntimeCompletion(out)
	}

	// commands that do not overload docker commands belong here
	rootCmd.AddCommand(completionCmd)
	rootCmd.AddCommand(generateCmd)
	rootCmd.AddCommand(inspectCmd)
	rootCmd.AddCommand(launchCmd)
	rootCmd.AddCommand(loadCmd)
	rootCmd.AddCommand(saveCmd)
	rootCmd.AddCommand(shutdownCmd)
	rootCmd.AddCommand(tailCmd)
	rootCmd.AddCommand(verifyCmd)
	rootCmd.AddCommand(versionCmd)
}
