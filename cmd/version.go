package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// overridden at build time via -ldflags "-X 'coral_cli/cmd.Version=vX.Y.Z'"
var Version = "dev"

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the Coral version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("Coral version %s\n", Version)
	},
}

// parseMajorVersion extracts the leading integer from a version string of the form [v]MAJOR[.MINOR[.PATCH[...]]].
func parseMajorVersion(v string) (int, error) {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexByte(v, '.'); i != -1 {
		v = v[:i]
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("cannot parse major version from %q", v)
	}
	return n, nil
}
