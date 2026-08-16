package ui

import (
	"fmt"
	"io"
	"os"

	"github.com/charmbracelet/colorprofile"
)

// Fprintln writes the (possibly styled) values to w, downsampling ANSI colour to
// w's detected profile and honouring NO_COLOR, then a trailing newline.
//
// It is the lipgloss v2 replacement for the per-writer Renderer: build styled
// strings with the global styles, then emit them through here so a pipe, a dumb
// terminal, or NO_COLOR all stay clean.
func Fprintln(w io.Writer, a ...any) (int, error) {
	return fmt.Fprintln(colorprofile.NewWriter(w, os.Environ()), a...)
}
