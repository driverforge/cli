// Package telemetryread reads a driver's telemetry (logs, errors and events)
// back out of Driverforge for `driverforge logs|errors|events|get`: one entry
// shape for all three types, the filters the commands share, payload
// truncation, paging a query newest-first into an oldest-first result, and
// waiting on the live stream for entries still to come.
//
// The entry shape is a published contract for scripts and coding agents. It
// carries only fields the web app already shows, and a driver's payload exactly
// as stored: structured JSON stays nested, strings stay strings, nothing is
// reordered, summarised or dropped.
package telemetryread

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/driverforge/cli/internal/driverforgeapi"
)

// Kind is an entry's type.
type Kind string

const (
	KindLog   Kind = "log"
	KindError Kind = "error"
	KindEvent Kind = "event"
)

// Device identifies the controller an entry came from. ID is the hardware id
// (the controller's MAC), which `--device` accepts; Name is its display name
// when the controller is registered to the organization.
type Device struct {
	ID   string  `json:"id"`
	Name *string `json:"name"`
}

// Truncated marks an entry whose message (logs) or payload (errors, events)
// was cut to the size cap. Field names which one.
type Truncated struct {
	Field         string `json:"field"`
	TotalBytes    int    `json:"total_bytes"`
	ReturnedBytes int    `json:"returned_bytes"`
}

// Entry is one log, error or event. Every key is always present, null when it
// doesn't apply to the type, so a reader can rely on the shape.
//
//   - message: a log's text; an error's message; null for events.
//   - payload: an event's {"args", "result"}; an error's {"type",
//     "stacktrace", "args", "lastLog"}; null for logs, whose text is the
//     message.
//   - name: the event's name; for a log or error, the event it was raised in.
//   - level: logs and errors ("error"); null for events.
//   - status, duration_ms, error_id: events only.
type Entry struct {
	ID            string          `json:"id"`
	Type          Kind            `json:"type"`
	Timestamp     string          `json:"timestamp"`
	Level         *string         `json:"level"`
	Name          *string         `json:"name"`
	Message       *string         `json:"message"`
	Payload       json.RawMessage `json:"payload"`
	Status        *string         `json:"status"`
	DurationMS    *float64        `json:"duration_ms"`
	ErrorID       *string         `json:"error_id"`
	Driver        string          `json:"driver"`
	DriverVersion *string         `json:"driver_version"`
	Device        Device          `json:"device"`
	CorrelationID *string         `json:"correlation_id"`
	Truncated     *Truncated      `json:"truncated"`

	at time.Time // the entry's own time, for ordering and windows
}

// Time is the entry's own timestamp: when a log or error was raised, when an
// event started.
func (e Entry) Time() time.Time { return e.at }

// AllowedKeys is every key an entry may emit. The output contract test holds
// the encoder to it, so a field reaches users only by being added here.
var AllowedKeys = []string{
	"id", "type", "timestamp", "level", "name", "message", "payload", "status",
	"duration_ms", "error_id", "driver", "driver_version", "device",
	"correlation_id", "truncated",
}

// Names resolves a controller's hardware id to its display name, or "" when
// the controller is not registered to the organization.
type Names func(controller string) string

func msTime(ms float64) time.Time {
	sec, frac := math.Modf(ms / 1000)
	return time.Unix(int64(sec), int64(frac*1e9)).UTC()
}

// formatTime is RFC 3339 UTC with milliseconds, the contract's timestamp form.
func formatTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z07:00") }

func opt(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func device(controller string, names Names) Device {
	d := Device{ID: controller}
	if names != nil {
		d.Name = opt(names(controller))
	}
	return d
}

// raw is v as JSON, null when the server sent nothing.
func raw(v json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(v)) == 0 {
		return json.RawMessage("null")
	}
	return v
}

// object encodes an ordered set of payload fields. Values are already JSON, so
// they are written as they came: the server's bytes, its key order.
func object(fields ...field) json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, f := range fields {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(f.key)
		b.Write(k)
		b.WriteByte(':')
		b.Write(raw(f.value))
	}
	b.WriteByte('}')
	return b.Bytes()
}

type field struct {
	key   string
	value json.RawMessage
}

func str(s string) json.RawMessage {
	if s == "" {
		return nil
	}
	b, _ := json.Marshal(s)
	return b
}

// FromLog converts a graph log.
func FromLog(l driverforgeapi.LogEntry, names Names) Entry {
	at := msTime(l.CreatedAt)
	level := normaliseLevel(l.Level)
	return Entry{
		ID:            l.ID,
		Type:          KindLog,
		Timestamp:     formatTime(at),
		Level:         &level,
		Name:          opt(l.EventName),
		Message:       &l.Message,
		Payload:       json.RawMessage("null"),
		Driver:        l.Driver,
		DriverVersion: opt(l.DriverVersion),
		Device:        device(l.Controller, names),
		CorrelationID: opt(l.TraceID),
		at:            at,
	}
}

// FromEvent converts a graph event. Its id is the trace id, which is how `get`
// looks it up and what its logs and errors carry as correlation_id.
func FromEvent(e driverforgeapi.EventEntry, names Names) Entry {
	at := msTime(e.StartedAt)
	duration := e.Duration
	return Entry{
		ID:            e.TraceID,
		Type:          KindEvent,
		Timestamp:     formatTime(at),
		Name:          opt(e.Name),
		Payload:       object(field{"args", e.Args}, field{"result", e.Result}),
		Status:        opt(e.Status),
		DurationMS:    &duration,
		ErrorID:       opt(e.ErrorID),
		Driver:        e.Driver,
		DriverVersion: opt(e.DriverVersion),
		Device:        device(e.Controller, names),
		CorrelationID: opt(e.TraceID),
		at:            at,
	}
}

// FromError converts a graph error occurrence.
func FromError(e driverforgeapi.ErrorEntry, names Names) Entry {
	at := msTime(e.CreatedAt)
	level := "error"
	return Entry{
		ID:        e.ID,
		Type:      KindError,
		Timestamp: formatTime(at),
		Level:     &level,
		Name:      opt(e.EventName),
		Message:   &e.Value,
		Payload: object(
			field{"type", str(e.Type)},
			field{"stacktrace", e.Stacktrace},
			field{"args", e.Args},
			field{"lastLog", str(e.LastLog)},
		),
		Driver:        e.Driver,
		DriverVersion: opt(e.DriverVersion),
		Device:        device(e.Controller, names),
		CorrelationID: opt(e.TraceID),
		at:            at,
	}
}

// FromPublication converts a live-stream publication, the same record the web
// app's live view receives. ok is false for anything that isn't a log, error
// or event (heartbeats, lifecycle beacons, flags) or that doesn't parse.
func FromPublication(pub json.RawMessage, names Names) (Entry, bool) {
	var head struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(pub, &head) != nil {
		return Entry{}, false
	}
	switch Kind(head.Type) {
	case KindLog:
		var l driverforgeapi.LogEntry
		if json.Unmarshal(pub, &l) != nil || l.ID == "" {
			return Entry{}, false
		}
		return FromLog(l, names), true
	case KindEvent:
		var e driverforgeapi.EventEntry
		if json.Unmarshal(pub, &e) != nil || e.TraceID == "" {
			return Entry{}, false
		}
		return FromEvent(e, names), true
	case KindError:
		// The stream renames the error's own type to errorType, because
		// "type" is the publication's discriminator.
		var e struct {
			driverforgeapi.ErrorEntry
			ErrorType string `json:"errorType"`
		}
		if json.Unmarshal(pub, &e) != nil || e.ID == "" {
			return Entry{}, false
		}
		e.ErrorEntry.Type = e.ErrorType
		return FromError(e.ErrorEntry, names), true
	}
	return Entry{}, false
}

// normaliseLevel lower-cases a stored level ("INFO" → "info"), the form the
// contract and --level use.
func normaliseLevel(l string) string { return strings.ToLower(l) }
