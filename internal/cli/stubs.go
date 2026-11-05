package cli

import (
	"github.com/spf13/cobra"

	"github.com/driverforge/cli/internal/clierr"
)

// newPublishCmd will publish a driver and upload its source map. Hidden until it
// ships alongside the marketplace; registered so the command path is reserved.
func newPublishCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "publish",
		Short:  "Publish a driver and upload its source map",
		Hidden: true,
		RunE: func(*cobra.Command, []string) error {
			return &clierr.UserError{
				Code:    codeNotAvailable,
				Title:   "Not available yet",
				Message: "driverforge publish isn't available in this release",
				Hint:    "it ships alongside the marketplace",
			}
		},
	}
}
