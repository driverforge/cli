package c4z

import (
	"archive/zip"
	"context"
	"flag"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// updateGolden regenerates the golden files: `go test ./pkg/c4z -run Golden -update`.
var updateGolden = flag.Bool("update", false, "update golden test files")

// TestGoldenBasic builds the testdata/fixtures/basic driver with a pinned clock
// and asserts the produced .c4z contents and source map match the committed
// golden output byte-for-byte. This freezes the squish, driver.xml transform,
// pack layout (c4zDir/recurse/module exclusion), and sourcemap behaviour so any
// regression is caught on every run, with no external toolchain required.
func TestGoldenBasic(t *testing.T) {
	fixture := filepath.Join("..", "..", "testdata", "fixtures", "basic")
	goldenDir := filepath.Join("..", "..", "testdata", "golden", "basic-dev")
	dist := t.TempDir()
	now := time.Date(2026, 6, 9, 14, 5, 0, 0, time.UTC)

	res, err := Build(context.Background(), Options{
		Configuration: "dev",
		SrcDir:        filepath.Join(fixture, "src"),
		DistDir:       dist,
		Sourcemap:     true,
		Now:           now,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Collect the actual outputs: every .c4z entry under "c4z/", plus the map.
	actual := map[string][]byte{}
	for name, data := range readZipEntries(t, res.C4zPath) {
		actual["c4z/"+name] = data
	}
	mapBytes, err := os.ReadFile(res.SourceMapPath)
	if err != nil {
		t.Fatal(err)
	}
	actual["sourcemap.json"] = mapBytes

	if *updateGolden {
		writeGolden(t, goldenDir, actual)
		t.Logf("updated golden in %s", goldenDir)
		return
	}
	compareGolden(t, goldenDir, actual)
}

func readZipEntries(t *testing.T, path string) map[string][]byte {
	t.Helper()
	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	out := map[string][]byte{}
	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name] = data
	}
	return out
}

func writeGolden(t *testing.T, dir string, files map[string][]byte) {
	t.Helper()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	for rel, data := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func compareGolden(t *testing.T, dir string, actual map[string][]byte) {
	t.Helper()
	// Expected set, from the golden tree on disk.
	expected := map[string]bool{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		expected[filepath.ToSlash(rel)] = true
		want, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		got, ok := actual[filepath.ToSlash(rel)]
		if !ok {
			t.Errorf("missing output: %s (present in golden, not produced)", rel)
			return nil
		}
		if string(got) != string(want) {
			t.Errorf("content mismatch for %s\n--- want ---\n%s\n--- got ---\n%s", rel, want, got)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("golden dir %s not found (run with -update to create it): %v", dir, err)
	}
	// Flag any outputs that have no golden counterpart.
	var extra []string
	for name := range actual {
		if !expected[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	for _, name := range extra {
		t.Errorf("unexpected output with no golden file: %s (run -update if intended)", name)
	}
}
