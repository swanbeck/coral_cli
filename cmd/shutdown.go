package cmd

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"darwin_cli/internal/cleanup"
	// "darwin_cli/internal/io"
	"darwin_cli/internal/metadata"
)

var (
	shutdownComposeFile string
	shutdownHandle      string
	shutdownDeviceID    string
	shutdownAll	        bool
	killShutdown        bool
)

func init() {
	rootCmd.AddCommand(shutdownCmd)

	shutdownCmd.Flags().StringVarP(&shutdownComposeFile, "compose-file", "f", "", "Path to Docker Compose file")
	shutdownCmd.Flags().StringVar(&shutdownHandle, "handle", "", "Handle to shut down")
	shutdownCmd.Flags().StringVar(&shutdownDeviceID, "device-id", "", "Device ID to shut down")
	shutdownCmd.Flags().BoolVarP(&shutdownAll, "all", "a", false, "Shut down all instances")
	shutdownCmd.Flags().BoolVarP(&killShutdown, "kill", "k", false, "Forcefully kills instances before removing them")
}

var shutdownCmd = &cobra.Command{
	Use:   "shutdown",
	Short: "Stop and remove resources for a given instance",
	RunE: func(cmd *cobra.Command, args []string) error {
		if shutdownAll {
			return shutdownAllInstances(killShutdown)
		}
		if shutdownComposeFile != "" {
			return shutdownByComposeFile(shutdownComposeFile, killShutdown)
		}
		if shutdownHandle != "" {
			return shutdownByHandle(shutdownHandle, killShutdown)
		}
		if shutdownDeviceID != "" {
			return shutdownByDeviceID(shutdownDeviceID, killShutdown)
		}
		return fmt.Errorf("no shutdown criteria provided: use --compose-file, --handle, or --device-id")
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
		if err := cleanup.StopCompose(meta.ComposeFile, kill); err != nil {
			fmt.Printf("Failed to stop compose for %s: %v\n", meta.Name, err)
		}
		if err := cleanup.RemoveInstanceFiles(meta.Name); err != nil {
			fmt.Printf("Failed to remove files for %s: %v\n", meta.Name, err)
		}
	}

	fmt.Println("All instances shut down.")
	return nil
}

func shutdownByComposeFile(composePath string, kill bool) error {
	fmt.Printf("Stopping compose: %s\n", composePath)
	if err := cleanup.StopCompose(composePath, kill); err != nil {
		return fmt.Errorf("stopping compose: %w", err)
	}

	instanceName := strings.TrimSuffix(filepath.Base(composePath), filepath.Ext(composePath))
	return cleanup.RemoveInstanceFiles(instanceName)
}

func shutdownByHandle(handle string, kill bool) error {
	metadataList, err := metadata.LoadAllMetadata()
	if err != nil {
		return err
	}

	for _, meta := range metadataList {
		if meta.Handle == handle {
			fmt.Printf("Shutting down instance with handle %s\n", handle)
			if err := cleanup.StopCompose(meta.ComposeFile, kill); err != nil {
				fmt.Printf("Failed to stop compose for %s: %v\n", meta.Name, err)
			}
			return cleanup.RemoveInstanceFiles(meta.Name)
		}
	}
	return fmt.Errorf("no instance found with handle: %s", handle)
}

func shutdownByDeviceID(deviceID string, kill bool) error {
	metadataList, err := metadata.LoadAllMetadata()
	if err != nil {
		return err
	}

	found := false
	for _, meta := range metadataList {
		if meta.DeviceID == deviceID {
			found = true
			fmt.Printf("Shutting down instance with device-id %s\n", deviceID)
			if err := cleanup.StopCompose(meta.ComposeFile, kill); err != nil {
				fmt.Printf("Failed to stop compose for %s: %v\n", meta.Name, err)
			}
			if err := cleanup.RemoveInstanceFiles(meta.Name); err != nil {
				fmt.Printf("Failed to remove files for %s: %v\n", meta.Name, err)
			}
		}
	}
	if !found {
		return fmt.Errorf("no instances found with device-id: %s", deviceID)
	}
	return nil
}
