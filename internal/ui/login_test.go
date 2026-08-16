package ui

import (
	"bytes"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestDeviceLoginCardPlainNonTTY(t *testing.T) {
	var buf bytes.Buffer // not an *os.File → non-terminal → plain output
	out := DeviceLoginCard(&buf, "https://auth.driverforge.test/device", "bzD6ecLP")
	if !strings.Contains(out, "https://auth.driverforge.test/device") {
		t.Errorf("verification URL missing: %q", out)
	}
	if !strings.Contains(out, "bzD6ecLP") {
		t.Errorf("user code missing: %q", out)
	}
	if strings.Contains(out, "\x1b[") {
		t.Errorf("non-TTY output must not contain ANSI: %q", out)
	}
}

// The login card's borders must line up: every rendered line shares one display
// width, including the centred code box (a nested bordered block).
func TestDeviceLoginCardAligned(t *testing.T) {
	codeBox := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 3).Render("bzD6ecLP")
	out := card(colInfo, "Sign in", []string{
		textRow(colInk, false, "To authorize driverforge, open this link in your browser:"),
		"",
		centerRow(codeBox),
	})

	lines := strings.Split(out, "\n")
	if len(lines) < 3 {
		t.Fatalf("card too short: %q", out)
	}
	want := lipgloss.Width(lines[0])
	for i, ln := range lines {
		if w := lipgloss.Width(ln); w != want {
			t.Errorf("line %d width = %d, want %d: %q", i, w, want, ln)
		}
	}
	if !strings.Contains(out, "SIGN IN") {
		t.Errorf("title tab missing: %q", out)
	}
}
