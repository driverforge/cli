package squish

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBundleFormat(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "config.lua", "return 1\n")
	writeFile(t, dir, "lib/util.lua", "local M = {}\nreturn M\n")
	writeFile(t, dir, "driver.lua", "print('hi')\n")

	sq := Squishy{
		Main: "driver.lua",
		Modules: []Module{
			{Name: "config", Path: "config.lua"},
			{Name: "lib.util", Path: "lib/util.lua"},
		},
	}
	got, err := Bundle(dir, sq)
	if err != nil {
		t.Fatal(err)
	}
	want := "package.preload['config'] = (function (...)\nreturn 1\n end)\n" +
		"package.preload['lib.util'] = (function (...)\nlocal M = {}\nreturn M\n end)\n" +
		"print('hi')\n"
	if string(got) != want {
		t.Errorf("bundle mismatch:\n--- want ---\n%q\n--- got ---\n%q", want, got)
	}
}

func TestBundleStripsShebang(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "driver.lua", "#!/usr/bin/env lua\nprint('hi')\n")
	got, err := Bundle(dir, Squishy{Main: "driver.lua"})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "print('hi')\n" {
		t.Errorf("shebang not stripped: %q", got)
	}
}

func TestBundleMissingMain(t *testing.T) {
	if _, err := Bundle(t.TempDir(), Squishy{}); err == nil {
		t.Error("expected error when Main is empty")
	}
}

func TestParse(t *testing.T) {
	in := `-- a comment

Main "driver.lua"
Module "config" "config.lua"
Module "lib.dotted"
Output "../dist/out.lua"
`
	sq := Parse([]byte(in))
	if sq.Main != "driver.lua" {
		t.Errorf("Main = %q", sq.Main)
	}
	if sq.Output != "../dist/out.lua" {
		t.Errorf("Output = %q", sq.Output)
	}
	if len(sq.Modules) != 2 {
		t.Fatalf("got %d modules, want 2", len(sq.Modules))
	}
	if sq.Modules[0] != (Module{Name: "config", Path: "config.lua"}) {
		t.Errorf("module[0] = %+v", sq.Modules[0])
	}
	// 2-arg form derives the path from the dotted name.
	if sq.Modules[1] != (Module{Name: "lib.dotted", Path: "lib/dotted.lua"}) {
		t.Errorf("module[1] = %+v", sq.Modules[1])
	}
}
