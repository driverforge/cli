package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/driverforgeapi"
	"github.com/driverforge/cli/internal/orgctx"
)

func nameTestDevices() []driverforgeapi.Device {
	return []driverforgeapi.Device{
		{ID: "uuid-1", DeviceID: "00:0D:6F:AB", Hostname: "ea5-primary", FriendlyName: "Living Room", Status: "ACTIVE"},
		{ID: "uuid-2", DeviceID: "00:0D:6F:CD", Hostname: "ea5-secondary", FriendlyName: "bench", Status: "ACTIVE"},
	}
}

// A name already worn by another controller is refused: findDevice matches
// friendly names, so allowing a duplicate would make `--device main` resolve to
// whichever device happened to come back first.
func TestCheckNameAvailableRejectsDuplicateName(t *testing.T) {
	err := checkNameAvailable(nameTestDevices(), "uuid-1", "BENCH")
	var ue *clierr.UserError
	if !errors.As(err, &ue) {
		t.Fatalf("want a UserError, got %v", err)
	}
	if !strings.Contains(ue.Message, "already named") {
		t.Errorf("message = %q, want it to name the clash", ue.Message)
	}
}

// findDevice also matches hostnames, so a name that shadows another
// controller's hostname is just as ambiguous as a duplicate friendly name.
func TestCheckNameAvailableRejectsOtherHostname(t *testing.T) {
	err := checkNameAvailable(nameTestDevices(), "uuid-1", "ea5-secondary")
	var ue *clierr.UserError
	if !errors.As(err, &ue) {
		t.Fatalf("want a UserError, got %v", err)
	}
	if !strings.Contains(ue.Message, "hostname") {
		t.Errorf("message = %q, want it to explain the hostname clash", ue.Message)
	}
}

// Re-applying a device's own name (or its own hostname) is a no-op, not a clash.
func TestCheckNameAvailableAllowsOwnNames(t *testing.T) {
	for _, name := range []string{"Living Room", "ea5-primary", "main"} {
		if err := checkNameAvailable(nameTestDevices(), "uuid-1", name); err != nil {
			t.Errorf("checkNameAvailable(%q) = %v, want nil", name, err)
		}
	}
}

// The mutation keys on the org-association id, so a controller registered to
// the user but not the org comes back "not found" — which is a friendly
// condition about association, not a missing device.
func TestDeviceNameErrExplainsUnassociatedDevice(t *testing.T) {
	err := deviceNameErr(&driverforgeapi.GraphError{Message: "Device not found."},
		orgctx.Device{Name: "ea5-primary"}, "Acme")
	var ue *clierr.UserError
	if !errors.As(err, &ue) {
		t.Fatalf("want a UserError, got %v", err)
	}
	if ue.Title != "Device not in this organization" {
		t.Errorf("title = %q", ue.Title)
	}
	if !strings.Contains(ue.Message, "ea5-primary") || !strings.Contains(ue.Message, "Acme") {
		t.Errorf("message = %q, want the device and org named", ue.Message)
	}
}

// Renaming changes the name for the whole org, so a role without edit rights is
// refused — say that, rather than surfacing a raw permission error.
func TestDeviceNameErrExplainsPermission(t *testing.T) {
	err := deviceNameErr(&driverforgeapi.GraphError{Message: "Forbidden resource", Code: "FORBIDDEN"},
		orgctx.Device{Name: "ea5-primary"}, "Acme")
	var ue *clierr.UserError
	if !errors.As(err, &ue) {
		t.Fatalf("want a UserError, got %v", err)
	}
	if ue.Title != "Not allowed to rename devices" {
		t.Errorf("title = %q", ue.Title)
	}
}

// Anything unanticipated stays a raw error so it still reaches the crash card.
func TestDeviceNameErrPassesThroughUnknown(t *testing.T) {
	in := errors.New("boom")
	if got := deviceNameErr(in, orgctx.Device{Name: "x"}, "Acme"); got != in {
		t.Errorf("deviceNameErr wrapped an unanticipated error: %v", got)
	}
}

// `device name` with neither a name nor --clear is a friendly usage error, not
// a silent no-op that would look like it worked.
func TestDeviceNameRequiresANameOrClear(t *testing.T) {
	isolateConfig(t)
	cmd := newDeviceNameCmd()
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	var ue *clierr.UserError
	if !errors.As(err, &ue) {
		t.Fatalf("want a UserError, got %v", err)
	}
	if !strings.Contains(ue.Message, "no name given") {
		t.Errorf("message = %q", ue.Message)
	}
}

// --clear and a name together are contradictory; refuse rather than guess.
func TestDeviceNameRejectsClearWithName(t *testing.T) {
	isolateConfig(t)
	cmd := newDeviceNameCmd()
	cmd.SetArgs([]string{"main", "--clear"})
	err := cmd.Execute()
	var ue *clierr.UserError
	if !errors.As(err, &ue) {
		t.Fatalf("want a UserError, got %v", err)
	}
	if !strings.Contains(ue.Message, "--clear takes no name") {
		t.Errorf("message = %q", ue.Message)
	}
}
