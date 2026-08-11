package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"coral_cli/internal/health"
	"coral_cli/internal/logging"
	"coral_cli/internal/runtime"
	"coral_cli/internal/util"
)

var (
	healthName   string
	healthHandle string
	healthGroup  string
)

func init() {
	healthCmd.Flags().StringVarP(&healthName, "name", "n", "", "Instance name to check")
	healthCmd.Flags().StringVar(&healthHandle, "handle", "", "Handle to check")
	healthCmd.Flags().StringVarP(&healthGroup, "group", "g", "", "Group to check")

	healthCmd.RegisterFlagCompletionFunc("name", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		metadataList, err := util.LoadAllMetadata()
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
	healthCmd.RegisterFlagCompletionFunc("group", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		metadataList, err := util.LoadAllMetadata()
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
	healthCmd.RegisterFlagCompletionFunc("handle", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		metadataList, err := util.LoadAllMetadata()
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

var healthCmd = &cobra.Command{
	Use:          "health",
	Short:        "Check the health of a coral instance or group",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if healthName == "" && healthHandle == "" && healthGroup == "" {
			return fmt.Errorf("one of --name, --handle, or --group is required")
		}
		if err := runtime.Check(); err != nil {
			return err
		}

		allMeta, err := util.LoadAllMetadata()
		if err != nil {
			return fmt.Errorf("loading instance metadata: %w", err)
		}

		instanceNames, err := resolveHealthInstances(allMeta, healthName, healthHandle, healthGroup)
		if err != nil {
			return err
		}

		label := fmt.Sprintf("(%d instance(s))", len(instanceNames))
		switch health.GroupHealth(instanceNames) {
		case health.StatusHealthy:
			fmt.Println(logging.Info("healthy " + label))
		case health.StatusStarting:
			fmt.Fprintln(os.Stderr, logging.Info("health starting "+label))
			os.Exit(2)
		default:
			fmt.Fprintln(os.Stderr, logging.Warning("unhealthy "+label))
			os.Exit(1)
		}
		return nil
	},
}

func resolveHealthInstances(allMeta []util.InstanceMetadata, name, handle, group string) ([]string, error) {
	switch {
	case name != "":
		for _, m := range allMeta {
			if m.Name == name {
				return []string{m.Name}, nil
			}
		}
		return nil, fmt.Errorf("no instance found with name: %s", name)
	case handle != "":
		for _, m := range allMeta {
			if m.Handle == handle {
				return []string{m.Name}, nil
			}
		}
		return nil, fmt.Errorf("no instance found with handle: %s", handle)
	default:
		var names []string
		for _, m := range allMeta {
			if m.Group == group {
				names = append(names, m.Name)
			}
		}
		if len(names) == 0 {
			return nil, fmt.Errorf("no instances found with group: %s", group)
		}
		return names, nil
	}
}
