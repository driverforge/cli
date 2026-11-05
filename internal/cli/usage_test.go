package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/driverforge/cli/internal/clierr"
)

// A cobra usage error must come out the far end of the command tree as a
// friendly clierr.UserError carrying a docs link — never a reportable crash.
func TestUsageErrorsAreFriendly(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantURL string
	}{
		{"unknown command", []string{"foo"}, docsBaseURL + "/" + overviewSlug},
		{"too many args", []string{"build", "x", "y"}, docsBaseURL + "/cli-build"},
		{"unknown flag", []string{"build", "--nope"}, docsBaseURL + "/cli-build"},
		{"unknown subcommand", []string{"org", "show", "z", "q"}, docsBaseURL + "/cli-org"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := newRootCmd()
			root.SetArgs(c.args)
			root.SetOut(new(strings.Builder))
			root.SetErr(new(strings.Builder))

			cmd, err := root.ExecuteC()
			err = friendlyUsage(cmd, err)
			if err == nil {
				t.Fatal("expected a usage error")
			}
			if !clierr.IsUser(err) {
				t.Fatalf("not a UserError: %T %v", err, err)
			}
			var ue *clierr.UserError
			errors.As(err, &ue)
			if ue.Hint != "see "+c.wantURL {
				t.Errorf("hint = %q, want docs link %q", ue.Hint, c.wantURL)
			}
		})
	}
}

func TestDocsURL(t *testing.T) {
	root := &cobra.Command{Use: "driverforge"}
	build := &cobra.Command{Use: "build"}
	device := &cobra.Command{Use: "device"}
	sel := &cobra.Command{Use: "select"}
	device.AddCommand(sel)
	root.AddCommand(build, device)

	if got := docsURL(root); got != docsBaseURL+"/"+overviewSlug {
		t.Errorf("root docsURL = %q", got)
	}
	if got := docsURL(build); got != docsBaseURL+"/cli-build" {
		t.Errorf("build docsURL = %q", got)
	}
	// A nested subcommand points at its top-level group, not the leaf.
	if got := docsURL(sel); got != docsBaseURL+"/cli-device" {
		t.Errorf("nested docsURL = %q", got)
	}
}
