package ui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func pressTab(m *FrameModel) tea.Cmd {
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	return cmd
}

func runCmd(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	return cmd()
}

// A run that finishes before the terminal answers Bubble Tea's startup probes
// must NOT quit yet — quitting first restores the terminal and the replies
// leak to the shell. It schedules the settle fallback and quits once the last
// reply arrives.
func TestFrameWaitsForProbeReplies(t *testing.T) {
	m := NewFrameModel("Building…", false)
	_, cmd := m.Update(frameDoneMsg{})
	if _, ok := runCmd(cmd).(settleTickMsg); !ok {
		t.Fatal("frameDoneMsg before the probe replies should schedule the settle timeout, not quit")
	}

	m.Update(tea.KeyboardEnhancementsMsg{})
	m.Update(tea.ModeReportMsg{})
	_, cmd = m.Update(tea.ModeReportMsg{})
	if _, ok := runCmd(cmd).(tea.QuitMsg); !ok {
		t.Fatal("frame did not quit after the probe replies arrived")
	}
}

// When the replies already arrived (slower run, fast terminal), frameDoneMsg
// quits immediately — no added latency.
func TestFrameQuitsImmediatelyWhenAlreadySettled(t *testing.T) {
	m := NewFrameModel("Building…", false)
	answerProbes(m)
	_, cmd := m.Update(frameDoneMsg{})
	if _, ok := runCmd(cmd).(tea.QuitMsg); !ok {
		t.Fatal("frame should quit immediately once the replies already arrived")
	}
}

// A terminal that never answers the probes (nothing to leak) still quits via
// the fallback timeout.
func TestFrameSettleTimeoutQuits(t *testing.T) {
	m := NewFrameModel("Building…", false)
	m.Update(frameDoneMsg{})
	_, cmd := m.Update(settleTickMsg{})
	if _, ok := runCmd(cmd).(tea.QuitMsg); !ok {
		t.Fatal("frame did not quit on the settle timeout")
	}
}

// Tab toggles the details log while running; the log stays hidden otherwise.
func TestFrameTabTogglesDetails(t *testing.T) {
	m := NewFrameModel("Building…", false)
	m.Update(frameLogMsg{t: time.Now(), text: "✓ Squish Lua (12ms)"})

	if v := m.View().Content; strings.Contains(v, "Squish Lua") {
		t.Errorf("log should be hidden before tab:\n%s", v)
	}
	if !strings.Contains(m.View().Content, "View Details") {
		t.Errorf("command bar should offer View Details:\n%s", m.View().Content)
	}

	pressTab(m)
	v := m.View().Content
	if !strings.Contains(v, "Squish Lua") {
		t.Errorf("log should be visible after tab:\n%s", v)
	}
	if !strings.Contains(v, "Hide Details") {
		t.Errorf("command bar should offer Hide Details while open:\n%s", v)
	}

	pressTab(m)
	if v := m.View().Content; strings.Contains(v, "Squish Lua") {
		t.Errorf("log should hide again on second tab:\n%s", v)
	}
}

// Failure auto-expands the details log so the chronology lands in scrollback
// above the red frame and the error card.
func TestFrameFailureAutoExpandsLog(t *testing.T) {
	m := NewFrameModel("Building…", false)
	m.Update(frameLogMsg{t: time.Now(), text: "✗ Squish Lua: boom"})
	m.Update(frameDoneMsg{err: errors.New("boom")})

	v := m.View().Content
	if !strings.Contains(v, "Squish Lua") {
		t.Errorf("failure should reveal the log:\n%s", v)
	}
	if !strings.Contains(v, "✗") {
		t.Errorf("the step line should carry a ✗:\n%s", v)
	}
	if strings.Contains(v, "View Details") || strings.Contains(v, "Hide Details") {
		t.Errorf("the command bar must disappear once done:\n%s", v)
	}
}

// A ctrl+c interrupt resolves the frame with the interrupt sentinel and does
// NOT dump the log — "No changes were made" shouldn't come with a build log.
func TestFrameInterrupt(t *testing.T) {
	m := NewFrameModel("Building…", false)
	m.Update(frameLogMsg{t: time.Now(), text: "✓ Squish Lua (12ms)"})
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if _, ok := runCmd(cmd).(tea.QuitMsg); !ok {
		t.Fatal("ctrl+c should quit")
	}
	if !errors.Is(m.Err(), tea.ErrInterrupted) {
		t.Fatalf("ctrl+c must set the interrupt sentinel, got %v", m.Err())
	}
	if v := m.View().Content; strings.Contains(v, "Squish Lua") {
		t.Errorf("interrupt must not auto-expand the log:\n%s", v)
	}
	// A late frameDoneMsg (the cancelled work returning) must not overwrite
	// the interrupt outcome.
	m.Update(frameDoneMsg{err: errors.New("context canceled")})
	if !errors.Is(m.Err(), tea.ErrInterrupted) {
		t.Fatalf("late done msg overwrote the interrupt, got %v", m.Err())
	}
}

// A warn entry degrades a successful outcome to ⚠ (amber), not ✓.
func TestFrameWarnDegradesOutcome(t *testing.T) {
	m := NewFrameModel("Syncing…", false)
	m.Update(frameLogMsg{t: time.Now(), text: "couldn't verify against a prior deploy", warn: true})
	m.Update(frameDoneMsg{})

	if m.outcome != frameWarn {
		t.Fatalf("outcome = %v, want frameWarn", m.outcome)
	}
	if v := m.View().Content; !strings.Contains(v, "⚠") {
		t.Errorf("warn outcome should render ⚠:\n%s", v)
	}
}

// Summary rows upsert by key and render aligned under the step line.
func TestFrameRows(t *testing.T) {
	m := NewFrameModel("Building…", false)
	m.Update(frameRowMsg{key: "driver", val: "old"})
	m.Update(frameRowMsg{key: "driver", val: "test"})
	m.Update(frameRowMsg{key: "version", val: "2026072012"})

	v := m.View().Content
	if strings.Contains(v, "old") {
		t.Errorf("row upsert should replace the value:\n%s", v)
	}
	if !strings.Contains(v, "driver") || !strings.Contains(v, "test") || !strings.Contains(v, "2026072012") {
		t.Errorf("rows missing:\n%s", v)
	}
}

// While live, only a tail of the log renders (an inline region taller than the
// terminal repaints badly); once done, the full log lands in the final render.
func TestFrameLogTailCap(t *testing.T) {
	m := NewFrameModel("Building…", true)
	for i := 0; i < frameLogTail+5; i++ {
		m.Update(frameLogMsg{t: time.Now(), text: fmt.Sprintf("line-%d", i)})
	}

	v := m.View().Content
	if strings.Contains(v, "line-0") {
		t.Errorf("live view should cap the log tail:\n%s", v)
	}
	if !strings.Contains(v, "… 5 earlier lines") {
		t.Errorf("live view should note elided lines:\n%s", v)
	}

	m.Update(frameDoneMsg{})
	v = m.View().Content
	if !strings.Contains(v, "line-0") || strings.Contains(v, "earlier lines") {
		t.Errorf("final render should show the full log:\n%s", v)
	}
}
