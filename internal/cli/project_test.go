package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/driverforge/cli/internal/clierr"
)

// In a directory that is not a driver project, requireProject returns a friendly
// UserError (so it's rendered nicely and never reported to Sentry), not a bare
// "not found" error.
func TestRequireProjectNonProjectDir(t *testing.T) {
	dir := t.TempDir() // empty temp dir → no src/manifest.c4zproj anywhere

	_, err := requireProject(dir)
	if err == nil {
		t.Fatal("expected an error outside a driver project")
	}
	if !clierr.IsUser(err) {
		t.Errorf("expected a clierr.UserError, got %T: %v", err, err)
	}
}

// When a project root exists, requireProject resolves it without error.
func TestRequireProjectFound(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "manifest.c4zproj"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	proj, err := requireProject(dir)
	if err != nil {
		t.Fatalf("requireProject = %v", err)
	}
	if proj.Dir != dir {
		t.Errorf("proj.Dir = %q, want %q", proj.Dir, dir)
	}
}
