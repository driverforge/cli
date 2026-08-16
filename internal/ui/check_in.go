package ui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
)

const (
	// checkInFirstWait bounds how long we wait for the driver's first event
	// before giving up (and showing nothing extra — the deploy already succeeded).
	checkInFirstWait = 30 * time.Second
	// revealCap bounds the climb: once the counter reaches it, we reveal without
	// waiting to see how many more events are coming.
	revealCap = 10
	// quietPeriod is how long without a new event means "the burst is done", so a
	// small batch reveals promptly rather than waiting out a fixed window.
	quietPeriod = 600 * time.Millisecond
	// countTickInterval paces the displayed counter. Events stream in far faster
	// than this, so the display climbs at a steady, watchable rate toward the
	// number received rather than snapping to it.
	countTickInterval = 50 * time.Millisecond
)

type (
	checkInEventMsg   struct{}          // one telemetry event arrived
	checkInClosedMsg  struct{}          // the event stream ended
	checkInTimeoutMsg struct{}          // no first event within checkInFirstWait
	countTickMsg      struct{}          // advance the displayed counter one step
	quietMsg          struct{ gen int } // no event for quietPeriod (debounce token)
)

// checkInModel first shows a live counter climbing as the driver's events stream
// in, then — once the counter reaches the batch total (burst gone quiet) or a
// cap — reveals the celebration and link. The celebration stays hidden while the
// counter is still climbing. events pulses once per event.
type checkInModel struct {
	driver   string
	url      string
	events   <-chan struct{}
	spinner  spinner.Model
	received int         // events counted so far
	shown    int         // events displayed (animates toward the target)
	gen      int         // debounce generation — only the latest quietMsg counts
	started  bool        // first event seen
	settling bool        // burst done; tick out to received, then reveal
	settle   probeSettle // defers the quit until the terminal's probe replies land
	quit     bool        // reveal + exit
}

func (m *checkInModel) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		readSignal(m.events),
		tea.Tick(checkInFirstWait, func(time.Time) tea.Msg { return checkInTimeoutMsg{} }),
	)
}

func (m *checkInModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if handled, cmd := m.settle.Observe(msg); handled {
		return m, cmd
	}
	switch msg := msg.(type) {
	case checkInEventMsg:
		if m.settling { // batch already decided — drain without counting
			return m, readSignal(m.events)
		}
		m.received++
		m.gen++
		g := m.gen
		cmds := []tea.Cmd{
			readSignal(m.events),
			tea.Tick(quietPeriod, func(time.Time) tea.Msg { return quietMsg{gen: g} }),
		}
		if !m.started {
			m.started = true
			cmds = append(cmds, tea.Tick(countTickInterval, func(time.Time) tea.Msg { return countTickMsg{} }))
		}
		return m, tea.Batch(cmds...)

	case countTickMsg:
		target := m.received
		if !m.settling && target > revealCap {
			target = revealCap
		}
		if m.shown < target {
			m.shown++
		}
		// Reveal once the counter has arrived: it hit the cap, or the burst is
		// done and the display has caught up to the total.
		if (!m.settling && m.received >= revealCap && m.shown >= revealCap) ||
			(m.settling && m.shown >= m.received) {
			m.quit = true
			return m, m.settle.Finish()
		}
		return m, tea.Tick(countTickInterval, func(time.Time) tea.Msg { return countTickMsg{} })

	case quietMsg:
		// Only the latest event's timer counts; a big batch caps out separately.
		if msg.gen == m.gen && !m.settling && m.received < revealCap {
			m.settling = true
			if m.shown >= m.received { // already caught up — reveal now
				m.quit = true
				return m, m.settle.Finish()
			}
		}
		return m, nil

	case checkInTimeoutMsg:
		if !m.started {
			m.quit = true
			return m, m.settle.Finish()
		}
		return m, nil // already counting; the cap/quiet governs the exit

	case checkInClosedMsg:
		m.quit = true
		return m, m.settle.Finish()

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			m.quit = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *checkInModel) View() tea.View {
	if !m.started {
		if m.quit { // timed out with no event — leave nothing behind
			return tea.NewView("")
		}
		return tea.NewView(m.spinner.View() + " " + mutedStyle.Render("Waiting for "+m.driver+" to check in with Anvil…") + "\n")
	}
	if !m.quit {
		// Still counting — just the live counter, celebration held back.
		return tea.NewView(m.spinner.View() + " " + mutedStyle.Render(m.countLabel()) + "\n")
	}

	// Arrived: counter settles into the celebration + link.
	var b strings.Builder
	b.WriteString(Success(m.countLabel()) + "\n")
	b.WriteString(Celebrate(m.driver+" is live and reporting to Anvil!") + "\n\n")
	b.WriteString(mutedStyle.Render("View your driver's event stream at:") + "\n")
	b.WriteString(mutedStyle.Render("→  ") + linkify(m.url) + "\n")
	return tea.NewView(b.String())
}

func (m *checkInModel) countLabel() string {
	if m.shown <= 1 {
		return "Initial event received…"
	}
	return fmt.Sprintf("%d events received…", m.shown)
}

// readSignal blocks for the next event pulse. A closed channel ends the stream.
func readSignal(ch <-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		if _, ok := <-ch; !ok {
			return checkInClosedMsg{}
		}
		return checkInEventMsg{}
	}
}

// RunCheckIn shows the "waiting → live counter → celebration" flow, driven by an
// events channel that pulses once per telemetry event. Interactive callers get
// the live TUI; non-interactive ones wait for the first event, then print a
// static celebration (or nothing on timeout).
func RunCheckIn(ctx context.Context, w io.Writer, interactive bool, driver, url string, events <-chan struct{}) {
	if !interactive {
		select {
		case _, ok := <-events:
			if !ok {
				return
			}
		case <-time.After(checkInFirstWait):
			return
		case <-ctx.Done():
			return
		}
		fmt.Fprintln(w, Success("Initial event received"))
		fmt.Fprintln(w, Celebrate(driver+" is live and reporting to Anvil!"))
		fmt.Fprintln(w)
		fmt.Fprintln(w, mutedStyle.Render("View your driver's event stream at:"))
		fmt.Fprintln(w, mutedStyle.Render("→  ")+linkify(url))
		return
	}
	m := &checkInModel{driver: driver, url: url, events: events, spinner: newSpinner()}
	p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithOutput(w))
	_, _ = p.Run()
}
