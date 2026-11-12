package cli

import (
	"errors"
	"testing"

	"github.com/driverforge/cli/internal/clierr"
)

func TestExperimental(t *testing.T) {
	for env, want := range map[string]bool{"": false, "run": true, "publish, Run": true, "all": true, "runner": false} {
		t.Setenv("DRIVERFORGE_EXPERIMENTAL", env)
		if got := experimental("run"); got != want {
			t.Errorf("DRIVERFORGE_EXPERIMENTAL=%q: experimental(run) = %v, want %v", env, got, want)
		}
	}
}

// Unreleased, run is hidden from help and refuses to run with a friendly
// not_available card; switched on, it is listed and runs.
func TestRunIsGatedBehindExperimental(t *testing.T) {
	t.Setenv("DRIVERFORGE_EXPERIMENTAL", "")
	cmd := newRunCmd()
	if !cmd.Hidden {
		t.Error("run should be hidden from help until switched on")
	}
	err := cmd.RunE(cmd, nil)
	var ue *clierr.UserError
	if !errors.As(err, &ue) || ue.Code != codeNotAvailable {
		t.Fatalf("gated run returned %v, want a %s UserError", err, codeNotAvailable)
	}

	t.Setenv("DRIVERFORGE_EXPERIMENTAL", "run")
	if newRunCmd().Hidden {
		t.Error("run should be listed once switched on")
	}
}
