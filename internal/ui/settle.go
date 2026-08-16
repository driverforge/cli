package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// At startup Bubble Tea probes the terminal: the Kitty keyboard-enhancement
// query (CSI ? u) and, on terminals expected to support them, DECRQM queries
// for synchronized output (2026) and Unicode core (2027). The replies arrive
// on stdin moments later. A program that quits before consuming them restores
// the terminal to cooked mode first, so the replies echo at the shell prompt
// as stray escapes (`^[[?1u`, `^[[?2026;2$y^[[?2027;4$y`) — exactly what a
// fast spinner or an instantly-settled list would otherwise leave behind.
//
// probeSettle centralises the fix: models embed it, forward every message to
// Observe, and route their quits through Finish, which defers the actual
// tea.Quit until the probes have been answered or a short window elapsed
// (a terminal that answers nothing has nothing to leak).

// settleTimeout caps how long a finished model waits for the probe replies.
// Only incurred when they haven't already arrived.
const settleTimeout = 150 * time.Millisecond

// settleTickMsg fires when the settle window elapses without the probe
// replies (e.g. a terminal that doesn't answer them, so there's nothing to
// leak).
type settleTickMsg struct{}

// probeSettle tracks the terminal's answers to Bubble Tea's startup probes so
// a model can defer its quit until they've been consumed.
type probeSettle struct {
	kb        bool // KeyboardEnhancementsMsg arrived
	modes     int  // DECRQM mode reports arrived (2026 + 2027 expected)
	finishing bool // Finish was requested; quit as soon as settled
}

// answered reports whether every expected probe reply has been consumed.
func (s *probeSettle) answered() bool { return s.kb && s.modes >= 2 }

// Observe consumes settle-related messages. When it returns handled=true the
// model should return cmd (which is tea.Quit once a pending Finish resolves)
// and not process the message further.
func (s *probeSettle) Observe(msg tea.Msg) (handled bool, cmd tea.Cmd) {
	switch msg.(type) {
	case tea.KeyboardEnhancementsMsg:
		s.kb = true
	case tea.ModeReportMsg:
		s.modes++
	case settleTickMsg:
		if s.finishing {
			return true, tea.Quit
		}
		return true, nil
	default:
		return false, nil
	}
	if s.finishing && s.answered() {
		return true, tea.Quit
	}
	return true, nil
}

// Finish requests the model's quit: immediate when the probe replies are
// already consumed, otherwise deferred behind the settle window. Use for
// normal completion paths; a user interrupt (ctrl+c) should quit immediately —
// responsiveness beats a rare stray escape there.
func (s *probeSettle) Finish() tea.Cmd {
	s.finishing = true
	if s.answered() {
		return tea.Quit
	}
	return tea.Tick(settleTimeout, func(time.Time) tea.Msg { return settleTickMsg{} })
}
