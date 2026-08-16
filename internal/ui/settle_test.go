package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// answerProbes feeds a model the terminal's startup-probe replies (Kitty
// keyboard enhancements + the two DECRQM mode reports), so a subsequent
// Finish-routed quit resolves immediately.
func answerProbes(m tea.Model) {
	m.Update(tea.KeyboardEnhancementsMsg{})
	m.Update(tea.ModeReportMsg{})
	m.Update(tea.ModeReportMsg{})
}

// A model that finishes before the terminal answers the startup probes must
// NOT quit yet — quitting first restores cooked mode and the replies echo to
// the shell (`^[[?1u`, `^[[?2026;2$y`). It schedules the settle fallback.
func TestSettleDefersQuitUntilProbesAnswered(t *testing.T) {
	var s probeSettle
	if _, ok := runCmd(s.Finish()).(settleTickMsg); !ok {
		t.Fatal("Finish before the probe replies should schedule the settle timeout, not quit")
	}
	if handled, cmd := s.Observe(tea.KeyboardEnhancementsMsg{}); !handled || cmd != nil {
		t.Fatal("the keyboard reply alone must not quit — the mode reports are still in flight")
	}
	s.Observe(tea.ModeReportMsg{})
	_, cmd := s.Observe(tea.ModeReportMsg{})
	if _, ok := runCmd(cmd).(tea.QuitMsg); !ok {
		t.Fatal("the final probe reply should resolve the pending quit")
	}
}

// When the replies already arrived (slower work, fast terminal), Finish quits
// immediately — no added latency.
func TestSettleQuitsImmediatelyWhenAnswered(t *testing.T) {
	var s probeSettle
	s.Observe(tea.KeyboardEnhancementsMsg{})
	s.Observe(tea.ModeReportMsg{})
	s.Observe(tea.ModeReportMsg{})
	if _, ok := runCmd(s.Finish()).(tea.QuitMsg); !ok {
		t.Fatal("Finish should quit immediately once the probes are answered")
	}
}

// A terminal that never answers (nothing to leak) still quits via the fallback.
func TestSettleTimeoutQuits(t *testing.T) {
	var s probeSettle
	s.Finish()
	_, cmd := s.Observe(settleTickMsg{})
	if _, ok := runCmd(cmd).(tea.QuitMsg); !ok {
		t.Fatal("the settle timeout should quit")
	}
}
