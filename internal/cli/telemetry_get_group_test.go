package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/driverforge/cli/internal/telemetryread"
)

func TestParseErrorRef(t *testing.T) {
	for _, tc := range []struct {
		in, project, fingerprint string
		ok                       bool
	}{
		{"POOL-PUMP-4F2A9C1B", "pool-pump", "4f2a9c1b", true},
		{"pool-pump-4f2a9c1b", "pool-pump", "4f2a9c1b", true},
		{"DRIVER-AE12F1BA", "driver", "ae12f1ba", true},
		{"4F2A9C1B", "", "", false},            // no project
		{"POOL-PUMP-4F2A9C1", "", "", false},   // seven hex
		{"POOL-PUMP-4F2A9C1BX", "", "", false}, // not hex
		{"6f1c2f9e-6c1a-4c39-9a51-0d4b8e1f2a3b", "", "", false},
	} {
		r, ok := parseErrorRef(tc.in)
		if ok != tc.ok || r.project != tc.project || r.fingerprint != tc.fingerprint {
			t.Errorf("parseErrorRef(%q) = %+v, %v", tc.in, r, ok)
		}
	}
}

// TestGetErrorGroup: a reference fetches its group from the project it names,
// with the newest occurrence's get command; with or without --type.
func TestGetErrorGroup(t *testing.T) {
	g := &fakeGraph{group: map[string]map[string]any{"4f2a9c1b": {
		"fingerprint": "4f2a9c1b", "ref": "SMART-TV-4F2A9C1B", "type": "lua:runtime_error",
		"value": "boom", "count": 4, "firstSeen": 1757809252224, "lastSeen": 1757816429634,
		"affectedVersions": []string{"12", "13"}, "affectedControllers": []string{"000FFF9BB74C"},
		"affectedDrivers": []string{"tv.c4z"}, "latestEventName": "OnDriverLateInit",
		"latestStacktrace": map[string]any{"frames": []any{}}, "latestOccurrenceId": "6aa379a1-394d-4a36-a3cf-8dbea3adf1b3",
	}}}
	dir := telemetryEnv(t, g)

	for _, args := range [][]string{
		{"get", "SMART-TV-4F2A9C1B", "--type", "error_group"},
		{"get", "smart-tv-4f2a9c1b"},
	} {
		stdout, _, code := run(t, dir, args...)
		if code != 0 {
			t.Fatalf("%v: exit %d\n%s", args, code, stdout)
		}
		out := decode(t, stdout)
		grp := out["error_group"].(map[string]any)
		if grp["ref"] != "SMART-TV-4F2A9C1B" || grp["count"] != float64(4) || grp["last_seen"] != "2025-09-14T02:20:29.634Z" {
			t.Errorf("%v: group = %v", args, grp)
		}
		if d := grp["devices"].([]any)[0].(map[string]any); d["name"] != "bench-a" {
			t.Errorf("devices = %v", grp["devices"])
		}
		latest := grp["latest_occurrence"].(map[string]any)
		if latest["command"] != "driverforge get 6aa379a1-394d-4a36-a3cf-8dbea3adf1b3 --type error --org acme --project smart-tv" {
			t.Errorf("latest_occurrence = %v", latest)
		}
		if out["scope"].(map[string]any)["project"] != "smart-tv" {
			t.Errorf("scope = %v", out["scope"])
		}
	}
	if fp := g.vars[0]["fingerprint"]; fp != "4f2a9c1b" || g.vars[0]["projectId"] != "proj-1" {
		t.Errorf("query vars = %v", g.vars[0])
	}

	stdout, _, code := run(t, dir, "get", "SMART-TV-00000000")
	if e := decode(t, stdout)["error"].(map[string]any); code == 0 || e["code"] != codeEntryNotFound {
		t.Errorf("missing group: exit %d, %v", code, e)
	}
}

func TestGetErrorGroupUsage(t *testing.T) {
	for _, args := range [][]string{
		{"get", "not-a-ref", "--type", "error_group"},
		{"get", "6f1c2f9e-6c1a-4c39-9a51-0d4b8e1f2a3b", "--type", "error_group"},
		{"get", "SMART-TV-4F2A9C1B", "--max-payload-bytes", "10"},
		{"get", "SMART-TV-4F2A9C1B", "--type", "nope"},
	} {
		if err := validateGet(args[1], typeArg(args), maxArg(args)); err == nil {
			t.Errorf("%v: accepted", args)
		}
	}
}

func typeArg(args []string) string {
	for i, a := range args {
		if a == "--type" {
			return args[i+1]
		}
	}
	return ""
}

func maxArg(args []string) int {
	for i, a := range args {
		if a == "--max-payload-bytes" {
			n := 0
			fmt.Sscan(args[i+1], &n)
			return n
		}
	}
	return 0
}

func TestGetErrorGroupText(t *testing.T) {
	name := "bench-a"
	o := errorGroupOut{Ref: "SMART-TV-4F2A9C1B", Type: "lua:runtime_error", Message: "boom", Count: 4,
		FirstSeen: "a", LastSeen: "b", DriverVersions: []string{"12"},
		Devices:          []telemetryread.Device{{ID: "000FFF9BB74C", Name: &name}},
		LatestOccurrence: &latestOccurrenceOut{ID: "x", Command: "driverforge get x --type error"}}
	var buf bytes.Buffer
	o.writeText(&buf)
	for _, want := range []string{"SMART-TV-4F2A9C1B  lua:runtime_error  4 occurrences", "devices: bench-a", "latest occurrence: driverforge get x"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("text missing %q:\n%s", want, buf.String())
		}
	}
}
