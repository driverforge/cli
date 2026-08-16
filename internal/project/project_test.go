package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocateFindsRootByManifest(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "manifest.c4zproj"), []byte("<Driver/>"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Locating from a nested directory must walk up to the project root.
	nested := filepath.Join(src, "www", "deep")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	proj, err := Locate(nested)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if proj.Dir != root {
		t.Errorf("Dir = %q, want %q", proj.Dir, root)
	}
	if proj.SrcDir != src {
		t.Errorf("SrcDir = %q, want %q", proj.SrcDir, src)
	}
	if want := filepath.Join(root, "dist"); proj.DistDir != want {
		t.Errorf("DistDir = %q, want %q", proj.DistDir, want)
	}
}

func TestLocateErrorsWhenNoProject(t *testing.T) {
	dir := t.TempDir()
	if _, err := Locate(dir); err == nil {
		t.Error("expected error when no src/manifest.c4zproj is found")
	}
}
