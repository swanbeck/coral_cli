package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"coral_cli/internal/logging"
	"coral_cli/internal/metadata"
)

var (
	tailAll       bool
	tailInstances []string
	tailGroups    []string
	tailHandles   []string
)

func init() {
	tailCmd.Flags().BoolVarP(&tailAll, "all", "a", false, "Tail logs from all instances")
	tailCmd.Flags().StringSliceVarP(&tailInstances, "name", "n", []string{}, "List of instances to tail logs from")
	tailCmd.Flags().StringSliceVarP(&tailGroups, "group", "g", []string{}, "List of groups to tail logs from")
	tailCmd.Flags().StringSliceVarP(&tailHandles, "handle", "", []string{}, "List of handles to tail logs from")

	tailCmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if toComplete == "" {
			var out []string
			cmd.Flags().VisitAll(func(f *pflag.Flag) {
				if f.Shorthand != "" {
					out = append(out, fmt.Sprintf("#   --%s,-%s", f.Name, f.Shorthand))
				} else {
					out = append(out, fmt.Sprintf("#   --%s", f.Name))
				}
			})
			return out, cobra.ShellCompDirectiveNoFileComp
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	tailCmd.RegisterFlagCompletionFunc("name", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		metadataList, err := metadata.LoadAllMetadata()
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		var suggestions []string
		for _, m := range metadataList {
			if strings.HasPrefix(m.Name, toComplete) {
				suggestions = append(suggestions, m.Name)
			}
		}
		return suggestions, cobra.ShellCompDirectiveNoFileComp
	})
	tailCmd.RegisterFlagCompletionFunc("group", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		metadataList, err := metadata.LoadAllMetadata()
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		var suggestions []string
		for _, m := range metadataList {
			if strings.HasPrefix(m.Group, toComplete) {
				suggestions = append(suggestions, m.Group)
			}
		}
		return suggestions, cobra.ShellCompDirectiveNoFileComp
	})
	tailCmd.RegisterFlagCompletionFunc("handle", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		metadataList, err := metadata.LoadAllMetadata()
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		var suggestions []string
		for _, m := range metadataList {
			if strings.HasPrefix(m.Handle, toComplete) {
				suggestions = append(suggestions, m.Handle)
			}
		}
		return suggestions, cobra.ShellCompDirectiveNoFileComp
	})
}

var tailCmd = &cobra.Command{
	Use:   "tail <instance, group, handle>",
	Short: "Tails the logs of running Coral instances",
	RunE: func(cmd *cobra.Command, args []string) error {
		return tail(tailAll, tailInstances, tailGroups, tailHandles)
	},
}

func tail(all bool, instances, groups, handles []string) error {
	// load all metadata
	metadataList, err := metadata.LoadAllMetadata()
	if err != nil {
		return fmt.Errorf("loading metadata: %w", err)
	}
	if len(metadataList) == 0 {
		fmt.Println("No instances found.")
		return nil
	}

	// list of containers to tail
	var containers []metadata.ContainerInfo

	for _, meta := range metadataList {
		if all || slices.Contains(instances, meta.Name) || slices.Contains(groups, meta.Group) || slices.Contains(handles, meta.Handle) {
			instance_containers, err := logging.GetContainerInfo(meta.Name, meta.ComposeFile)
			if err != nil {
				return fmt.Errorf("getting container info for %s: %w", meta.Name, err)
			}
			containers = append(containers, instance_containers...)
		}
	}

	if len(containers) == 0 {
		return fmt.Errorf("no containers found matching criteria")
	}

	shutdownChan := make(chan struct{})
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-signalChan
		signal.Stop(signalChan)
		close(shutdownChan)
	}()

	doneChan, errCh := logging.TailLogs(containers, shutdownChan, false)

	select {
	case <-shutdownChan:
		fmt.Printf("\n%s\n", logging.Warning("Interrupt received. Detaching..."))
	case <-doneChan:
		fmt.Println(logging.Info("All log tails completed. Exiting..."))
	case err := <-errCh:
		fmt.Println(logging.Failure(fmt.Sprintf("Error while streaming logs: %v", err)))
	}

	return nil
}
