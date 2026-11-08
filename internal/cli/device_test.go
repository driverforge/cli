package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/driverforgeapi"
	"github.com/driverforge/cli/internal/orgctx"
)

// `driverforge device current` prints just the device name when one is selected for
// the active org.
func TestDeviceCurrentPrintsName(t *testing.T) {
	isolateConfig(t)
	if err := orgctx.SetCurrent(orgctx.Org{ID: "org-1", Slug: "acme"}); err != nil {
		t.Fatal(err)
	}
	if err := orgctx.SetCurrentDevice(orgctx.Device{ID: "dev-1", Name: "living-room", OrgID: "org-1"}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newDeviceCurrentCmd()
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("device current = %v", err)
	}
	if got := out.String(); got != "living-room\n" {
		t.Errorf("stdout = %q, want \"living-room\\n\"", got)
	}
}

// With no device selected it stays silent and signals a silent, non-reportable
// error (so the process exits non-zero without noise).
func TestDeviceCurrentSilentWhenUnset(t *testing.T) {
	isolateConfig(t)
	if err := orgctx.SetCurrent(orgctx.Org{ID: "org-1", Slug: "acme"}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newDeviceCurrentCmd()
	cmd.SetOut(&out)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected a (silent) error when no device is selected")
	}
	if !clierr.IsUser(err) {
		t.Errorf("silent error should be a non-reportable UserError, got %T", err)
	}
	if out.Len() != 0 {
		t.Errorf("expected no stdout, got %q", out.String())
	}
}

// An org with no active controllers is an expected condition, not a bug: it
// must be a friendly (non-reportable) UserError, never a crash card + Sentry.
func TestNoActiveDevicesIsFriendly(t *testing.T) {
	err := noActiveDevicesErr("Suru Pty Ltd")
	if !clierr.IsUser(err) {
		t.Fatalf("no-active-devices should be a UserError, got %T", err)
	}
	if !strings.Contains(err.Error(), "Suru Pty Ltd") {
		t.Errorf("message should name the org: %q", err.Error())
	}
	var ue *clierr.UserError
	if errors.As(err, &ue) && !strings.Contains(ue.Hint, agentInstallURL) {
		t.Errorf("hint should link the agent install guide: %q", ue.Hint)
	}
}

func TestFilterActive(t *testing.T) {
	in := []driverforgeapi.Device{
		{ID: "a", Status: "ACTIVE"},
		{ID: "b", Status: "REVOKED"},
		{ID: "c", Status: "active"}, // case-insensitive
		{ID: "d", Status: "REJECTED"},
	}
	out := filterActive(in)
	if len(out) != 2 {
		t.Fatalf("got %d active, want 2: %+v", len(out), out)
	}
}

func TestFindDevice(t *testing.T) {
	devs := []driverforgeapi.Device{
		{ID: "uuid-1", DeviceID: "00:0D:6F:AB", Hostname: "ea5-primary", FriendlyName: "Living Room", Status: "ACTIVE"},
		{ID: "uuid-2", DeviceID: "00:0D:6F:CD", Hostname: "ea5-secondary", Status: "ACTIVE"},
	}
	for _, q := range []string{"uuid-1", "00:0d:6f:ab", "EA5-PRIMARY", "living room"} {
		if d := findDevice(devs, q); d == nil || d.ID != "uuid-1" {
			t.Errorf("findDevice(%q) did not match uuid-1: %+v", q, d)
		}
	}
	if findDevice(devs, "nope") != nil {
		t.Error("findDevice matched a non-existent device")
	}
}
