package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func newCheckIn() *checkInModel {
	m := &checkInModel{driver: "Eight Sleep", url: "https://app.driverforge.test/suru/eight-sleep/events/", events: make(chan struct{}), spinner: newSpinner()}
	answerProbes(m) // terminal probes answered — quits resolve immediately
	return m
}

func isQuit(cmd tea.Cmd) bool {
	_, ok := runCmd(cmd).(tea.QuitMsg)
	return ok
}

func TestCheckInHidesCelebrationWhileCounting(t *testing.T) {
	m := newCheckIn()
	if v := m.View().Content; !strings.Contains(v, "Waiting for Eight Sleep to check in") {
		t.Errorf("waiting view wrong:\n%s", v)
	}

	// Events arrive and the counter ticks — but the celebration stays hidden.
	m.Update(checkInEventMsg{})
	m.Update(checkInEventMsg{})
	m.Update(checkInEventMsg{})
	m.Update(countTickMsg{})
	m.Update(countTickMsg{})
	if m.shown != 2 {
		t.Fatalf("shown = %d, want 2", m.shown)
	}
	v := m.View().Content
	if strings.Contains(v, "🎉") {
		t.Errorf("celebration must stay hidden while counting:\n%s", v)
	}
	if !strings.Contains(v, "2 events received") {
		t.Errorf("counter line missing:\n%s", v)
	}
}

func TestCheckInRevealsAtCap(t *testing.T) {
	m := newCheckIn()
	for i := 0; i < 15; i++ { // a big burst — well past the cap
		m.Update(checkInEventMsg{})
	}
	var cmd tea.Cmd
	for i := 0; i < revealCap; i++ { // tick the display up to the cap
		_, cmd = m.Update(countTickMsg{})
	}
	if m.shown != revealCap {
		t.Fatalf("shown = %d, want %d", m.shown, revealCap)
	}
	if !isQuit(cmd) {
		t.Error("reaching the cap should reveal + quit")
	}
	v := m.View().Content
	for _, want := range []string{"🎉", "10 events received", "/eight-sleep/events/"} {
		if !strings.Contains(v, want) {
			t.Errorf("revealed view missing %q:\n%s", want, v)
		}
	}
}

func TestCheckInRevealsWhenBurstGoesQuiet(t *testing.T) {
	m := newCheckIn()
	m.Update(checkInEventMsg{}) // received 1, gen 1
	m.Update(checkInEventMsg{}) // received 2, gen 2
	m.Update(checkInEventMsg{}) // received 3, gen 3
	m.Update(countTickMsg{})
	m.Update(countTickMsg{})
	m.Update(countTickMsg{}) // shown caught up to 3
	if strings.Contains(m.View().Content, "🎉") {
		t.Error("celebration revealed before the burst went quiet")
	}

	// A stale quiet (superseded by a newer event) must not settle.
	if _, cmd := m.Update(quietMsg{gen: 1}); isQuit(cmd) {
		t.Error("a stale quiet token should be ignored")
	}

	// The latest quiet, with the counter caught up, reveals + quits.
	_, cmd := m.Update(quietMsg{gen: 3})
	if !isQuit(cmd) {
		t.Error("the burst going quiet should reveal + quit")
	}
	v := m.View().Content
	if !strings.Contains(v, "🎉") || !strings.Contains(v, "3 events received") {
		t.Errorf("revealed view wrong:\n%s", v)
	}
}

func TestCheckInTimesOutQuietly(t *testing.T) {
	m := newCheckIn()
	_, cmd := m.Update(checkInTimeoutMsg{})
	if !isQuit(cmd) {
		t.Error("first-wait timeout with no event should quit")
	}
	if v := m.View().Content; v != "" {
		t.Errorf("timed-out view should be empty, got %q", v)
	}
}
