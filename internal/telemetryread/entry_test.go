package telemetryread

import (
	"bytes"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/driverforge/cli/internal/driverforgeapi"
)

var names = func(controller string) string {
	if controller == "000FFF9BB74C" {
		return "bench-a"
	}
	return ""
}

func TestEventPayloadRoundTripsExactly(t *testing.T) {
	// A vendor response with an undocumented outer "data" wrapper, nulls and a
	// key order that re-encoding through a map would change.
	args := json.RawMessage(`{"zeta":1,"data":{"items":[{"id":7,"name":null}]},"alpha":"x"}`)
	e := FromEvent(driverforgeapi.EventEntry{
		TraceID: "0123456789abcdef0123456789abcdef", Name: "ReceivedFromNetwork",
		Args: args, StartedAt: 1762247551482, Controller: "000FFF9BB74C", DriverVersion: "12",
	}, names)

	var payload struct {
		Args   json.RawMessage `json:"args"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(e.Payload, &payload); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	if !bytes.Equal(payload.Args, args) {
		t.Errorf("args = %s, want the stored bytes %s", payload.Args, args)
	}
	if string(payload.Result) != "null" {
		t.Errorf("absent result = %s, want null", payload.Result)
	}
	if e.Timestamp != "2025-11-04T09:12:31.482Z" {
		t.Errorf("timestamp = %q", e.Timestamp)
	}
	if e.Device.ID != "000FFF9BB74C" || deref(e.Device.Name) != "bench-a" {
		t.Errorf("device = %+v", e.Device)
	}
}

func TestLogKeepsTextAsMessage(t *testing.T) {
	text := `Vendor response: {"data":{"items":[]}}`
	e := FromLog(driverforgeapi.LogEntry{ID: "l1", Level: "INFO", Message: text, CreatedAt: 1}, names)
	if *e.Message != text || string(e.Payload) != "null" || *e.Level != "info" {
		t.Errorf("log = message %q payload %s level %q", *e.Message, e.Payload, *e.Level)
	}
	if e.Device.Name != nil {
		t.Errorf("unregistered controller got name %q", *e.Device.Name)
	}
}

func TestEntryKeysAreTheAllowList(t *testing.T) {
	for _, e := range []Entry{
		FromLog(driverforgeapi.LogEntry{ID: "l1"}, nil),
		FromEvent(driverforgeapi.EventEntry{TraceID: "t1"}, nil),
		FromError(driverforgeapi.ErrorEntry{ID: "e1"}, nil),
	} {
		b, _ := json.Marshal(e)
		var m map[string]json.RawMessage
		_ = json.Unmarshal(b, &m)
		for k := range m {
			if !slices.Contains(AllowedKeys, k) {
				t.Errorf("%s entry emits %q, outside the allow-list", e.Type, k)
			}
		}
		if len(m) != len(AllowedKeys) {
			t.Errorf("%s entry has %d keys, want every allowed key present (%d)", e.Type, len(m), len(AllowedKeys))
		}
	}
}

// TestLogArgsArePayload: a log whose call passed a table carries its arguments
// as payload, exactly as sent; a text-only log's payload stays null.
func TestLogArgsArePayload(t *testing.T) {
	args := `["Response received:",{"status":200,"body":"OK"}]`
	e := FromLog(driverforgeapi.LogEntry{ID: "l1", Message: "Response received: [table]", Args: json.RawMessage(args)}, nil)
	if want := `{"args":` + args + `,"argsTruncated":false}`; string(e.Payload) != want {
		t.Errorf("payload = %s, want %s", e.Payload, want)
	}
	if *e.Message != "Response received: [table]" {
		t.Errorf("message = %q", *e.Message)
	}

	capped := FromLog(driverforgeapi.LogEntry{ID: "l2", Args: json.RawMessage(`[{"items":[1,"[truncated]"]}]`), ArgsTruncated: true}, nil)
	if !strings.HasSuffix(string(capped.Payload), `"argsTruncated":true}`) {
		t.Errorf("capped payload = %s", capped.Payload)
	}

	for _, a := range []string{"", "null", " null "} {
		if e := FromLog(driverforgeapi.LogEntry{ID: "l3", Message: "hi", Args: json.RawMessage(a)}, nil); string(e.Payload) != "null" {
			t.Errorf("args %q: payload = %s, want null", a, e.Payload)
		}
	}

	// The graph response decodes into the same payload.
	var l driverforgeapi.LogEntry
	if err := json.Unmarshal([]byte(`{"id":"l4","message":"m","args":`+args+`,"argsTruncated":false}`), &l); err != nil {
		t.Fatal(err)
	}
	if e := FromLog(l, nil); !strings.Contains(string(e.Payload), `{"status":200,"body":"OK"}`) {
		t.Errorf("decoded payload = %s", e.Payload)
	}
}

// TestErrorRef: an error carries its group reference, from the server or, on
// the live stream, built from the project slug; logs and events never do.
func TestErrorRef(t *testing.T) {
	e := FromError(driverforgeapi.ErrorEntry{ID: "e1", Fingerprint: "4f2a9c1b", Ref: "POOL-PUMP-4F2A9C1B"}, nil)
	e.FillRef("other")
	if e.Ref == nil || *e.Ref != "POOL-PUMP-4F2A9C1B" {
		t.Errorf("server ref = %v, want it kept", e.Ref)
	}

	pub := `{"type":"error","errorType":"lua:runtime_error","id":"e2","value":"boom","createdAt":1,"controller":"c","fingerprint":"4F2A9C1B"}`
	e, _ = FromPublication(json.RawMessage(pub), nil)
	if e.Ref != nil {
		t.Errorf("publication ref before FillRef = %q", *e.Ref)
	}
	e.FillRef("pool-pump")
	if e.Ref == nil || *e.Ref != "POOL-PUMP-4F2A9C1B" {
		t.Errorf("filled ref = %v", e.Ref)
	}

	for _, fp := range []string{"", "4f2a9c1", "not-hex!", "4f2a9c1b00"} {
		x := FromError(driverforgeapi.ErrorEntry{ID: "e3", Fingerprint: fp}, nil)
		if x.FillRef("pool-pump"); x.Ref != nil {
			t.Errorf("fingerprint %q: ref = %q, want none", fp, *x.Ref)
		}
	}
	l := FromLog(driverforgeapi.LogEntry{ID: "l1"}, nil)
	if l.FillRef("pool-pump"); l.Ref != nil {
		t.Error("a log got a ref")
	}
	b, _ := json.Marshal(l)
	if !strings.Contains(string(b), `"ref":null`) {
		t.Errorf("log JSON lacks a null ref: %s", b)
	}
}

func TestFromPublication(t *testing.T) {
	// The live record carries routing and device meta the contract must never
	// emit; conversion keeps only mapped fields.
	logPub := `{"type":"log","id":"l1","createdAt":1762247551482,"level":"WARN","message":"hi","traceId":"t","controller":"000FFF9BB74C","driver":"d.c4z","driverVersion":"12","organizationId":"org","meta":{"ip":"10.0.0.2"}}`
	e, ok := FromPublication(json.RawMessage(logPub), names)
	if !ok || e.Type != KindLog || *e.Level != "warn" || *e.DriverVersion != "12" {
		t.Fatalf("log publication = %+v, %v", e, ok)
	}
	b, _ := json.Marshal(e)
	if strings.Contains(string(b), "10.0.0.2") || strings.Contains(string(b), "organizationId") {
		t.Errorf("publication meta leaked into the entry: %s", b)
	}

	errPub := `{"type":"error","errorType":"lua:runtime_error","id":"e1","value":"boom","createdAt":1,"controller":"c"}`
	e, ok = FromPublication(json.RawMessage(errPub), nil)
	if !ok || e.Type != KindError || !strings.Contains(string(e.Payload), `"type":"lua:runtime_error"`) {
		t.Errorf("error publication = %s, %v", e.Payload, ok)
	}

	for _, pub := range []string{`{"type":"heartbeat"}`, `{"type":"log"}`, `not json`} {
		if _, ok := FromPublication(json.RawMessage(pub), nil); ok {
			t.Errorf("FromPublication(%s) accepted it", pub)
		}
	}
}

func TestTruncate(t *testing.T) {
	// "é" is two bytes; a cap landing between them must back off to the
	// boundary.
	msg := "aé" + strings.Repeat("b", 10)
	e := FromLog(driverforgeapi.LogEntry{ID: "l1", Message: msg}, nil)
	if !Truncate(&e, 2) {
		t.Fatal("over-cap message not truncated")
	}
	if *e.Message != "a" || e.Truncated.Field != "message" || e.Truncated.TotalBytes != len(msg) || e.Truncated.ReturnedBytes != 1 {
		t.Errorf("message %q truncated %+v", *e.Message, e.Truncated)
	}

	ev := FromEvent(driverforgeapi.EventEntry{TraceID: "t", Args: json.RawMessage(`{"data":"` + strings.Repeat("x", 100) + `"}`)}, nil)
	total := len(ev.Payload)
	if !Truncate(&ev, 10) {
		t.Fatal("over-cap payload not truncated")
	}
	var s string
	if err := json.Unmarshal(ev.Payload, &s); err != nil || s != `{"args":{"` {
		t.Errorf("truncated payload = %s, want a JSON string of the prefix", ev.Payload)
	}
	if ev.Truncated.Field != "payload" || ev.Truncated.TotalBytes != total || ev.Truncated.ReturnedBytes != 10 {
		t.Errorf("truncated = %+v", ev.Truncated)
	}

	// A log's arguments are capped like any payload, and when both its
	// message and payload are cut, the record describes the payload.
	big := `["` + strings.Repeat("y", 100) + `"]`
	lg := FromLog(driverforgeapi.LogEntry{ID: "l2", Message: strings.Repeat("m", 50), Args: json.RawMessage(big)}, nil)
	total = len(lg.Payload)
	if !Truncate(&lg, 20) {
		t.Fatal("over-cap log payload not truncated")
	}
	if len(*lg.Message) != 20 || lg.Truncated.Field != "payload" || lg.Truncated.TotalBytes != total || lg.Truncated.ReturnedBytes != 20 {
		t.Errorf("message %d bytes, truncated %+v", len(*lg.Message), lg.Truncated)
	}
	if err := json.Unmarshal(lg.Payload, &s); err != nil || !strings.HasPrefix(s, `{"args":["yyy`) {
		t.Errorf("truncated log payload = %s", lg.Payload)
	}

	small := FromLog(driverforgeapi.LogEntry{Message: "ok"}, nil)
	if Truncate(&small, 0) || Truncate(&small, 10) || small.Truncated != nil {
		t.Error("an entry within the cap, or with no cap, was truncated")
	}
}

func TestFilterMatch(t *testing.T) {
	at := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	e := FromLog(driverforgeapi.LogEntry{
		ID: "l1", Level: "ERROR", Message: `got {"Data":1}`, TraceID: "t1", EventName: "OnTimer",
		Driver: "d.c4z", DriverVersion: "12", Controller: "MAC", CreatedAt: float64(at.UnixMilli()),
	}, nil)

	pass := []Filter{
		{},
		{Kind: KindLog, Controller: "MAC", Drivers: []string{"d.c4z"}, DriverVersion: "12", Levels: []string{"warn", "error"}},
		{CorrelationID: "t1", Name: "OnTimer", Fields: map[string]string{"level": "error", "driver": "d.c4z"}},
		{Grep: regexp.MustCompile(`(?i)"data"`)},
		{Since: at, Until: at},
	}
	for i, f := range pass {
		if !f.Match(e) {
			t.Errorf("pass[%d] rejected the entry", i)
		}
	}
	fail := []Filter{
		{Kind: KindEvent},
		{Controller: "OTHER"},
		{DriverVersion: "11"},
		{Levels: []string{"info"}},
		{Fields: map[string]string{"controller": "OTHER"}},
		{Grep: regexp.MustCompile(`"data"`)}, // case-sensitive
		{Since: at.Add(time.Millisecond)},
		{Until: at.Add(-time.Millisecond)},
	}
	for i, f := range fail {
		if f.Match(e) {
			t.Errorf("fail[%d] accepted the entry", i)
		}
	}
}
