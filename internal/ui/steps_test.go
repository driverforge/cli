package ui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRunStepsPlainRunsAllInOrder(t *testing.T) {
	var order []string
	var buf bytes.Buffer
	steps := []Step{
		{Label: "Creating project", Run: func() error { order = append(order, "a"); return nil }},
		{Label: "Applying changes", Run: func() error { order = append(order, "b"); return nil }},
	}
	if err := RunSteps(context.Background(), &buf, false, steps); err != nil {
		t.Fatalf("RunSteps: %v", err)
	}
	if strings.Join(order, ",") != "a,b" {
		t.Errorf("steps ran out of order: %v", order)
	}
	out := buf.String()
	if !strings.Contains(out, "Creating project") || !strings.Contains(out, "Applying changes") {
		t.Errorf("expected step labels in output:\n%s", out)
	}
}

func TestRunStepsStopsAtFailure(t *testing.T) {
	boom := errors.New("boom")
	var ran []string
	steps := []Step{
		{Label: "one", Run: func() error { ran = append(ran, "one"); return nil }},
		{Label: "two", Run: func() error { ran = append(ran, "two"); return boom }},
		{Label: "three", Run: func() error { ran = append(ran, "three"); return nil }},
	}
	err := RunSteps(context.Background(), new(bytes.Buffer), false, steps)
	if !errors.Is(err, boom) {
		t.Fatalf("expected boom, got %v", err)
	}
	if strings.Join(ran, ",") != "one,two" {
		t.Errorf("expected to stop after the failing step, ran: %v", ran)
	}
}
