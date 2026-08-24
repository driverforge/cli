package squish

import (
	"os"
	"path/filepath"
	"strings"
)

// MissingRequire is a require whose target module exists as a file under the
// source tree but has no Module entry in the squishy manifest. The bundle
// packages without it, so the driver loads and then dies at the require — on
// a controller, in the field.
//
// Only requires that resolve to a local file are reported. A name with no
// matching file (e.g. `require 'openssl'`) is assumed to come from the
// controller's runtime, which the packager cannot see and must not guess at.
type MissingRequire struct {
	Module string // the required name, as written in the source
	File   string // file containing the require, relative to srcDir (slash-separated)
	Line   int    // 1-based line of the require
}

// MissingRequires scans the files the squishy bundles (Main plus every Module)
// for require calls whose target resolves to a Lua file under srcDir but is
// not declared in sq. Each missing module is reported once, at its first
// occurrence. Files that cannot be read are skipped here — Bundle owns
// reporting those.
func MissingRequires(srcDir string, sq Squishy) []MissingRequire {
	declared := make(map[string]bool, len(sq.Modules))
	for _, m := range sq.Modules {
		declared[m.Name] = true
	}
	files := make([]string, 0, len(sq.Modules)+1)
	if sq.Main != "" {
		files = append(files, sq.Main)
	}
	for _, m := range sq.Modules {
		files = append(files, m.Path)
	}
	seen := make(map[string]bool)
	var out []MissingRequire
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(srcDir, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		for _, ref := range scanRequires(data) {
			if declared[ref.name] || seen[ref.name] {
				continue
			}
			if !moduleFileExists(srcDir, ref.name) {
				continue
			}
			seen[ref.name] = true
			out = append(out, MissingRequire{Module: ref.name, File: rel, Line: ref.line})
		}
	}
	return out
}

// moduleFileExists reports whether the required name resolves to a Lua file
// under srcDir, trying the two shapes Lua's loader would: <name>.lua (dots act
// as path separators) and <name>/init.lua.
func moduleFileExists(srcDir, name string) bool {
	rel := strings.ReplaceAll(name, ".", "/")
	if strings.Contains(rel, "..") {
		return false
	}
	for _, candidate := range []string{rel + ".lua", rel + "/init.lua"} {
		if st, err := os.Stat(filepath.Join(srcDir, filepath.FromSlash(candidate))); err == nil && !st.IsDir() {
			return true
		}
	}
	return false
}

// requireRef is one require call found in a source file.
type requireRef struct {
	name string
	line int
}

// scanRequires extracts the literal string argument of every require call in
// Lua source: require("x"), require('x'), require "x", require 'x'. Comments
// and unrelated string literals are skipped, so a commented-out require or the
// word require inside a string never matches; method-style access
// (foo.require, foo:require) and dynamic arguments are ignored.
func scanRequires(src []byte) []requireRef {
	var out []requireRef
	line := 1
	for i, n := 0, len(src); i < n; {
		switch c := src[i]; {
		case c == '\n':
			line++
			i++
		case c == '-' && i+1 < n && src[i+1] == '-':
			i += 2
			if lvl, w := longBracketOpen(src[i:]); w > 0 {
				i = skipLongBracket(src, i+w, lvl, &line)
			} else {
				for i < n && src[i] != '\n' {
					i++
				}
			}
		case c == '"' || c == '\'':
			i = skipShortString(src, i, &line)
		case c == '[':
			if lvl, w := longBracketOpen(src[i:]); w > 0 {
				i = skipLongBracket(src, i+w, lvl, &line)
			} else {
				i++
			}
		case isIdentStart(c):
			start := i
			for i < n && isIdentPart(src[i]) {
				i++
			}
			if string(src[start:i]) != "require" ||
				(start > 0 && (src[start-1] == '.' || src[start-1] == ':')) {
				continue
			}
			if name, next, ok := requireArg(src, i); ok {
				out = append(out, requireRef{name: name, line: line})
				i = next
			}
		default:
			i++
		}
	}
	return out
}

// requireArg matches the argument following a require keyword ending at i:
// optional horizontal whitespace, an optional opening paren, then a literal
// short string with no escapes. ok is false for anything else (a dynamic
// argument, a long-string argument, a line break before the string).
func requireArg(src []byte, i int) (name string, next int, ok bool) {
	n := len(src)
	for i < n && (src[i] == ' ' || src[i] == '\t') {
		i++
	}
	if i < n && src[i] == '(' {
		i++
		for i < n && (src[i] == ' ' || src[i] == '\t') {
			i++
		}
	}
	if i >= n || (src[i] != '"' && src[i] != '\'') {
		return "", 0, false
	}
	q := src[i]
	i++
	start := i
	for i < n && src[i] != q && src[i] != '\n' && src[i] != '\\' {
		i++
	}
	if i >= n || src[i] != q || i == start {
		return "", 0, false
	}
	return string(src[start:i]), i + 1, true
}

// longBracketOpen matches a long-bracket opener [[, [=[, [==[ … at the start
// of b, returning its level and the opener's width (0 when b doesn't open one).
func longBracketOpen(b []byte) (level, width int) {
	if len(b) == 0 || b[0] != '[' {
		return 0, 0
	}
	i := 1
	for i < len(b) && b[i] == '=' {
		i++
	}
	if i < len(b) && b[i] == '[' {
		return i - 1, i + 1
	}
	return 0, 0
}

// skipLongBracket advances past a long string/comment body opened at i (just
// after the opener) until the matching ]=*] closer, counting newlines into line.
func skipLongBracket(src []byte, i, level int, line *int) int {
	closer := "]" + strings.Repeat("=", level) + "]"
	for n := len(src); i < n; i++ {
		if src[i] == '\n' {
			*line++
			continue
		}
		if src[i] == ']' && i+len(closer) <= n && string(src[i:i+len(closer)]) == closer {
			return i + len(closer)
		}
	}
	return len(src)
}

// skipShortString advances past a quoted string starting at i (the opening
// quote), honouring backslash escapes. Lua short strings don't span raw
// newlines, so an unterminated string ends at the line break.
func skipShortString(src []byte, i int, line *int) int {
	q := src[i]
	i++
	for n := len(src); i < n; i++ {
		switch src[i] {
		case '\\':
			if i+1 < n {
				if src[i+1] == '\n' {
					*line++
				}
				i++
			}
		case '\n':
			*line++
			return i + 1
		case q:
			return i + 1
		}
	}
	return len(src)
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}
