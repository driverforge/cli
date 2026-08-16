package pack

import (
	"archive/zip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/driverforge/cli/pkg/c4z/manifest"
)

// writeSrc lays out files under a fresh temp dir (rel slash-path -> content).
func writeSrc(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// packEntries runs Write and returns the archive contents keyed by entry name,
// failing the test on any duplicate entry.
func packEntries(t *testing.T, p Plan) map[string]string {
	t.Helper()
	if err := Write(p); err != nil {
		t.Fatalf("Write: %v", err)
	}
	r, err := zip.OpenReader(p.C4zPath)
	if err != nil {
		t.Fatalf("open %s: %v", p.C4zPath, err)
	}
	defer r.Close()
	entries := map[string]string{}
	for _, f := range r.File {
		if _, dup := entries[f.Name]; dup {
			t.Fatalf("duplicate zip entry %q", f.Name)
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open entry %s: %v", f.Name, err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read entry %s: %v", f.Name, err)
		}
		entries[f.Name] = string(data)
	}
	return entries
}

// requireNoPlaintext fails if any archive entry contains the plaintext marker.
func requireNoPlaintext(t *testing.T, entries map[string]string, marker string) {
	t.Helper()
	for name, content := range entries {
		if strings.Contains(content, marker) {
			t.Fatalf("entry %q contains plaintext source", name)
		}
	}
}

func TestEncryptTargetInsideDirItem(t *testing.T) {
	src := writeSrc(t, map[string]string{
		"src/driver.lua": "SECRET SOURCE",
		"src/helper.lua": "helper",
	})
	entries := packEntries(t, Plan{
		C4zPath:           filepath.Join(t.TempDir(), "out.c4z"),
		SrcDir:            src,
		DriverXML:         []byte("<devicedata/>"),
		Items:             []manifest.Item{{Type: manifest.ItemDir, Name: "src", Recurse: true}},
		EncryptEnabled:    true,
		EncryptTargetBase: "driver.lua",
		Encrypted:         []byte("ENCRYPTED-PAYLOAD"),
	})
	if got := entries["src/driver.lua.encrypted"]; got != "ENCRYPTED-PAYLOAD" {
		t.Fatalf("src/driver.lua.encrypted = %q, want the encrypted payload", got)
	}
	if _, ok := entries["src/driver.lua"]; ok {
		t.Fatal("plaintext src/driver.lua was written alongside the encrypted entry")
	}
	if _, ok := entries["src/helper.lua"]; !ok {
		t.Fatal("unrelated dir-item file missing from archive")
	}
	requireNoPlaintext(t, entries, "SECRET SOURCE")
}

func TestSquishMainInsideDirItem(t *testing.T) {
	src := writeSrc(t, map[string]string{
		"src/driver.lua": "SECRET MAIN",
		"src/lib.lua":    "SECRET MODULE",
	})
	entries := packEntries(t, Plan{
		C4zPath:    filepath.Join(t.TempDir(), "out.c4z"),
		SrcDir:     src,
		DriverXML:  []byte("<devicedata/>"),
		Items:      []manifest.Item{{Type: manifest.ItemDir, Name: "src", Recurse: true}},
		SquishLua:  true,
		MainBase:   "driver.lua",
		Bundle:     []byte("BUNDLED"),
		ModuleBase: map[string]bool{"lib.lua": true},
	})
	if got := entries["src/driver.lua"]; got != "BUNDLED" {
		t.Fatalf("src/driver.lua = %q, want the squished bundle", got)
	}
	if _, ok := entries["src/lib.lua"]; ok {
		t.Fatal("squishy module source was written despite being bundled")
	}
	requireNoPlaintext(t, entries, "SECRET")
}

func TestSquishMainInsideDirItemEncrypted(t *testing.T) {
	src := writeSrc(t, map[string]string{"src/driver.lua": "SECRET MAIN"})
	entries := packEntries(t, Plan{
		C4zPath:           filepath.Join(t.TempDir(), "out.c4z"),
		SrcDir:            src,
		DriverXML:         []byte("<devicedata/>"),
		Items:             []manifest.Item{{Type: manifest.ItemDir, Name: "src", Recurse: true}},
		SquishLua:         true,
		MainBase:          "driver.lua",
		Bundle:            []byte("BUNDLED"),
		ModuleBase:        map[string]bool{},
		EncryptEnabled:    true,
		EncryptTargetBase: "driver.lua",
		Encrypted:         []byte("ENCRYPTED-BUNDLE"),
	})
	if got := entries["src/driver.lua.encrypted"]; got != "ENCRYPTED-BUNDLE" {
		t.Fatalf("src/driver.lua.encrypted = %q, want the encrypted bundle", got)
	}
	if _, ok := entries["src/driver.lua"]; ok {
		t.Fatal("plaintext main was written alongside the encrypted bundle")
	}
	requireNoPlaintext(t, entries, "SECRET")
}

func TestFileAndDirItemOverlapWritesOneEncryptedEntry(t *testing.T) {
	src := writeSrc(t, map[string]string{"driver.lua": "SECRET SOURCE"})
	entries := packEntries(t, Plan{
		C4zPath:   filepath.Join(t.TempDir(), "out.c4z"),
		SrcDir:    src,
		DriverXML: []byte("<devicedata/>"),
		Items: []manifest.Item{
			{Type: manifest.ItemFile, Name: "driver.lua"},
			{Type: manifest.ItemDir, Name: "."},
		},
		EncryptEnabled:    true,
		EncryptTargetBase: "driver.lua",
		Encrypted:         []byte("ENCRYPTED-PAYLOAD"),
	})
	if got := entries["driver.lua.encrypted"]; got != "ENCRYPTED-PAYLOAD" {
		t.Fatalf("driver.lua.encrypted = %q, want the encrypted payload", got)
	}
	if _, ok := entries["driver.lua"]; ok {
		t.Fatal("plaintext driver.lua leaked via the dir item")
	}
	requireNoPlaintext(t, entries, "SECRET SOURCE")
}

func TestMissingEncryptTargetFails(t *testing.T) {
	src := writeSrc(t, map[string]string{"other.lua": "other"})
	c4zPath := filepath.Join(t.TempDir(), "out.c4z")
	err := Write(Plan{
		C4zPath:           c4zPath,
		SrcDir:            src,
		DriverXML:         []byte("<devicedata/>"),
		Items:             []manifest.Item{{Type: manifest.ItemFile, Name: "other.lua"}},
		EncryptEnabled:    true,
		EncryptTargetBase: "driver.lua",
		Encrypted:         []byte("ENCRYPTED-PAYLOAD"),
	})
	var missing *MissingTargetError
	if !errors.As(err, &missing) {
		t.Fatalf("Write error = %v, want MissingTargetError", err)
	}
	if missing.Target != "driver.lua" {
		t.Fatalf("MissingTargetError.Target = %q, want driver.lua", missing.Target)
	}
	if _, statErr := os.Stat(c4zPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("partial archive left behind after failed Write (stat: %v)", statErr)
	}
}

func TestMissingSquishMainFails(t *testing.T) {
	src := writeSrc(t, map[string]string{"src/lib.lua": "module"})
	err := Write(Plan{
		C4zPath:    filepath.Join(t.TempDir(), "out.c4z"),
		SrcDir:     src,
		DriverXML:  []byte("<devicedata/>"),
		Items:      []manifest.Item{{Type: manifest.ItemDir, Name: "src", Recurse: true}},
		SquishLua:  true,
		MainBase:   "driver.lua",
		Bundle:     []byte("BUNDLED"),
		ModuleBase: map[string]bool{"lib.lua": true},
	})
	var missing *MissingTargetError
	if !errors.As(err, &missing) {
		t.Fatalf("Write error = %v, want MissingTargetError", err)
	}
	if missing.Target != "driver.lua" {
		t.Fatalf("MissingTargetError.Target = %q, want driver.lua", missing.Target)
	}
}

// A subdirectory whose name relates to the item's must keep its own path.
// build_c4z.py matched the item name with rfind, which flattened js/objs into
// js/ (a bug in the Control4 tool, deliberately not reproduced).
func TestDirItemPreservesSubdirPaths(t *testing.T) {
	for _, c := range []struct {
		name  string
		item  string
		files map[string]string // relative path -> content, expected verbatim in the archive
	}{
		{
			name: "subdirectory name ends with the item name",
			item: "js",
			files: map[string]string{
				"js/app.lua":      "top",
				"js/objs/app.lua": "nested",
			},
		},
		{
			name: "nested directory repeats the item name",
			item: "www",
			files: map[string]string{
				"www/index.html":            "root",
				"www/assets/www/index.html": "deep",
			},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			entries := packEntries(t, Plan{
				C4zPath:   filepath.Join(t.TempDir(), "out.c4z"),
				SrcDir:    writeSrc(t, c.files),
				DriverXML: []byte("<devicedata/>"),
				Items:     []manifest.Item{{Type: manifest.ItemDir, Name: c.item, Recurse: true}},
			})
			for rel, want := range c.files {
				if got := entries[rel]; got != want {
					t.Errorf("entries[%q] = %q, want %q (flattened?)", rel, got, want)
				}
			}
		})
	}
}

// C4zDir remaps the item root; subdirectory structure follows it.
func TestDirItemC4zDirWithSubdirs(t *testing.T) {
	src := writeSrc(t, map[string]string{
		"web/img/logo.png": "png",
	})
	entries := packEntries(t, Plan{
		C4zPath:   filepath.Join(t.TempDir(), "out.c4z"),
		SrcDir:    src,
		DriverXML: []byte("<devicedata/>"),
		Items:     []manifest.Item{{Type: manifest.ItemDir, Name: "web", C4zDir: "www", Recurse: true}},
	})
	if got := entries["www/img/logo.png"]; got != "png" {
		t.Errorf("www/img/logo.png = %q, want png", got)
	}
	if _, ok := entries["web/img/logo.png"]; ok {
		t.Error("item name must not leak into the archive when C4zDir is set")
	}
}

// Non-recursive dir items include only the top level.
func TestDirItemNonRecursive(t *testing.T) {
	src := writeSrc(t, map[string]string{
		"docs/readme.txt":      "top",
		"docs/deep/hidden.txt": "nested",
	})
	entries := packEntries(t, Plan{
		C4zPath:   filepath.Join(t.TempDir(), "out.c4z"),
		SrcDir:    src,
		DriverXML: []byte("<devicedata/>"),
		Items:     []manifest.Item{{Type: manifest.ItemDir, Name: "docs", Recurse: false}},
	})
	if _, ok := entries["docs/readme.txt"]; !ok {
		t.Error("top-level file missing from non-recursive dir item")
	}
	if _, ok := entries["docs/deep/hidden.txt"]; ok {
		t.Error("non-recursive dir item must not descend into subdirectories")
	}
}

// Two manifest items resolving to the same archive path are refused —
// archive/zip would write both, with extractor-defined results.
func TestDuplicateEntriesRejected(t *testing.T) {
	src := writeSrc(t, map[string]string{
		"a/helper.lua": "from a",
		"b/helper.lua": "from b",
	})
	err := Write(Plan{
		C4zPath:   filepath.Join(t.TempDir(), "out.c4z"),
		SrcDir:    src,
		DriverXML: []byte("<devicedata/>"),
		Items: []manifest.Item{
			{Type: manifest.ItemFile, Name: "a/helper.lua", C4zDir: "lib"},
			{Type: manifest.ItemFile, Name: "b/helper.lua", C4zDir: "lib"},
		},
	})
	var dup *DuplicateEntryError
	if !errors.As(err, &dup) {
		t.Fatalf("error = %v, want DuplicateEntryError", err)
	}
	if dup.Name != "lib/helper.lua" {
		t.Errorf("colliding entry = %q, want lib/helper.lua", dup.Name)
	}
}

// A dir item covering the project root must not duplicate driver.xml — the
// buffer copy is authoritative.
func TestDirItemRootSkipsDriverXML(t *testing.T) {
	src := writeSrc(t, map[string]string{
		"driver.xml": "<devicedata>on disk</devicedata>",
		"driver.lua": "print('x')",
	})
	entries := packEntries(t, Plan{
		C4zPath:   filepath.Join(t.TempDir(), "out.c4z"),
		SrcDir:    src,
		DriverXML: []byte("<devicedata>from buffer</devicedata>"),
		Items:     []manifest.Item{{Type: manifest.ItemDir, Name: ".", Recurse: true}},
	})
	if got := entries["driver.xml"]; got != "<devicedata>from buffer</devicedata>" {
		t.Errorf("driver.xml = %q, want the in-memory buffer copy", got)
	}
	if _, ok := entries["driver.lua"]; !ok {
		t.Error("driver.lua missing from the root dir item")
	}
}
