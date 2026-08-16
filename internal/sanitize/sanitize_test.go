package sanitize

import "testing"

func TestTerminal(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain text untouched", "installing driver v2", "installing driver v2"},
		{"unicode untouched", "Wohnzimmer Büro — ✓", "Wohnzimmer Büro — ✓"},
		{"newline and tab kept", "line1\n\tline2", "line1\n\tline2"},
		{"CSI color stripped", "\x1b[31mred alert\x1b[0m", "red alert"},
		{"cursor movement stripped", "\x1b[2Alook up here", "look up here"},
		{"screen clear stripped", "\x1b[2J\x1b[Hwiped", "wiped"},
		{"OSC title BEL-terminated", "\x1b]0;evil title\x07step", "step"},
		{"OSC hyperlink ST-terminated", "\x1b]8;;https://evil.test\x1b\\click\x1b]8;;\x1b\\", "click"},
		{"OSC clipboard write", "\x1b]52;c;ZXZpbA==\x07done", "done"},
		{"DCS stripped", "\x1bPq payload\x1b\\after", "after"},
		{"two-char escape stripped", "\x1b(Bcharset", "charset"},
		{"single-byte CSI (C1)", "31mred", "red"},
		{"other C1 controls dropped", "a\u0085b\u008fc", "abc"},
		{"C0 controls dropped", "a\x00b\x08c\x0bd", "abcd"},
		{"CRLF becomes LF", "line1\r\nline2", "line1\nline2"},
		{"bare trailing ESC", "step\x1b", "step"},
		{"unterminated OSC drops the rest", "before\x1b]0;never terminated", "before"},
		{"unterminated CSI drops the rest", "before\x1b[38;5;", "before"},
		{"nested-looking sequences", "\x1b[1m\x1b[31mbold red\x1b[0m ok", "bold red ok"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Terminal(tc.in); got != tc.want {
				t.Errorf("Terminal(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The fast path must not allocate or alter clean strings.
func TestTerminalFastPathIdentity(t *testing.T) {
	in := "a perfectly ordinary step description"
	if got := Terminal(in); got != in {
		t.Errorf("clean string altered: %q", got)
	}
}
