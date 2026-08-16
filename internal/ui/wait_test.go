package ui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestWaitModelViews(t *testing.T) {
	m := &waitModel{label: "Waiting…", spinner: newSpinner()}

	// While spinning: shows a spinner frame and the label.
	spinning := m.View().Content
	if !strings.Contains(spinning, "Waiting…") {
		t.Errorf("spinning view missing label: %q", spinning)
	}
	if !strings.Contains(spinning, "⣾") { // first frame of spinner.Dot
		t.Errorf("spinning view missing spinner frame: %q", spinning)
	}

	// Finished OK: the spinner line becomes a ✓ success label (supplied on done).
	m.quit = true
	m.done = "Logged in as alice"
	done := m.View().Content
	if !strings.Contains(done, "✓") || !strings.Contains(done, "Logged in as alice") {
		t.Errorf("done view = %q, want ✓ + success label", done)
	}

	// Finished with error: the line is cleared so the caller can render it.
	m.err = errors.New("boom")
	if got := m.View().Content; got != "" {
		t.Errorf("error view = %q, want empty", got)
	}
}

func TestWaitForNonInteractive(t *testing.T) {
	var buf bytes.Buffer
	ran := false
	err := WaitFor(context.Background(), &buf, false, "Waiting…", func() (string, error) {
		ran = true
		return "Logged in as alice", nil
	})
	if err != nil {
		t.Fatalf("WaitFor: %v", err)
	}
	if !ran {
		t.Error("fn was not run")
	}
	if !strings.Contains(buf.String(), "Waiting…") {
		t.Errorf("plain output missing label: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "Logged in as alice") {
		t.Errorf("plain output missing success label: %q", buf.String())
	}
	if strings.ContainsAny(buf.String(), "▱▰") {
		t.Errorf("non-interactive output must not animate a spinner: %q", buf.String())
	}
}

func TestWaitForNonInteractivePropagatesError(t *testing.T) {
	var buf bytes.Buffer
	want := errors.New("poll failed")
	got := WaitFor(context.Background(), &buf, false, "Waiting…", func() (string, error) { return "", want })
	if !errors.Is(got, want) {
		t.Errorf("WaitFor error = %v, want %v", got, want)
	}
}
