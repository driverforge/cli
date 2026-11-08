package cli

import (
	"errors"
	"testing"

	"github.com/driverforge/cli/internal/clierr"
)

func TestExperimental(t *testing.T) {
	for env, want := range map[string]bool{"": false, "debug": true, "publish, Debug": true, "all": true, "debugger": false} {
		t.Setenv("DRIVERFORGE_EXPERIMENTAL", env)
		if got := experimental("debug"); got != want {
			t.Errorf("DRIVERFORGE_EXPERIMENTAL=%q: experimental(debug) = %v, want %v", env, got, want)
		}
	}
}

// Unreleased, debug is hidden from help and refuses to run with a friendly
// not_available card; switched on, it is listed and runs.
func TestDebugIsGatedBehindExperimental(t *testing.T) {
	t.Setenv("DRIVERFORGE_EXPERIMENTAL", "")
	cmd := newDebugCmd()
	if !cmd.Hidden {
		t.Error("debug should be hidden from help until switched on")
	}
	err := cmd.RunE(cmd, nil)
	var ue *clierr.UserError
	if !errors.As(err, &ue) || ue.Code != codeNotAvailable {
		t.Fatalf("gated debug returned %v, want a %s UserError", err, codeNotAvailable)
	}

	t.Setenv("DRIVERFORGE_EXPERIMENTAL", "debug")
	if newDebugCmd().Hidden {
		t.Error("debug should be listed once switched on")
	}
}
