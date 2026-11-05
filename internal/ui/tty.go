// Package ui renders everything the CLI puts on a terminal: status lines, error
// and crash cards, diffs, pickers, forms, and the live progress frames that
// deploy, sync and init run under.
//
// Every surface exists twice. Interactive reports which to use, and the Bubble
// Tea models here are paired with a plain renderer that emits the same
// information as ordinary lines — because the CLI has to be as usable piped into
// a file or running in CI as it is on a terminal. Commands ask Interactive once
// and pick a path; they never branch on it further down.
//
// Nothing here talks to the network or the filesystem. Callers pass in finished
// data and, where a surface reports progress, a channel or callback to drive it,
// so rendering can be tested without standing up the thing being rendered.
package ui

import (
	"fmt"
	"os"

	"golang.org/x/term"

	"github.com/driverforge/cli/internal/agentmode"
	"github.com/driverforge/cli/internal/clierr"
)

// Interactive reports whether to drive the Bubble Tea TUI. It is false when
// --no-tui is set, under CI, when output is structured (a coding agent, or
// DRIVERFORGE_OUTPUT=json), or when stdout is not a terminal (piped or
// redirected), in which case callers should use the plain renderer.
func Interactive(noTUI bool) bool {
	if noTUI || agentmode.Structured() {
		return false
	}
	if os.Getenv("CI") != "" {
		return false
	}
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// promptWidth is the target inner width for interactive prompts. Forms are pinned
// to this so titles, descriptions and (crucially) confirm buttons land at the
// same place from one prompt to the next, instead of huh sizing each card to its
// own content. It mirrors the error card's 70-column inner width.
const promptWidth = 70

// promptWidthMin keeps prompts usable on very narrow terminals.
const promptWidthMin = 24

// formWidth returns the width to pin a prompt form to: promptWidth, clamped down
// to fit a narrow terminal (leaving a small right margin). Returns promptWidth
// when the terminal size can't be determined (e.g. not a TTY).
func formWidth() int {
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w <= 0 {
		return promptWidth
	}
	avail := w - 2 // small right margin
	if avail >= promptWidth {
		return promptWidth
	}
	if avail < promptWidthMin {
		return promptWidthMin
	}
	return avail
}

// cardWidth returns the content width for a left-bar callout block
// (RenderUserError, crash, device-login): a natural 70 columns, clamped to fit a
// narrow terminal (the "┃ " bar prefix is outside this width). Blocks render to
// stderr (errors) or stdout (login), so it measures whichever is a terminal.
// Falls back to the full width when neither is a TTY.
func cardWidth() int {
	w, ok := terminalWidth()
	if !ok {
		return promptWidth
	}
	avail := w - 2 // the "┃ " bar prefix
	switch {
	case avail >= promptWidth:
		return promptWidth
	case avail < promptWidthMin:
		return promptWidthMin
	default:
		return avail
	}
}

// frameWidth returns the wrap width for run-frame content: the full terminal
// width minus the two cells the "┃ " bar prefix occupies. The frame must
// hard-wrap to SOME width — it is a live inline region, and a line the
// terminal soft-wraps throws off the repaint height math — but unlike a card
// it claims the whole terminal, so long values (artifact paths, log lines)
// stay selectable in one piece. Falls back to the card width when not a TTY.
func frameWidth() int {
	w, ok := terminalWidth()
	if !ok {
		return promptWidth
	}
	avail := w - 2 // the "┃ " bar prefix
	if avail < promptWidthMin {
		return promptWidthMin
	}
	return avail
}

// terminalWidth returns the controlling terminal's column count. It tries stderr
// then stdout (either is the terminal in practice), so it works for cards on
// either stream. ok is false when neither is a TTY.
func terminalWidth() (int, bool) {
	for _, fd := range []int{int(os.Stderr.Fd()), int(os.Stdout.Fd())} {
		if w, _, err := term.GetSize(fd); err == nil && w > 0 {
			return w, true
		}
	}
	return 0, false
}

// checkPrompt refuses a prompt when output is structured: nobody is at a
// terminal to answer it, and a form would corrupt the output. The error names
// the question, so the caller can see which answer to pass as a flag.
func checkPrompt(title string) error {
	if !agentmode.Structured() {
		return nil
	}
	return clierr.WithCode(clierr.UserT("Input required",
		fmt.Sprintf("%q needs an answer, and prompts are off for structured output", title),
		"pass the answer as a flag or argument (see --help)"), clierr.CodeInputRequired)
}
