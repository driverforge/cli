package telemetryread

import (
	"encoding/json"
	"unicode/utf8"
)

// DefaultMaxPayloadBytes is the default per-entry cap on a log's message or an
// error's or event's payload.
const DefaultMaxPayloadBytes = 65536

// Truncate caps the entry's message (a log) or payload (an error or event) at
// limit bytes. Over the cap, the value becomes a string holding the prefix, cut
// on a UTF-8 boundary, and Truncated records both sizes. A limit of 0 means no
// cap. It reports whether anything was cut.
func Truncate(e *Entry, limit int) bool {
	if limit <= 0 {
		return false
	}
	if e.Type == KindLog {
		if e.Message == nil || len(*e.Message) <= limit {
			return false
		}
		cut := prefix(*e.Message, limit)
		e.Truncated = &Truncated{Field: "message", TotalBytes: len(*e.Message), ReturnedBytes: len(cut)}
		e.Message = &cut
		return true
	}
	if len(e.Payload) <= limit {
		return false
	}
	cut := prefix(string(e.Payload), limit)
	encoded, _ := json.Marshal(cut)
	e.Truncated = &Truncated{Field: "payload", TotalBytes: len(e.Payload), ReturnedBytes: len(cut)}
	e.Payload = encoded
	return true
}

// prefix is the longest prefix of s within limit bytes that ends on a UTF-8
// boundary.
func prefix(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
