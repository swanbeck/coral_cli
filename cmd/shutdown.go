package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"darwin_cli/internal/cleanup"
	"darwin_cli/internal/compose"
	"darwin_cli/internal/metadata"
)

var (
	shutdownName   string
	shutdownHandle string
	shutdownGroup  string
	shutdownAll    bool
	shutdownKill   bool
)

func init() {
	shutdownCmd.Flags().StringVarP(&shutdownName, "name", "n", "", "Path to Docker Compose file")
	shutdownCmd.Flags().StringVar(&shutdownHandle, "handle", "", "Handle to shut down")
	shutdownCmd.Flags().StringVarP(&shutdownGroup, "group", "g", "", "Group to shut down")
	shutdownCmd.Flags().BoolVarP(&shutdownAll, "all", "a", false, "Shut down all instances")
	shutdownCmd.Flags().BoolVar(&shutdownKill, "kill", false, "Forcefully kills instances before removing them")
}

var shutdownCmd = &cobra.Command{
	Use:   "shutdown",
	Short: "Stop and remove resources for a given instance",
	RunE: func(cmd *cobra.Command, args []string) error {
		if shutdownAll {
			return shutdownAllInstances(shutdownKill)
		}
		if shutdownName != "" {
			return shutdownByName(shutdownName, shutdownKill)
		}
		if shutdownHandle != "" {
			return shutdownByHandle(shutdownHandle, shutdownKill)
		}
		if shutdownGroup != "" {
			return shutdownByGroup(shutdownGroup, shutdownKill)
		}
		return fmt.Errorf("no shutdown criteria provided: use --compose-file, --handle, or --group")
	},
}

func shutdownAllInstances(kill bool) error {
	metadataList, err := metadata.LoadAllMetadata()
	if err != nil {
		return fmt.Errorf("loading metadata: %w", err)
	}

	if len(metadataList) == 0 {
		fmt.Println("No instances found.")
		return nil
	}

	for _, meta := range metadataList {
		fmt.Printf("Shutting down instance: %s\n", meta.Name)
		profiles, err := extractProfiles(meta.ComposeFile)
		if err != nil {
			fmt.Printf("Failed to extract profiles for %s: %v\n", meta.Name, err)
			continue
		}
		if err := cleanup.StopCompose(meta.Name, meta.ComposeFile, kill, profiles); err != nil {
			fmt.Printf("Failed to stop compose for %s: %v\n", meta.Name, err)
		}
		if err := cleanup.RemoveInstanceFiles(meta.Name); err != nil {
			fmt.Printf("Failed to remove files for %s: %v\n", meta.Name, err)
		}
	}

	fmt.Println("All instances shut down.")
	return nil
}

func shutdownByName(name string, kill bool) error {
	metadataList, err := metadata.LoadAllMetadata()
	if err != nil {
		return err
	}

	for _, meta := range metadataList {
		if meta.Name == name {
			fmt.Printf("Shutting down instance with name %s\n", name)
			profiles, err := extractProfiles(meta.ComposeFile)
			if err != nil {
				fmt.Printf("Failed to extract profiles for %s: %v\n", meta.Name, err)
				continue
			}
			if err := cleanup.StopCompose(meta.Name, meta.ComposeFile, kill, profiles); err != nil {
				fmt.Printf("Failed to stop compose for %s: %v\n", meta.Name, err)
			}
			return cleanup.RemoveInstanceFiles(meta.Name)
		}
	}
	return fmt.Errorf("no instance found with name: %s", name)
}

func shutdownByHandle(handle string, kill bool) error {
	metadataList, err := metadata.LoadAllMetadata()
	if err != nil {
		return err
	}

	for _, meta := range metadataList {
		if meta.Handle == handle {
			fmt.Printf("Shutting down instance with handle %s\n", handle)
			profiles, err := extractProfiles(meta.ComposeFile)
			if err != nil {
				fmt.Printf("Failed to extract profiles for %s: %v\n", meta.Name, err)
				continue
			}
			if err := cleanup.StopCompose(meta.Name, meta.ComposeFile, kill, profiles); err != nil {
				fmt.Printf("Failed to stop compose for %s: %v\n", meta.Name, err)
			}
			return cleanup.RemoveInstanceFiles(meta.Name)
		}
	}
	return fmt.Errorf("no instance found with handle: %s", handle)
}

func shutdownByGroup(group string, kill bool) error {
	metadataList, err := metadata.LoadAllMetadata()
	if err != nil {
		return err
	}

	found := false
	for _, meta := range metadataList {
		if meta.Group == group {
			found = true
			fmt.Printf("Shutting down instance with group %s\n", group)
			profiles, err := extractProfiles(meta.ComposeFile)
			if err != nil {
				fmt.Printf("Failed to extract profiles for %s: %v\n", meta.Name, err)
				continue
			}
			if err := cleanup.StopCompose(meta.Name, meta.ComposeFile, kill, profiles); err != nil {
				fmt.Printf("Failed to stop compose for %s: %v\n", meta.Name, err)
			}
			if err := cleanup.RemoveInstanceFiles(meta.Name); err != nil {
				fmt.Printf("Failed to remove files for %s: %v\n", meta.Name, err)
			}
		}
	}
	if !found {
		return fmt.Errorf("no instances found with group: %s", group)
	}
	return nil
}

func extractProfiles(composePath string) ([]string, error) {
	env := map[string]string{}
	cf, err := compose.ParseCompose(composePath, env)
	if err != nil {
		return nil, fmt.Errorf("parsing compose file for profile extraction: %w", err)
	}

	profileSet := make(map[string]struct{})
	for _, svc := range cf.Services {
		if rawProfiles, ok := svc["profiles"]; ok {
			switch p := rawProfiles.(type) {
			case []interface{}:
				for _, val := range p {
					if str, ok := val.(string); ok {
						profileSet[str] = struct{}{}
					}
				}
			}
		}
	}

	var profiles []string
	for p := range profileSet {
		profiles = append(profiles, p)
	}
	return orderedProfiles(profiles), nil
}
