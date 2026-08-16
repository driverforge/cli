package agent

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// Remote strings render into the user's terminal, so they are stripped of
// control sequences at ingestion — a hostile agent must not be able to move
// the cursor, clear the screen, or write the terminal title/clipboard. The
// payloads use JSON \u escapes (as a real hostile response would); the decoder
// turns them into the actual control runes that the scrubber then strips.
func TestHealthScrubsControlSequences(t *testing.T) {
	// \u001b[2J = clear screen; \u001b]0;…\u0007 = set-title OSC.
	body := `{"agentVersion":"\u001b[2J1.0.0","controllerName":"\u001b]0;pwn\u0007EA-5","protocol":4,"capabilities":[]}`
	c := fakeAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, body)
	})
	h, err := c.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if h.AgentVersion != "1.0.0" {
		t.Errorf("AgentVersion = %q, want scrubbed 1.0.0", h.AgentVersion)
	}
	if h.ControllerName != "EA-5" {
		t.Errorf("ControllerName = %q, want scrubbed EA-5", h.ControllerName)
	}
}

// Streamed deploy events are scrubbed the same way, on both the emitted and
// terminal copies.
func TestDeployStreamScrubsControlSequences(t *testing.T) {
	stubLogin(t)
	// step: SGR colour; hint: an OSC-8 hyperlink wrapping "here".
	line := `{"state":"unconfirmed","terminal":true,"step":"\u001b[31mrestarting\u001b[0m","hint":"check \u001b]8;;http://evil\u001b\\here\u001b]8;;\u001b\\"}`
	c := fakeAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, line+"\n")
	})
	var last DeployEvent
	_, terminal, err := c.DeployStream(context.Background(), "d.c4z", "h", "", strings.NewReader("c4z"), true, func(e DeployEvent) { last = e })
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Step != "restarting" {
		t.Errorf("terminal Step = %q, want scrubbed restarting", terminal.Step)
	}
	if terminal.Hint != "check here" {
		t.Errorf("terminal Hint = %q, want scrubbed 'check here'", terminal.Hint)
	}
	if last.Step != "restarting" {
		t.Errorf("emitted Step = %q, want scrubbed restarting", last.Step)
	}
}

// The project-tree location names render straight into the picker.
func TestProjectTreeScrubsNodeNames(t *testing.T) {
	stubLogin(t)
	body := `{"roots":[{"id":"r1","name":"\u001b[2JMain House","children":[{"id":"r2","name":"\u001b]0;x\u0007Theatre"}]}]}`
	c := fakeAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, body)
	})
	roots, err := c.ProjectTree(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if roots[0].Name != "Main House" {
		t.Errorf("root name = %q, want scrubbed Main House", roots[0].Name)
	}
	if roots[0].Children[0].Name != "Theatre" {
		t.Errorf("child name = %q, want scrubbed Theatre", roots[0].Children[0].Name)
	}
}
