package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/driverforge/cli/internal/clierr"
)

// newCurrentCmd builds a hidden `current` porcelain command: it prints one
// field of the local context for scripts and shell prompts (e.g. a Spaceship
// section). It reads the context only — no network — and when nothing is
// selected it prints nothing and exits non-zero silently, so a prompt shows
// nothing rather than an error.
//
// `org current` and `device current` are the same command over different
// getters; value returns the string to print, or an error meaning "nothing
// selected".
func newCurrentCmd(short string, value func() (string, error)) *cobra.Command {
	return &cobra.Command{
		Use:    "current",
		Short:  short,
		Args:   cobra.NoArgs,
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			v, err := value()
			if err != nil {
				return clierr.Silent()
			}
			fmt.Fprintln(cmd.OutOrStdout(), v)
			return nil
		},
	}
}
