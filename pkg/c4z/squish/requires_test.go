package squish

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanRequires(t *testing.T) {
	src := `
local a = require('lib.actions')
local b = require "lib.util"
local c = require ('lib.spaced')
-- local dead = require('lib.commented')
--[[
local dead2 = require('lib.blockcommented')
]]
local s = "not a require('lib.instring')"
local d = foo.require('lib.method')
local e = foo:require('lib.colon')
local f = require(modname)
local g = require('lib.dup')
local h = require('lib.dup')
`
	refs := scanRequires([]byte(src))
	want := map[string]int{
		"lib.actions": 2,
		"lib.util":    3,
		"lib.spaced":  4,
		"lib.dup":     13, // scanRequires reports every occurrence; dedupe is MissingRequires' job
	}
	got := map[string]int{}
	for _, r := range refs {
		if _, dup := got[r.name]; !dup {
			got[r.name] = r.line
		}
	}
	if len(refs) != 5 { // the four above + the second lib.dup
		t.Fatalf("got %d refs (%v), want 5", len(refs), refs)
	}
	for name, line := range want {
		if got[name] != line {
			t.Errorf("%s: line %d, want %d", name, got[name], line)
		}
	}
}

func TestScanRequiresStringWithCommentMarker(t *testing.T) {
	// A -- inside a string must not swallow the rest of the line.
	src := `local s = "--" local a = require('lib.after')`
	refs := scanRequires([]byte(src))
	if len(refs) != 1 || refs[0].name != "lib.after" {
		t.Fatalf("got %v, want lib.after", refs)
	}
}

func TestMissingRequires(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("driver.lua", `
local declared = require('lib.declared')
local missing = require('lib.missing')
local runtime = require('openssl')
local pkg = require('lib.pkg')
`)
	write("lib/declared.lua", "return {}\n")
	write("lib/missing.lua", "local nested = require('lib.nested')\nreturn {}\n")
	write("lib/nested.lua", "return {}\n")
	write("lib/pkg/init.lua", "return {}\n")

	sq := Squishy{
		Main: "driver.lua",
		Modules: []Module{
			{Name: "lib.declared", Path: "lib/declared.lua"},
			// lib.missing is bundled here so its own requires are scanned too,
			// then omitted from a second run below.
			{Name: "lib.missing", Path: "lib/missing.lua"},
		},
	}
	got := MissingRequires(dir, sq)
	want := []MissingRequire{
		{Module: "lib.pkg", File: "driver.lua", Line: 5},
		{Module: "lib.nested", File: "lib/missing.lua", Line: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] got %v, want %v", i, got[i], want[i])
		}
	}

	// Drop lib.missing from the manifest: it is now itself reported (its file
	// exists), and its no-longer-bundled source is no longer scanned, so
	// lib.nested disappears.
	sq.Modules = sq.Modules[:1]
	got = MissingRequires(dir, sq)
	want = []MissingRequire{
		{Module: "lib.missing", File: "driver.lua", Line: 3},
		{Module: "lib.pkg", File: "driver.lua", Line: 5},
	}
	if len(got) != len(want) {
		t.Fatalf("after dropping lib.missing: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("after dropping lib.missing: [%d] got %v, want %v", i, got[i], want[i])
		}
	}
}

func TestMissingRequiresRuntimeOnlyStaysQuiet(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "driver.lua"),
		[]byte("local openssl = require 'openssl'\nlocal socket = require('socket')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := MissingRequires(dir, Squishy{Main: "driver.lua"}); len(got) != 0 {
		t.Fatalf("runtime-provided requires reported: %v", got)
	}
}
