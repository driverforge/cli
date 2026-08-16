package orgctx

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)            // darwin
	t.Setenv("XDG_CONFIG_HOME", dir) // linux
	return dir
}

func TestRoundTripAndCurrent(t *testing.T) {
	isolate(t)

	if _, err := Current(); !errors.Is(err, ErrNoOrg) {
		t.Fatalf("fresh Current = %v, want ErrNoOrg", err)
	}

	org := Org{ID: "uuid-1", Name: "Acme Corp", Slug: "acme-corp"}
	if err := SetCurrent(org); err != nil {
		t.Fatalf("SetCurrent: %v", err)
	}
	got, err := Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if got != org {
		t.Errorf("Current = %+v, want %+v", got, org)
	}

	// Persisted to context.json (NOT credentials.json) — separate from creds.
	path := contextPath()
	if filepath.Base(path) != "context.json" {
		t.Errorf("context filename = %q, want context.json", filepath.Base(path))
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("context file not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "credentials.json")); !os.IsNotExist(err) {
		t.Errorf("orgctx must not write credentials.json (err=%v)", err)
	}
}

func TestClear(t *testing.T) {
	isolate(t)

	if err := SetCurrent(Org{ID: "uuid-1", Slug: "acme"}); err != nil {
		t.Fatalf("SetCurrent: %v", err)
	}
	if err := Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, err := Current(); !errors.Is(err, ErrNoOrg) {
		t.Errorf("after Clear, Current = %v, want ErrNoOrg", err)
	}
	if _, err := os.Stat(contextPath()); !os.IsNotExist(err) {
		t.Errorf("context file should be gone after Clear (stat err=%v)", err)
	}
	// Idempotent: clearing an already-empty context is a no-op.
	if err := Clear(); err != nil {
		t.Errorf("second Clear = %v, want nil", err)
	}
}

func TestDeviceScopedToOrg(t *testing.T) {
	isolate(t)

	if _, err := CurrentDevice(); !errors.Is(err, ErrNoDevice) {
		t.Fatalf("fresh CurrentDevice = %v, want ErrNoDevice", err)
	}

	if err := SetCurrent(Org{ID: "orgA", Name: "A"}); err != nil {
		t.Fatal(err)
	}
	if err := SetCurrentDevice(Device{ID: "d1", Name: "Dev1", OrgID: "orgA"}); err != nil {
		t.Fatal(err)
	}
	got, err := CurrentDevice()
	if err != nil || got.ID != "d1" {
		t.Fatalf("CurrentDevice = (%+v, %v), want d1", got, err)
	}

	// Switching org invalidates the device selection.
	if err := SetCurrent(Org{ID: "orgB", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	if _, err := CurrentDevice(); !errors.Is(err, ErrNoDevice) {
		t.Errorf("device should be invalid after org switch, got %v", err)
	}
}

func TestDeviceGuardOnOrgMismatch(t *testing.T) {
	isolate(t)
	// Device stored for orgA while orgB is active → guarded out even if the
	// SetCurrent clearing was bypassed.
	if err := Save(Context{CurrentOrg: &Org{ID: "orgB"}, CurrentDevice: &Device{ID: "d1", OrgID: "orgA"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := CurrentDevice(); !errors.Is(err, ErrNoDevice) {
		t.Errorf("CurrentDevice across org mismatch = %v, want ErrNoDevice", err)
	}
}

func TestChooseDefault(t *testing.T) {
	ids := []string{"a", "b", "c"}
	if got := ChooseDefault(ids, "b"); got != "b" {
		t.Errorf("remembered present = %q, want b", got)
	}
	if got := ChooseDefault(ids, "z"); got != "a" {
		t.Errorf("remembered absent = %q, want first (a)", got)
	}
	if got := ChooseDefault(ids, ""); got != "a" {
		t.Errorf("no history = %q, want first (a)", got)
	}
	if got := ChooseDefault(nil, "b"); got != "" {
		t.Errorf("empty list = %q, want empty", got)
	}
}
