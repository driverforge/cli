package ui

import (
	"errors"
	"fmt"
	"image/color"
	"io"
	"os"
	"regexp"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/muesli/termenv"
	"golang.org/x/term"

	"github.com/driverforge/cli/internal/clierr"
)

// Error-rendering palette. The border + title carry the accent; the message is
// clean and readable; the tip pops in a brighter tone within the same card.
var (
	colAmber = lipgloss.Color("#D9A441") // accent: border for guidance user errors
	colTip   = lipgloss.Color("#F4D58D") // brighter gold — the actionable tip
	colInk   = lipgloss.Color("#F2ECE0") // warm off-white — primary message text
	colRed   = lipgloss.Color("#E5534B") // accent: border for crashes + fatal failures
	colDim   = lipgloss.Color("#9B948A") // dimmed technical detail
	colBack  = lipgloss.Color("#1C1B19") // dark ink for text on an accent fill
)

// RenderUserError writes an expected, user-facing error to w. On a terminal it
// renders a left-bordered block accented in amber: the message, and — grouped
// beneath it — a brighter "💡" tip. In a pipe/CI it writes plain "error:" /
// "tip:" lines.
//
// It reads message/hint from a clierr.UserError when present, and otherwise
// falls back to splitting a trailing "(run `driverforge …`)" hint off the message
// so hint-carrying errors from earlier Driverforge CLI versions render nicely too.
func RenderUserError(w io.Writer, err error) {
	if err == nil {
		return
	}
	_, msg, hint := dissect(err)
	if msg == "" && hint == "" {
		return // silent error (clierr.Silent): exit non-zero, render nothing
	}

	if !isTerminalWriter(w) {
		fmt.Fprintln(w, "error:", msg)
		if hint != "" {
			fmt.Fprintln(w, "tip:", hint)
		}
		return
	}

	// Guidance (a fixable precondition) gets the softer amber bar; a fatal
	// failure (an operation that failed outright, e.g. a build) gets the red
	// error bar — so it reads as a failure, not a warning.
	accent := colAmber
	if clierr.IsFatal(err) {
		accent = colRed
	}
	rows := []string{blockRow(colInk, true, msg)}
	if hint != "" {
		rows = append(rows, "", blockRow(colTip, true, " 💡  "+linkify(hint)))
	}
	fmt.Fprintln(w) // breathing room from the command line above
	Fprintln(w, calloutBlock(accent, rows))
}

// RenderCrash writes an unexpected error (a bug) to w. On a terminal it renders
// a red left-bordered block with an apologetic line and the underlying error as
// dimmed technical detail; in a pipe/CI it writes a plain "error:" line. The
// offer to report it is handled separately by errreport.
func RenderCrash(w io.Writer, err error) {
	if err == nil {
		return
	}
	if !isTerminalWriter(w) {
		fmt.Fprintln(w, "error:", err.Error())
		return
	}

	rows := []string{
		blockRow(colInk, true, "Something went wrong on our end — sorry about that."),
		"",
		blockRow(colDim, false, err.Error()),
	}
	fmt.Fprintln(w) // breathing room from the command line above
	Fprintln(w, calloutBlock(colRed, rows))
}

// card frames pre-rendered content rows in a rounded box whose top border
// carries an uppercase title tab: ╭─ TITLE ─────────╮. Empty rows become spacer
// rows (top and bottom breathing room are added automatically). The inner width
// is measured from a rendered spacer so the borders always line up with the
// body, regardless of padding/emoji width quirks.
func card(accent color.Color, title string, rows []string) string {
	bar := lipgloss.NewStyle().Foreground(accent)
	label := lipgloss.NewStyle().Bold(true).Foreground(colBack).Background(accent)

	spacer := textRow(colInk, false, "")
	width := lipgloss.Width(spacer)

	tab := label.Render(" " + strings.ToUpper(title) + " ")
	dashes := width - 1 - lipgloss.Width(tab)
	if dashes < 0 {
		dashes = 0
	}
	top := bar.Render("╭─") + tab + bar.Render(strings.Repeat("─", dashes)+"╮")
	bottom := bar.Render("╰" + strings.Repeat("─", width) + "╯")

	lines := []string{spacer} // top breathing room
	for _, row := range rows {
		if row == "" {
			lines = append(lines, spacer)
			continue
		}
		lines = append(lines, strings.Split(row, "\n")...)
	}
	lines = append(lines, spacer) // bottom breathing room

	var b strings.Builder
	b.WriteString(top + "\n")
	for _, line := range lines {
		b.WriteString(bar.Render("│") + line + bar.Render("│") + "\n")
	}
	b.WriteString(bottom)
	return b.String()
}

// calloutBlock renders content rows as a left-bordered block: each line prefixed
// with a thick accent-coloured bar (┃). Empty rows are the bar alone. Unlike a
// framed card there is no top/bottom border and no title — the accent bar carries
// the whole signal.
func calloutBlock(accent color.Color, rows []string) string {
	bar := lipgloss.NewStyle().Foreground(accent).Render("┃")
	var lines []string
	for _, row := range rows {
		for _, line := range strings.Split(row, "\n") {
			if line == "" {
				lines = append(lines, bar)
			} else {
				lines = append(lines, bar+" "+line)
			}
		}
	}
	return strings.Join(lines, "\n")
}

// blockRow styles and wraps a callout line to the block's content width. No left
// padding: the bar + a space in calloutBlock supplies the indent.
func blockRow(fg color.Color, bold bool, s string) string {
	return lipgloss.NewStyle().Foreground(fg).Bold(bold).Width(cardWidth()).Render(s)
}

// textRow renders s into a card-width row (wrapping if needed), in the given
// colour, optionally bold. Used by the framed card (login panel).
func textRow(fg color.Color, bold bool, s string) string {
	return lipgloss.NewStyle().Foreground(fg).Bold(bold).Width(cardWidth()).Padding(0, 2).Render(s)
}

var urlRe = regexp.MustCompile(`https?://[^\s]+`)

// linkify wraps any URL in s as an OSC-8 hyperlink, so terminals that support it
// make the docs link clickable. The visible text is unchanged (others just see
// the URL), and the escapes are zero-width, so card layout is unaffected.
func linkify(s string) string {
	return urlRe.ReplaceAllStringFunc(s, func(u string) string {
		return termenv.Hyperlink(u, u)
	})
}

// dissect pulls a title, message and hint out of err. A clierr.UserError
// supplies them directly (defaulting the title to "Heads up"); otherwise the
// message is the error text with any trailing "(run `driverforge …`)" hint split off.
func dissect(err error) (title, msg, hint string) {
	var ue *clierr.UserError
	if errors.As(err, &ue) {
		title = ue.Title
		if title == "" {
			title = "Heads up"
		}
		return title, ue.Message, ue.Hint
	}
	msg, hint = splitHint(err.Error())
	return "Heads up", msg, hint
}

// isTerminalWriter reports whether w is a terminal (so we should colourise).
func isTerminalWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// splitHint separates a trailing actionable "(run `driverforge …`)" hint from the
// headline so it can be pulled out as a tip. Returns (headline, hint).
func splitHint(msg string) (headline, hint string) {
	i := strings.LastIndex(msg, "(run `driverforge")
	if i < 0 {
		return msg, ""
	}
	headline = strings.TrimSpace(msg[:i])
	hint = strings.TrimSpace(msg[i:])
	hint = strings.TrimSuffix(strings.TrimPrefix(hint, "("), ")")
	return headline, hint
}
