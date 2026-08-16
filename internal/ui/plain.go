package ui

import (
	"fmt"
	"io"

	"github.com/driverforge/cli/pkg/c4z"
)

// PlainProgress returns a c4z progress callback that streams plain lines to w,
// for non-interactive output (CI, pipes, --no-tui).
func PlainProgress(w io.Writer, verbose bool) func(c4z.Event) {
	return func(e c4z.Event) {
		switch e.Status {
		case c4z.StepStart:
			if verbose {
				fmt.Fprintf(w, "==> %s...\n", StepLabel(e.Step))
			}
		case c4z.StepOK:
			if e.Step == c4z.StepDone {
				return
			}
			suffix := ""
			if e.Msg != "" {
				suffix = " " + e.Msg
			}
			fmt.Fprintf(w, "  ✓ %s%s\n", StepLabel(e.Step), suffix)
		case c4z.StepSkip:
			if verbose {
				fmt.Fprintf(w, "  – %s (skipped)\n", StepLabel(e.Step))
			}
		case c4z.StepFail:
			fmt.Fprintf(w, "  ✗ %s: %v\n", StepLabel(e.Step), e.Err)
		}
	}
}

// PrintResultPlain writes the final build summary as plain text.
func PrintResultPlain(w io.Writer, r c4z.Result) {
	fmt.Fprintf(w, "\n✓ build complete\n")
	fmt.Fprintf(w, "  driver    %s\n", r.Name)
	fmt.Fprintf(w, "  version   %s\n", versionLabel(r))
	fmt.Fprintf(w, "  artifact  %s\n", r.C4zPath)
	if r.Encrypted {
		fmt.Fprintf(w, "  encrypted yes\n")
	}
	if r.UnpackedDir != "" {
		fmt.Fprintf(w, "  unpacked  %s\n", r.UnpackedDir)
	}
	if r.SourceMapPath != "" {
		fmt.Fprintf(w, "  sourcemap %s\n", r.SourceMapPath)
	}
}
