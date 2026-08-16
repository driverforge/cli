// Package sanitize strips terminal control sequences from remote-sourced
// strings. Agent and server responses are rendered into the user's terminal
// (step lines, hints, error messages, device and project names); a hostile or
// compromised peer could embed ANSI/OSC sequences — cursor movement, screen
// clearing, title/clipboard writes, fake OSC-8 hyperlinks — to spoof CLI
// output. Scrubbing happens at ingestion (where the JSON is decoded), so every
// render path downstream is covered.
package sanitize

import "strings"

// Terminal returns s with terminal control content removed: ESC-introduced
// sequences (CSI, OSC, DCS/SOS/PM/APC, two-character escapes) are dropped
// whole; other C0 controls (except \n and \t), DEL, and C1 controls are
// dropped individually. Printable text — including all non-control Unicode —
// passes through unchanged.
func Terminal(s string) string {
	if !strings.ContainsFunc(s, isControl) {
		return s // fast path: nothing to scrub
	}
	rs := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == 0x1b: // ESC: skip the whole sequence
			i = skipEscape(rs, i)
		case r == 0x9b: // single-byte CSI (C1): same as ESC [
			i = skipCSIBody(rs, i)
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f: // remaining C0 + DEL (\r included: CRLF → LF)
		case r >= 0x80 && r <= 0x9f: // remaining C1 controls
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isControl(r rune) bool {
	return (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

// skipEscape returns the index of the last rune of the escape sequence
// starting at rs[i] (an ESC), so the caller's loop increment moves past it.
func skipEscape(rs []rune, i int) int {
	if i+1 >= len(rs) {
		return i // trailing bare ESC
	}
	switch rs[i+1] {
	case '[': // CSI: parameters/intermediates then a final byte 0x40-0x7e
		return skipCSIBody(rs, i+1)
	case ']', 'P', 'X', '^', '_': // OSC / DCS / SOS / PM / APC: until BEL or ST (ESC \)
		for j := i + 2; j < len(rs); j++ {
			if rs[j] == 0x07 || rs[j] == 0x9c { // BEL or single-byte ST
				return j
			}
			if rs[j] == 0x1b && j+1 < len(rs) && rs[j+1] == '\\' { // ESC \ (ST)
				return j + 1
			}
		}
		return len(rs) - 1 // unterminated: drop the rest
	default:
		// nF-style escapes: zero or more intermediate bytes (0x20-0x2f), then
		// one final byte — covers charset selection (ESC ( B) and the
		// two-character escapes (ESC M etc.) alike.
		j := i + 1
		for j < len(rs) && rs[j] >= 0x20 && rs[j] <= 0x2f {
			j++
		}
		if j < len(rs) {
			return j
		}
		return len(rs) - 1
	}
}

// skipCSIBody consumes a CSI sequence's body starting after the introducer at
// rs[i]: parameter and intermediate bytes (0x20-0x3f) then one final byte
// (0x40-0x7e).
func skipCSIBody(rs []rune, i int) int {
	for j := i + 1; j < len(rs); j++ {
		if rs[j] >= 0x40 && rs[j] <= 0x7e {
			return j // final byte ends the sequence
		}
		if rs[j] < 0x20 || rs[j] > 0x3f {
			return j - 1 // malformed: stop consuming, rescan this rune
		}
	}
	return len(rs) - 1 // unterminated: drop the rest
}
