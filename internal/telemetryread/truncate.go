package telemetryread

import (
	"encoding/json"
	"unicode/utf8"
)

// DefaultMaxPayloadBytes is the default per-entry cap on a log's message and
// on any entry's payload.
const DefaultMaxPayloadBytes = 65536

// Truncate caps a log's message, and any entry's payload, at limit bytes each.
// Over the cap, a message keeps its prefix and a payload becomes a string
// holding its prefix, both cut on a UTF-8 boundary. Truncated records the
// sizes of the field cut, and when a log loses both, of the payload: the
// structured data is what a reader must not reason from in part. A limit of 0
// means no cap. It reports whether anything was cut.
func Truncate(e *Entry, limit int) bool {
	if limit <= 0 {
		return false
	}
	if e.Type == KindLog && e.Message != nil && len(*e.Message) > limit {
		cut := prefix(*e.Message, limit)
		e.Truncated = &Truncated{Field: "message", TotalBytes: len(*e.Message), ReturnedBytes: len(cut)}
		e.Message = &cut
	}
	if len(e.Payload) > limit && string(e.Payload) != "null" {
		cut := prefix(string(e.Payload), limit)
		encoded, _ := json.Marshal(cut)
		e.Truncated = &Truncated{Field: "payload", TotalBytes: len(e.Payload), ReturnedBytes: len(cut)}
		e.Payload = encoded
	}
	return e.Truncated != nil
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
