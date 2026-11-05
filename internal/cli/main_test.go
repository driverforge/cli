package cli

import (
	"os"
	"testing"
)

// TestMain runs every test as a person at a terminal would, whatever shell the
// suite runs in: a coding agent's environment would otherwise switch commands
// to structured output. Tests of structured output opt in with t.Setenv.
func TestMain(m *testing.M) {
	_ = os.Setenv("DRIVERFORGE_AGENT_MODE", "false")
	_ = os.Unsetenv("DRIVERFORGE_OUTPUT")
	os.Exit(m.Run())
}
