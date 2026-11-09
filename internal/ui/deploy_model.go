package ui

import (
	"strings"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/driverforge/cli/internal/agent"
)

// DeployEventMsg carries a deploy progress event into the program.
type DeployEventMsg agent.DeployEvent

// DeployOutcome is the terminal result of a deploy.
type DeployOutcome int

const (
	DeployOK   DeployOutcome = iota
	DeployWarn               // deployed, but unconfirmed or init reported errors
	DeployFail
)

// DeployDoneMsg signals the deploy finished. Step resolves the current spinner
// line; Detail/ReportURL carry an init error or hint for the summary.
type DeployDoneMsg struct {
	State     agent.DeployState
	Step      string
	Version   string
	Detail    string
	ReportURL string
	Outcome   DeployOutcome
	Err       error
}

type lineStatus int

const (
	lineSpinning lineStatus = iota
	lineOK
	lineWarn
	lineFail
)

type deployLine struct {
	text   string
	status lineStatus
}

// DeployModel renders a live, multi-step deploy. The agent streams one state per
// line; each new non-terminal state ticks the previous line ✓ and spins the
// current. The terminal event resolves the current line to ✓/✗/⚠ and prints the
// summary.
type DeployModel struct {
	action    string
	device    string
	steps     []*deployLine
	spinner   spinner.Model
	settle    probeSettle // defers the quit until the terminal's probe replies land
	done      bool
	outcome   DeployOutcome
	termState agent.DeployState
	version   string
	detail    string
	reportURL string
	err       error
}

// NewDeployModel creates a deploy progress model. When action is non-empty it
// heads the steps with "🚀 <action> <device>"; when empty (the deploy path prints
// its own heading above the resolve/connect steps) the header is omitted.
func NewDeployModel(action, device string) *DeployModel {
	return &DeployModel{action: action, device: device, spinner: newSpinner()}
}

// DeployHeading renders the "🚀 <action> <device>" title — the action prominent,
// the target device subdued — that heads a deploy/install/upgrade's steps.
func DeployHeading(action, device string) string {
	return titleStyle.Render("🚀 "+action) + " " + mutedStyle.Render(device)
}

func (m *DeployModel) Init() tea.Cmd { return m.spinner.Tick }

func (m *DeployModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if handled, cmd := m.settle.Observe(msg); handled {
		return m, cmd
	}
	switch msg := msg.(type) {
	case DeployEventMsg:
		m.apply(agent.DeployEvent(msg))
		return m, nil
	case DeployDoneMsg:
		m.done = true
		m.outcome = msg.Outcome
		m.termState = msg.State
		m.version = msg.Version
		m.detail = msg.Detail
		m.reportURL = msg.ReportURL
		m.err = msg.Err
		m.resolveLast(msg.Step, msg.Outcome)
		return m, m.settle.Finish()
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			// The sentinel makes ExpectedIfAborted map this to a friendly
			// "Cancelled" card instead of a reportable crash.
			m.err = tea.ErrInterrupted
			m.outcome = DeployFail
			m.done = true
			m.resolveLast("", DeployFail)
			return m, tea.Quit
		}
	}
	return m, nil
}

// apply advances the step list: a new non-terminal state completes the current
// spinner line and starts a new one. Terminal events are resolved by DeployDoneMsg.
func (m *DeployModel) apply(e agent.DeployEvent) {
	if e.Terminal {
		return
	}
	if n := len(m.steps); n > 0 && m.steps[n-1].status == lineSpinning {
		m.steps[n-1].status = lineOK
	}
	m.steps = append(m.steps, &deployLine{text: e.Step, status: lineSpinning})
}

func (m *DeployModel) resolveLast(step string, outcome DeployOutcome) {
	st := lineOK
	switch outcome {
	case DeployWarn:
		st = lineWarn
	case DeployFail:
		st = lineFail
	}
	if n := len(m.steps); n > 0 {
		if step != "" {
			m.steps[n-1].text = step
		}
		m.steps[n-1].status = st
	} else if step != "" {
		m.steps = append(m.steps, &deployLine{text: step, status: st})
	}
}

func (m *DeployModel) View() tea.View {
	var b strings.Builder
	if m.action != "" {
		b.WriteString(DeployHeading(m.action, m.device) + "\n\n")
	}
	for _, ln := range m.steps {
		b.WriteString(m.renderLine(ln) + "\n")
	}
	if m.done {
		b.WriteString(m.summary())
	}
	return tea.NewView(b.String())
}

func (m *DeployModel) renderLine(ln *deployLine) string {
	var icon string
	switch ln.status {
	case lineSpinning:
		icon = m.spinner.View()
	case lineOK:
		icon = okStyle.Render("✓")
	case lineWarn:
		icon = warnStyle.Render("⚠")
	case lineFail:
		icon = errStyle.Render("✗")
	}
	return icon + " " + ln.text
}

func (m *DeployModel) summary() string {
	switch m.outcome {
	case DeployOK:
		return "\n" + deploySuccessBlock(m.version, m.device) + "\n"
	case DeployWarn:
		var b strings.Builder
		b.WriteString("\n")
		if m.detail != "" {
			b.WriteString(warnStyle.Render("⚠ "+m.detail) + "\n")
		}
		if m.reportURL != "" {
			b.WriteString(mutedStyle.Render("  Deployment report:") + "\n")
			b.WriteString(mutedStyle.Render("  "+linkify(m.reportURL)) + "\n")
		}
		// confirmed_with_errors: the driver is running, so still report the deploy.
		if m.termState == agent.StateConfirmedWithErrors {
			b.WriteString("\n" + deploySuccessBlock(m.version, m.device) + "\n")
		}
		return b.String()
	default: // DeployFail → the command surfaces the error as its own card
		return ""
	}
}

// deploySuccessBlock renders the deploy result as a green left-bar block, mirroring
// the build-complete card: a mandatory headline carrying the version, then the
// controller it landed on as context.
func deploySuccessBlock(version, controller string) string {
	suffix := ""
	if version != "" {
		suffix = "(version " + version + ")"
	}
	var rows [][2]string
	if controller != "" {
		rows = append(rows, [2]string{"controller", controller})
	}
	return SuccessBlock("deploy complete", suffix, rows)
}

// Err / Outcome / Version / TermState expose the result after the program exits.
func (m *DeployModel) Err() error                   { return m.err }
func (m *DeployModel) Outcome() DeployOutcome       { return m.outcome }
func (m *DeployModel) Version() string              { return m.version }
func (m *DeployModel) TermState() agent.DeployState { return m.termState }

func versionDeployedLabel(v string) string {
	if v == "" {
		return "deployed"
	}
	return "Version " + v + " deployed"
}
