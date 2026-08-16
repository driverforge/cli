package cli

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/driverforge/cli/internal/initdriver"
)

func mustWrite(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	// WriteFile's perm is umask-masked on create; pin the mode exactly.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s should not exist (stat err: %v)", path, err)
	}
}

// failingChange returns a change guaranteed to fail: its parent "directory" is
// an existing regular file, so MkdirAll refuses.
func failingChange(t *testing.T, root string) initdriver.Change {
	t.Helper()
	blocker := filepath.Join(root, "blocker")
	mustWrite(t, blocker, "not a dir", 0o644)
	return initdriver.Change{
		RelPath: "blocker/never.lua",
		AbsPath: filepath.Join(blocker, "never.lua"),
		Kind:    initdriver.Create,
		New:     []byte("x"),
	}
}

func TestApplyChangesSuccessAppliesAndRemovesBackups(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "driver.lua")
	mustWrite(t, target, "old", 0o644)

	err := applyChanges([]initdriver.Change{
		{RelPath: "driver.lua", AbsPath: target, Kind: initdriver.Modify, Old: []byte("old"), New: []byte("new")},
		{RelPath: "vendor/sdk.lua", AbsPath: filepath.Join(root, "vendor", "sdk.lua"), Kind: initdriver.Create, New: []byte("sdk")},
	})
	if err != nil {
		t.Fatalf("applyChanges: %v", err)
	}
	if got := mustRead(t, target); got != "new" {
		t.Errorf("driver.lua = %q, want new", got)
	}
	if got := mustRead(t, filepath.Join(root, "vendor", "sdk.lua")); got != "sdk" {
		t.Errorf("vendor/sdk.lua = %q, want sdk", got)
	}
	mustNotExist(t, target+".bak")
}

// The core case: a write that fails after the backup was taken must
// restore this file from its backup, not orphan it — the undo record is
// appended before the truncating write.
func TestApplyChangesFailedWriteRestoresBackup(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "driver.lua")
	mustWrite(t, target, "precious", 0o444) // read-only: the write itself fails

	err := applyChanges([]initdriver.Change{
		{RelPath: "driver.lua", AbsPath: target, Kind: initdriver.Modify, Old: []byte("precious"), New: []byte("patched")},
	})
	if err == nil {
		t.Fatal("applyChanges should fail writing a read-only file")
	}
	if got := mustRead(t, target); got != "precious" {
		t.Errorf("driver.lua = %q, want original content restored", got)
	}
	mustNotExist(t, target+".bak")
}

func TestApplyChangesRollbackRestoresContentAndMode(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "driver.lua")
	mustWrite(t, target, "original", 0o755) // executable, to prove mode survives

	err := applyChanges([]initdriver.Change{
		{RelPath: "driver.lua", AbsPath: target, Kind: initdriver.Modify, Old: []byte("original"), New: []byte("patched")},
		failingChange(t, root),
	})
	if err == nil {
		t.Fatal("applyChanges should fail on the blocked change")
	}
	if got := mustRead(t, target); got != "original" {
		t.Errorf("driver.lua = %q, want original content restored", got)
	}
	info, statErr := os.Stat(target)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("driver.lua mode = %v, want 0755 restored", info.Mode().Perm())
	}
	mustNotExist(t, target+".bak")
}

func TestApplyChangesRollbackRemovesCreatedFilesAndDirs(t *testing.T) {
	root := t.TempDir()
	created := filepath.Join(root, "src", "vendor", "sdk.lua")

	err := applyChanges([]initdriver.Change{
		{RelPath: "src/vendor/sdk.lua", AbsPath: created, Kind: initdriver.Create, New: []byte("sdk")},
		failingChange(t, root),
	})
	if err == nil {
		t.Fatal("applyChanges should fail on the blocked change")
	}
	mustNotExist(t, created)
	mustNotExist(t, filepath.Join(root, "src", "vendor"))
	mustNotExist(t, filepath.Join(root, "src"))
	// The pre-existing root must survive.
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("project root removed by rollback: %v", err)
	}
}

func TestApplyChangesPreservesUsersOwnBakFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "driver.lua")
	mustWrite(t, target, "original", 0o644)
	mustWrite(t, target+".bak", "the user's own backup", 0o644)

	// Failure path: rollback must restore from .bak2 and leave the user's .bak alone.
	err := applyChanges([]initdriver.Change{
		{RelPath: "driver.lua", AbsPath: target, Kind: initdriver.Modify, Old: []byte("original"), New: []byte("patched")},
		failingChange(t, root),
	})
	if err == nil {
		t.Fatal("applyChanges should fail on the blocked change")
	}
	if got := mustRead(t, target); got != "original" {
		t.Errorf("driver.lua = %q, want original content restored", got)
	}
	if got := mustRead(t, target+".bak"); got != "the user's own backup" {
		t.Errorf("user's .bak = %q, want untouched", got)
	}
	mustNotExist(t, target+".bak2")

	// Success path: same guarantee, and the transient .bak2 is cleaned up.
	if err := applyChanges([]initdriver.Change{
		{RelPath: "driver.lua", AbsPath: target, Kind: initdriver.Modify, Old: []byte("original"), New: []byte("patched")},
	}); err != nil {
		t.Fatalf("applyChanges: %v", err)
	}
	if got := mustRead(t, target); got != "patched" {
		t.Errorf("driver.lua = %q, want patched", got)
	}
	if got := mustRead(t, target+".bak"); got != "the user's own backup" {
		t.Errorf("user's .bak = %q, want untouched", got)
	}
	mustNotExist(t, target+".bak2")
}
