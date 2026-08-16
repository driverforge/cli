package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// Diff colours: green additions, red deletions, dim context — a familiar
// review palette that degrades gracefully on NO_COLOR / non-TTY output.
var (
	diffAddStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	diffDelStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	diffCtxStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	diffHeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(colInfo)
	diffHunkStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
)

const (
	diffContext      = 3  // unchanged lines shown around each change
	createPreviewMax = 40 // new files larger than this are summarised, not dumped
)

// RenderFileDiff renders a coloured, hunked diff for a single file change. A nil
// old means a newly-created file (rendered as additions, or summarised when
// large). The header marks created (＋) vs modified (～).
func RenderFileDiff(path string, old, new []byte) string {
	var b strings.Builder
	if old == nil {
		newLines := splitLines(string(new))
		b.WriteString(diffHeaderStyle.Render("＋ " + path))
		b.WriteString(diffCtxStyle.Render(fmt.Sprintf("  (new file, %d lines)", len(newLines))))
		b.WriteByte('\n')
	} else {
		b.WriteString(diffHeaderStyle.Render("～ " + path))
		b.WriteByte('\n')
	}
	b.WriteString(RenderFileDiffBody(old, new))
	return b.String()
}

// RenderFileDiffBody renders just the coloured diff lines (no path header) — used
// by the tabbed reviewer, which titles each file via its tab instead.
func RenderFileDiffBody(old, new []byte) string {
	if old == nil {
		newLines := splitLines(string(new))
		if len(newLines) > createPreviewMax {
			return diffCtxStyle.Render("  … contents omitted") + "\n"
		}
		var b strings.Builder
		for _, ln := range newLines {
			b.WriteString(diffAddStyle.Render("+ " + ln))
			b.WriteByte('\n')
		}
		return b.String()
	}
	ops := lineDiff(splitLines(string(old)), splitLines(string(new)))
	return renderHunks(ops)
}

// op is a single line operation in a diff.
type op struct {
	kind byte // ' ' equal, '+' add, '-' del
	text string
}

// splitLines splits s into lines, dropping a single trailing newline so a file
// ending in "\n" doesn't show a phantom empty final line.
func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// lineDiff produces a line-level diff of a→b via an LCS table. Inputs here are
// small (entry/manifest/config files), so the O(n·m) table is fine.
func lineDiff(a, b []string) []op {
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	var ops []op
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, op{' ', a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, op{'-', a[i]})
			i++
		default:
			ops = append(ops, op{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, op{'-', a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, op{'+', b[j]})
	}
	return ops
}

// renderHunks prints changes with up to diffContext lines of surrounding
// context, collapsing larger unchanged gaps with a "⋯" separator.
func renderHunks(ops []op) string {
	keep := make([]bool, len(ops))
	for i, o := range ops {
		if o.kind == ' ' {
			continue
		}
		for k := i - diffContext; k <= i+diffContext; k++ {
			if k >= 0 && k < len(ops) {
				keep[k] = true
			}
		}
	}

	var b strings.Builder
	gap := false
	for i, o := range ops {
		if !keep[i] {
			gap = true
			continue
		}
		if gap {
			b.WriteString(diffHunkStyle.Render("  ⋯"))
			b.WriteByte('\n')
			gap = false
		}
		switch o.kind {
		case '+':
			b.WriteString(diffAddStyle.Render("+ " + o.text))
		case '-':
			b.WriteString(diffDelStyle.Render("- " + o.text))
		default:
			b.WriteString(diffCtxStyle.Render("  " + o.text))
		}
		b.WriteByte('\n')
	}
	return b.String()
}
