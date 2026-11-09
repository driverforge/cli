package project

import (
	"errors"
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

func TestLocateFindsNamedManifestAtProjectRoot(t *testing.T) {
	root := t.TempDir()
	manifest := filepath.Join(root, "pool-pump.c4zproj")
	if err := os.WriteFile(manifest, []byte("<Driver/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	proj, err := Locate(root)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if proj.Dir != root || proj.SrcDir != root || proj.ManifestPath != manifest {
		t.Errorf("Locate() = %+v, want root-level manifest project", proj)
	}
}

func TestLocateFindsManifestInTopLevelSourceDirectory(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "driver")
	manifest := filepath.Join(src, "pool-pump.c4zproj")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte("<Driver/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	proj, err := Locate(root)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if proj.Dir != root || proj.SrcDir != src || proj.ManifestPath != manifest {
		t.Errorf("Locate() = %+v, want top-level source manifest project", proj)
	}
}

func TestLocateReportsAmbiguousManifests(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"legacy.c4zproj", "current.c4zproj"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("<Driver/>"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Locate(root); !errors.Is(err, ErrAmbiguous) {
		t.Errorf("Locate error = %v, want ErrAmbiguous", err)
	}
}

func TestLocateForInitReturnsSiblingManifests(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"legacy.c4zproj", "current.c4zproj"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("<Driver/>"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	proj, err := LocateForInit(root)
	if err != nil {
		t.Fatalf("LocateForInit: %v", err)
	}
	if len(proj.ManifestPaths) != 2 {
		t.Errorf("ManifestPaths = %v, want both sibling manifests", proj.ManifestPaths)
	}
}

func TestLocateForInitSupportsRootDriverLayout(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"tooling", "shared", "lib", "vendor", "output", "assets", "www"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"driver.lua", "driver.xml", "squishy"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("fixture"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"compat.c4zproj", "current.c4zproj", "legacy.c4zproj"}
	for _, name := range want {
		if err := os.WriteFile(filepath.Join(root, name), []byte("<Driver/>"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	proj, err := LocateForInit(root)
	if err != nil {
		t.Fatalf("LocateForInit: %v", err)
	}
	if proj.Dir != root || proj.SrcDir != root {
		t.Errorf("LocateForInit() = %+v, want root-level source project", proj)
	}
	if len(proj.ManifestPaths) != len(want) {
		t.Fatalf("ManifestPaths = %v, want %d manifests", proj.ManifestPaths, len(want))
	}
	for index, name := range want {
		if got := filepath.Base(proj.ManifestPaths[index]); got != name {
			t.Errorf("ManifestPaths[%d] = %q, want %q", index, got, name)
		}
	}

	built, err := LocateManifest(root, "current.c4zproj")
	if err != nil {
		t.Fatalf("LocateManifest: %v", err)
	}
	if built.SrcDir != root || filepath.Base(built.ManifestPath) != "current.c4zproj" {
		t.Errorf("LocateManifest() = %+v, want current root manifest", built)
	}
}

func TestLocateManifestSelectsExplicitManifest(t *testing.T) {
	root := t.TempDir()
	manifest := filepath.Join(root, "legacy.c4zproj")
	if err := os.WriteFile(manifest, []byte("<Driver/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	proj, err := LocateManifest(root, "legacy.c4zproj")
	if err != nil {
		t.Fatalf("LocateManifest: %v", err)
	}
	if proj.ManifestPath != manifest || proj.SrcDir != root {
		t.Errorf("LocateManifest() = %+v, want root manifest", proj)
	}
}
