package ui

import (
	"context"
	"fmt"
	"io"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// ReviewFile is one file's proposed change for the tabbed reviewer. Old is nil
// for a newly-created file.
type ReviewFile struct {
	Path string
	Old  []byte
	New  []byte
}

// ReviewChanges shows a Claude-Code-style tabbed diff viewer: page through each
// file with ←/→, scroll the diff with ↑/↓, then accept or cancel. It returns
// whether the user accepted the changes into the plan (nothing is written here —
// the caller confirms and applies later). It is interactive-only; its sole
// caller (driverforge init) rejects a non-interactive terminal up front.
func ReviewChanges(ctx context.Context, w io.Writer, files []ReviewFile) (bool, error) {
	if len(files) == 0 {
		return false, nil
	}
	if err := checkPrompt("Review these changes"); err != nil {
		return false, err
	}

	m := newReviewModel(files)
	p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithOutput(w))
	res, err := p.Run()
	if err != nil {
		return false, ExpectedIfAborted(err)
	}
	return res.(*reviewModel).approved, nil
}

var (
	reviewTabActive   = lipgloss.NewStyle().Bold(true).Foreground(colInfoBright).Underline(true)
	reviewTabInactive = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	reviewArrow       = lipgloss.NewStyle().Foreground(colInfo)
	reviewPanel       = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colInfo).Padding(0, 1)
	reviewFooter      = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
)

// reviewMinHeight keeps the diff panel usable on short terminals.
const reviewMinHeight = 8

type reviewModel struct {
	files  []ReviewFile
	bodies []string // pre-rendered diff body per file
	cur    int
	vp     viewport.Model
	width  int
	ready  bool

	approved bool
}

func newReviewModel(files []ReviewFile) *reviewModel {
	bodies := make([]string, len(files))
	for i, f := range files {
		bodies[i] = strings.TrimRight(RenderFileDiffBody(f.Old, f.New), "\n")
	}
	return &reviewModel{files: files, bodies: bodies, vp: viewport.New()}
}

func (m *reviewModel) Init() tea.Cmd { return nil }

func (m *reviewModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		// Fit the panel to the window: width-6 leaves room for the border and
		// padding, and grows with the terminal. The old max(40,…) floor forced
		// 40 even under ~46 cols, overflowing the window.
		innerW := max(1, msg.Width-6)
		// Occupy about a third of the terminal (a compact panel, not a takeover),
		// with a floor so it stays usable in short windows.
		vpH := max(reviewMinHeight, msg.Height/3)
		m.vp.SetWidth(innerW)
		m.vp.SetHeight(vpH)
		m.vp.SetContent(m.bodies[m.cur])
		m.ready = true
		return m, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "left", "h":
			m.move(-1)
			return m, nil
		case "right", "l", "tab":
			m.move(1)
			return m, nil
		case "a", "enter":
			m.approved = true
			return m, tea.Quit
		case "q", "esc":
			m.approved = false
			return m, tea.Quit
		case "ctrl+c":
			m.approved = false
			return m, tea.Quit // ExpectedIfAborted maps interrupt to "Cancelled"
		}
		// Anything else (↑/↓/pgup/pgdn/…) scrolls the diff.
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd
	}
	return m, nil
}

// move changes the active file tab and resets the scroll position.
func (m *reviewModel) move(delta int) {
	n := len(m.files)
	m.cur = (m.cur + delta + n) % n
	m.vp.SetContent(m.bodies[m.cur])
	m.vp.GotoTop()
}

func (m *reviewModel) View() tea.View {
	if !m.ready {
		return tea.NewView("Loading review…\n")
	}

	var b strings.Builder
	b.WriteString(Heading(fmt.Sprintf("Review changes — %d file%s", len(m.files), pluralS(len(m.files)))))
	b.WriteString("\n\n")
	b.WriteString(m.tabBar())
	b.WriteString("\n\n")
	b.WriteString(reviewPanel.Width(m.vp.Width()).Render(m.vp.View()))
	b.WriteString("\n")
	b.WriteString(reviewFooter.Render(fmt.Sprintf("file %d/%d · ←/→ files · ↑/↓ scroll · a accept · q cancel", m.cur+1, len(m.files))))
	b.WriteByte('\n')
	return tea.NewView(b.String())
}

// tabBar renders the file tabs with ◀/▶ affordances and the active one styled.
func (m *reviewModel) tabBar() string {
	var parts []string
	if m.cur > 0 {
		parts = append(parts, reviewArrow.Render("◀"))
	} else {
		parts = append(parts, " ")
	}
	for i, f := range m.files {
		name := tabName(f.Path)
		if i == m.cur {
			parts = append(parts, reviewTabActive.Render(name))
		} else {
			parts = append(parts, reviewTabInactive.Render(name))
		}
	}
	if m.cur < len(m.files)-1 {
		parts = append(parts, reviewArrow.Render("▶"))
	} else {
		parts = append(parts, " ")
	}
	return strings.Join(parts, "  ")
}

// tabName shortens a path to its base for the tab label, keeping a hint of the
// parent dir for files like vendor/driverforge-sdk.lua.
func tabName(path string) string {
	parts := strings.Split(filepathToSlash(path), "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "/" + parts[len(parts)-1]
	}
	return path
}

func filepathToSlash(p string) string { return strings.ReplaceAll(p, "\\", "/") }

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
