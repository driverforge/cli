package c4z

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeZip builds a zip at path from the given entries. A name ending in "/" is
// written as a directory entry. Names are written verbatim — the point is to be
// able to author the hostile ones the extractor has to defend against.
func writeZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, body := range entries {
		hdr := &zip.FileHeader{Name: name, Method: zip.Deflate}
		if strings.HasSuffix(name, "/") {
			hdr.SetMode(os.ModeDir | 0o755)
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

// A well-formed archive extracts faithfully: nested paths are created, explicit
// directory entries become directories, and file bodies survive intact.
func TestUnzipExtractsTree(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "driver.c4z")
	writeZip(t, src, map[string]string{
		"driver.xml":           "<devicedata/>",
		"driver.lua":           "print('hi')\n",
		"www/icons/":           "",
		"www/icons/device.png": "PNG",
		"deep/a/b/c.txt":       "nested",
	})

	dest := filepath.Join(tmp, "out")
	if err := unzip(src, dest); err != nil {
		t.Fatalf("unzip: %v", err)
	}

	for rel, want := range map[string]string{
		"driver.xml":           "<devicedata/>",
		"driver.lua":           "print('hi')\n",
		"www/icons/device.png": "PNG",
		"deep/a/b/c.txt":       "nested",
	} {
		got, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	if fi, err := os.Stat(filepath.Join(dest, "www", "icons")); err != nil || !fi.IsDir() {
		t.Errorf("www/icons should be a directory (err=%v)", err)
	}
}

// Zip-slip: an entry whose name escapes the destination must be refused, and
// nothing may be written outside the destination. This is the one extraction
// path in the build that consumes an attacker-influenced archive, so each escape
// shape gets pinned rather than trusting filepath.Join to clean it away.
func TestUnzipRejectsPathTraversal(t *testing.T) {
	cases := []struct {
		name  string
		entry string
	}{
		{"parent escape", "../escaped.txt"},
		{"nested parent escape", "www/../../escaped.txt"},
		{"deep parent escape", "../../../escaped.txt"},
		// The guard must compare against dest + separator: a sibling directory
		// sharing the destination's name as a string prefix is still an escape.
		{"sibling prefix escape", "../out-evil/escaped.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			// dest sits one level down so ".." has somewhere to land inside tmp,
			// where the test can prove nothing was written.
			base := filepath.Join(tmp, "dist")
			dest := filepath.Join(base, "out")
			src := filepath.Join(tmp, "evil.c4z")
			writeZip(t, src, map[string]string{
				"driver.xml": "<devicedata/>",
				tc.entry:     "pwned",
			})

			err := unzip(src, dest)
			if err == nil {
				t.Fatal("unzip accepted an entry that escapes the destination")
			}
			if !strings.Contains(err.Error(), "unsafe path") {
				t.Fatalf("error = %v, want an unsafe-path rejection", err)
			}

			// Nothing may exist outside dest anywhere under tmp.
			err = filepath.WalkDir(tmp, func(p string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				if p == src {
					return nil // the archive itself
				}
				if !strings.HasPrefix(p, dest+string(os.PathSeparator)) {
					t.Errorf("wrote %s outside the destination %s", p, dest)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// An absolute entry name is not an escape: filepath.Join re-roots it under the
// destination. It must extract there rather than at the filesystem root.
func TestUnzipRerootsAbsoluteEntry(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "abs.c4z")
	writeZip(t, src, map[string]string{"/driver.lua": "rooted"})

	dest := filepath.Join(tmp, "out")
	if err := unzip(src, dest); err != nil {
		t.Fatalf("unzip: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "driver.lua"))
	if err != nil {
		t.Fatalf("absolute entry should extract under dest: %v", err)
	}
	if string(got) != "rooted" {
		t.Errorf("content = %q, want %q", got, "rooted")
	}
}

func TestUnzipMissingArchive(t *testing.T) {
	if err := unzip(filepath.Join(t.TempDir(), "absent.c4z"), t.TempDir()); err == nil {
		t.Fatal("unzip of a missing archive should fail")
	}
}

// Options.Unpack extracts the packed .c4z beside it, and the unpacked tree must
// match the archive exactly — it is what users inspect to see what shipped.
func TestBuildUnpackExtractsC4z(t *testing.T) {
	_, srcDir, distDir := writeTestProject(t, false)

	res, err := Build(context.Background(), Options{
		Configuration: "dev", SrcDir: srcDir, DistDir: distDir,
		Unpack: true, Now: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}

	wantDir := filepath.Join(distDir, "test-driver-dev")
	if res.UnpackedDir != wantDir {
		t.Errorf("UnpackedDir = %q, want %q", res.UnpackedDir, wantDir)
	}
	entries := readZip(t, res.C4zPath)
	if len(entries) == 0 {
		t.Fatal("packed c4z has no entries")
	}
	for name, want := range entries {
		got, err := os.ReadFile(filepath.Join(res.UnpackedDir, filepath.FromSlash(name)))
		if err != nil {
			t.Errorf("unpacked tree missing %s: %v", name, err)
			continue
		}
		if string(got) != string(want) {
			t.Errorf("%s: unpacked content differs from the archive", name)
		}
	}
}

// Without Unpack no directory is produced — the flag is the only thing that
// creates it, and Result must not claim one.
func TestBuildWithoutUnpackLeavesNoDir(t *testing.T) {
	_, srcDir, distDir := writeTestProject(t, false)

	res, err := Build(context.Background(), Options{
		Configuration: "dev", SrcDir: srcDir, DistDir: distDir, Now: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.UnpackedDir != "" {
		t.Errorf("UnpackedDir = %q, want empty without Unpack", res.UnpackedDir)
	}
	if _, err := os.Stat(filepath.Join(distDir, "test-driver-dev")); !os.IsNotExist(err) {
		t.Errorf("unpacked dir should not exist (stat err = %v)", err)
	}
}

// A rebuild must not leave a previous build's files in the unpacked tree —
// a stale .lua there reads as if it shipped in the new artifact.
func TestBuildUnpackClearsStaleDir(t *testing.T) {
	_, srcDir, distDir := writeTestProject(t, false)
	stale := filepath.Join(distDir, "test-driver-dev", "stale.lua")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("from a previous build"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Build(context.Background(), Options{
		Configuration: "dev", SrcDir: srcDir, DistDir: distDir,
		Unpack: true, Now: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale file survived the rebuild (stat err = %v)", err)
	}
}
