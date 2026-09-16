package cmd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"coral_cli/internal/logging"
	"coral_cli/internal/runtime"
)

// substitutes the runtime binary name with "coral" in pass-through output so users see consistent branding without being surprised by docker/podman references
type replacingWriter struct {
	w   io.Writer
	old string
}

func (r *replacingWriter) Write(p []byte) (n int, err error) {
	cap := strings.ToUpper(r.old[:1]) + r.old[1:]
	s := strings.ReplaceAll(string(p), cap, "Coral")
	s = strings.ReplaceAll(s, r.old, "coral")
	_, err = r.w.Write([]byte(s))
	return len(p), err
}

func brandedWriter(f *os.File, rt string) io.Writer {
	info, err := f.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return f
	}
	return &replacingWriter{w: f, old: rt}
}

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
	rt := runtime.Current.Binary
	// a diagnostic, not output: on stdout it lands in the middle of whatever the delegated command produced
	fmt.Fprintln(os.Stderr, logging.Info(fmt.Sprintf("Delegating '%s' to %s", args[0], rt)))
	dockerCmd := exec.Command(rt, args...)
	dockerCmd.Stdin = os.Stdin
	dockerCmd.Stdout = brandedWriter(os.Stdout, rt)
	dockerCmd.Stderr = brandedWriter(os.Stderr, rt)

	if err := dockerCmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "coral %v failed: %v\n", args, err)
		return err
	}

	return nil
}

// decodes the output of `runtime __complete` into the completions and directive that Cobra expects from a ValidArgsFunction
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

// prints runtime commands provided by the underlying runtime that are not overloaded by coral subcommands; invoked as part of the root help output
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
	fmt.Fprintf(w, "\nAdditional Commands  (via %s):\n", rt)
	for _, e := range entries {
		if e.desc != "" {
			fmt.Fprintf(w, "  %-*s  %s\n", maxLen, e.name, e.desc)
		} else {
			fmt.Fprintf(w, "  %s\n", e.name)
		}
	}
}

func init() {
	rootCmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		w := cmd.OutOrStdout()

		desc := cmd.Long
		if desc == "" {
			desc = cmd.Short
		}
		if desc != "" {
			fmt.Fprintln(w, strings.TrimRight(desc, " \t\n"))
			fmt.Fprintln(w)
		}

		fmt.Fprintf(w, "Usage:\n  %s\n", cmd.UseLine())
		if cmd.HasAvailableSubCommands() {
			fmt.Fprintf(w, "  %s [command]\n", cmd.CommandPath())
		}
		if len(cmd.Aliases) > 0 {
			fmt.Fprintf(w, "\nAliases:\n  %s\n", cmd.NameAndAliases())
		}
		if cmd.HasExample() {
			fmt.Fprintf(w, "\nExamples:\n%s\n", cmd.Example)
		}

		if cmd.HasAvailableSubCommands() {
			nameWidth := 0
			for _, c := range cmd.Commands() {
				if (c.IsAvailableCommand() || c.Name() == "help") && len(c.Name()) > nameWidth {
					nameWidth = len(c.Name())
				}
			}
			fmt.Fprintln(w, "\nNative Commands:")
			for _, c := range cmd.Commands() {
				if c.IsAvailableCommand() || c.Name() == "help" {
					fmt.Fprintf(w, "  %-*s  %s\n", nameWidth, c.Name(), c.Short)
				}
			}
		}

		if cmd == rootCmd {
			printRuntimeCommands(cmd)
		}

		if cmd.HasAvailableLocalFlags() {
			fmt.Fprintf(w, "\nFlags:\n%s\n", strings.TrimRight(cmd.LocalFlags().FlagUsages(), " \t\n"))
		}
		if cmd.HasAvailableInheritedFlags() {
			fmt.Fprintf(w, "\nGlobal Flags:\n%s\n", strings.TrimRight(cmd.InheritedFlags().FlagUsages(), " \t\n"))
		}
		if cmd.HasHelpSubCommands() {
			fmt.Fprintln(w, "\nAdditional help topics:")
			for _, c := range cmd.Commands() {
				if c.IsAdditionalHelpTopicCommand() {
					fmt.Fprintf(w, "  %-11s  %s\n", c.Name(), c.Short)
				}
			}
		}
		if cmd.HasAvailableSubCommands() {
			fmt.Fprintf(w, "\nUse \"%s [command] --help\" for more information about a command.\n", cmd.CommandPath())
		}
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
	rootCmd.AddCommand(extractCmd)
	rootCmd.AddCommand(generateCmd)
	rootCmd.AddCommand(healthCmd)
	rootCmd.AddCommand(inspectCmd)
	rootCmd.AddCommand(launchCmd)
	rootCmd.AddCommand(loadCmd)
	rootCmd.AddCommand(saveCmd)
	rootCmd.AddCommand(shutdownCmd)
	rootCmd.AddCommand(tailCmd)
	rootCmd.AddCommand(verifyCmd)
	rootCmd.AddCommand(versionCmd)
}
