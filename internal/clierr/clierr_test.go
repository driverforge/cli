package clierr

import (
	"errors"
	"fmt"
	"testing"
)

func TestUserErrorString(t *testing.T) {
	if got := User("not a project", "run `driverforge init`").Error(); got != "not a project (run `driverforge init`)" {
		t.Errorf("with hint = %q", got)
	}
	if got := User("not a project", "").Error(); got != "not a project" {
		t.Errorf("without hint = %q", got)
	}
}

func TestAsFatal(t *testing.T) {
	// A plain UserError is guidance (not fatal) until marked.
	if IsFatal(User("guidance", "")) {
		t.Error("a plain UserError must not be fatal")
	}
	if got := AsFatal(User("build broke", "fix it")); !IsFatal(got) {
		t.Error("AsFatal(UserError) should be fatal")
	}
	// Reaches a UserError nested inside another wrapper.
	if got := AsFatal(fmt.Errorf("outer: %w", Wrap(errors.New("cause"), "msg", "hint"))); !IsFatal(got) {
		t.Error("AsFatal should mark a nested UserError fatal")
	}
	// A non-UserError passes through untouched and is never fatal.
	plain := errors.New("boom")
	if AsFatal(plain) != plain || IsFatal(plain) {
		t.Error("AsFatal must pass a non-UserError through unchanged")
	}
}

func TestIsUser(t *testing.T) {
	cause := errors.New("boom")
	ue := Wrap(cause, "friendly", "do x")

	if !IsUser(ue) {
		t.Error("IsUser(UserError) = false")
	}
	if !IsUser(fmt.Errorf("context: %w", ue)) {
		t.Error("IsUser(wrapped UserError) = false")
	}
	if IsUser(errors.New("plain")) {
		t.Error("IsUser(plain) = true")
	}
	if IsUser(nil) {
		t.Error("IsUser(nil) = true")
	}
	// The underlying cause stays reachable via errors.Is.
	if !errors.Is(ue, cause) {
		t.Error("Unwrap lost the cause")
	}
}
