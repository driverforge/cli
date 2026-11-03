package ui

import (
	"bytes"
	"strings"
	"testing"
)

func TestDeviceLoginCardPlainNonTTY(t *testing.T) {
	var buf bytes.Buffer // not an *os.File → non-terminal → plain output
	out := DeviceLoginCard(&buf, "https://auth.driverforge.test/device", "", "bzD6ecLP")
	if !strings.Contains(out, "https://auth.driverforge.test/device") {
		t.Errorf("verification URL missing: %q", out)
	}
	if !strings.Contains(out, "enter this code:  bzD6ecLP") {
		t.Errorf("code to enter missing: %q", out)
	}
	if strings.Contains(out, "\x1b[") {
		t.Errorf("non-TTY output must not contain ANSI: %q", out)
	}
}

// With a complete verification URL the code is already in the link, so that is
// the link shown and the code is there to check, not to type.
func TestDeviceLoginCardCompleteURL(t *testing.T) {
	var buf bytes.Buffer
	complete := "https://auth.driverforge.test/device?user_code=bzD6ecLP"
	out := DeviceLoginCard(&buf, "https://auth.driverforge.test/device", complete, "bzD6ecLP")
	if !strings.Contains(out, complete) {
		t.Errorf("complete URL missing: %q", out)
	}
	if strings.Contains(out, "enter") {
		t.Errorf("must not ask the user to enter a pre-filled code: %q", out)
	}
	if !strings.Contains(out, "check it shows this code:  bzD6ecLP") {
		t.Errorf("code to check missing: %q", out)
	}
}

// On a terminal the login instructions render as a left-bar block: every line
// carries the accent bar, with no frame or title tab.
func TestDeviceLoginBlock(t *testing.T) {
	out := calloutBlock(colInfo, []string{
		blockRow(colInk, false, "To authorize driverforge, open this link in your browser:"),
		"",
		blockRow(colInfoBright, true, "    bzD6ecLP"),
	})
	for i, ln := range strings.Split(out, "\n") {
		if !strings.Contains(ln, "┃") {
			t.Errorf("line %d missing the accent bar: %q", i, ln)
		}
	}
	if strings.ContainsAny(out, "╭╮╰╯") {
		t.Errorf("block must not be framed: %q", out)
	}
}
