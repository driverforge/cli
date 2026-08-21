package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/driverforge/cli/internal/agent"
)

func devEvent(state agent.DeployState, step string) DeployEventMsg {
	return DeployEventMsg(agent.DeployEvent{State: state, Step: step})
}

func TestDeployModelOK(t *testing.T) {
	m := NewDeployModel("Deploying test to", "core1")
	m.Update(devEvent(agent.StateStaged, "driver written to the controller"))
	m.Update(devEvent(agent.StateReinstalling, "director reinstalling the driver"))
	m.Update(devEvent(agent.StateRestarting, "driver restarting"))

	v := m.View().Content
	if !strings.Contains(v, "driver written to the controller") || !strings.Contains(v, "driver restarting") {
		t.Errorf("step lines missing:\n%s", v)
	}

	// The terminal event resolves the last spinner line to its final wording.
	m.Update(DeployDoneMsg{State: agent.StateConfirmed, Step: "driver restarted", Version: "1.2.3", Outcome: DeployOK})
	v = m.View().Content
	if !strings.Contains(v, "driver restarted") || strings.Contains(v, "driver restarting") {
		t.Errorf("terminal should resolve 'restarting'→'restarted':\n%s", v)
	}
	if !strings.Contains(v, "deploy complete") || !strings.Contains(v, "(version 1.2.3)") {
		t.Errorf("version line missing:\n%s", v)
	}
	if !strings.Contains(v, "core1") {
		t.Errorf("controller context missing from deploy summary:\n%s", v)
	}
	if m.Outcome() != DeployOK || m.Version() != "1.2.3" {
		t.Errorf("accessors: outcome=%v version=%q", m.Outcome(), m.Version())
	}
}

func TestDeployModelUnconfirmed(t *testing.T) {
	m := NewDeployModel("Deploying test to", "core1")
	m.Update(devEvent(agent.StateStaged, "staged"))
	m.Update(devEvent(agent.StateRestarting, "driver restarting"))
	m.Update(DeployDoneMsg{State: agent.StateUnconfirmed, Step: "couldn't confirm restart", Detail: "older SDK?", Outcome: DeployWarn})

	v := m.View().Content
	if !strings.Contains(v, "couldn't confirm restart") {
		t.Errorf("resolved warn line missing:\n%s", v)
	}
	if !strings.Contains(v, "older SDK?") {
		t.Errorf("warn detail missing from summary:\n%s", v)
	}
	if strings.Contains(v, "deployed") {
		t.Errorf("unconfirmed must not claim a deployed version:\n%s", v)
	}
	if m.Outcome() != DeployWarn {
		t.Errorf("outcome = %v, want DeployWarn", m.Outcome())
	}
}

func TestDeployModelConfirmedWithErrors(t *testing.T) {
	m := NewDeployModel("Deploying test to", "core1")
	m.Update(devEvent(agent.StateStaged, "staged"))
	m.Update(devEvent(agent.StateRestarting, "driver restarting"))
	m.Update(DeployDoneMsg{
		State:     agent.StateConfirmedWithErrors,
		Step:      "driver restarted with init errors",
		Version:   "1.2.3",
		Detail:    "init: index nil",
		ReportURL: "https://dev.driverforge.com/reports/abc",
		Outcome:   DeployWarn,
	})

	v := m.View().Content
	if !strings.Contains(v, "init: index nil") {
		t.Errorf("init error detail missing:\n%s", v)
	}
	if !strings.Contains(v, "reports/abc") {
		t.Errorf("error-report link missing:\n%s", v)
	}
	if !strings.Contains(v, "deploy complete") || !strings.Contains(v, "(version 1.2.3)") {
		t.Errorf("a running driver should still report its version:\n%s", v)
	}
}

func TestDeployModelFailed(t *testing.T) {
	m := NewDeployModel("Deploying test to", "core1")
	m.Update(devEvent(agent.StateStaged, "staged"))
	m.Update(devEvent(agent.StateReinstalling, "reinstalling"))
	m.Update(DeployDoneMsg{State: agent.StateFailed, Step: "driver failed to load", Outcome: DeployFail, Err: errors.New("init crashed")})

	v := m.View().Content
	if !strings.Contains(v, "driver failed to load") {
		t.Errorf("the failed step should be resolved on its line:\n%s", v)
	}
	if m.Outcome() != DeployFail {
		t.Errorf("outcome = %v, want DeployFail", m.Outcome())
	}
}

func TestDeployModelEmptyVersion(t *testing.T) {
	m := NewDeployModel("Deploying test to", "core1")
	m.Update(devEvent(agent.StateStaged, "staged"))
	m.Update(DeployDoneMsg{State: agent.StateConfirmed, Step: "done", Outcome: DeployOK})
	v := m.View().Content
	if !strings.Contains(v, "✓ deploy complete") || strings.Contains(v, "(version )") {
		t.Errorf("empty version should read '✓ deploy complete' with no version suffix:\n%s", v)
	}
}
