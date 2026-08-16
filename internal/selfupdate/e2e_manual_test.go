package selfupdate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Opt-in end-to-end check against the real release store. Not part of the
// normal suite: it needs the network and the production signing key.
func TestAgainstProductionReleaseStore(t *testing.T) {
	if os.Getenv("DRIVERFORGE_E2E") == "" {
		t.Skip("set DRIVERFORGE_E2E=1 to run against the live release store")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, binaryName)
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho stale\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	const prod = "https://releases.driverforge.com/driverforge-releases/driverforge-cli/latest/manifest.json"
	version, err := Apply(context.Background(), prod, Target{Path: path, Writable: true}, func(s string) {
		t.Log(s)
	})
	if err != nil {
		t.Fatalf("Apply against production: %v", err)
	}
	t.Logf("installed %s", version)

	out, err := exec.Command(path, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("the installed binary does not run: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), version) {
		t.Errorf("installed binary reports %q, manifest said %s", strings.TrimSpace(string(out)), version)
	}
	t.Logf("installed binary reports: %s", strings.TrimSpace(string(out)))
}
