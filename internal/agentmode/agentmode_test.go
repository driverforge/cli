package agentmode

import "testing"

func clearAgentEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DRIVERFORGE_AGENT_MODE", "")
	for _, name := range agentEnv {
		t.Setenv(name, "")
	}
}

func TestEnabled(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"nothing set", nil, false},
		{"explicit true", map[string]string{"DRIVERFORGE_AGENT_MODE": "true"}, true},
		{"explicit 1", map[string]string{"DRIVERFORGE_AGENT_MODE": "1"}, true},
		{"detected agent", map[string]string{"CLAUDECODE": "1"}, true},
		{"explicit false beats detection", map[string]string{"CLAUDECODE": "1", "DRIVERFORGE_AGENT_MODE": "false"}, false},
		{"unparseable override falls back to detection", map[string]string{"CURSOR_AGENT": "1", "DRIVERFORGE_AGENT_MODE": "yes please"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearAgentEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if got := Enabled(); got != tc.want {
				t.Errorf("Enabled() = %v, want %v", got, tc.want)
			}
		})
	}
}
