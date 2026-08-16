// Package squish reimplements the Lua bundling that the Control4 toolchain
// delegated to the third-party `luajit squish --no-minify`.
//
// A "squishy" file declares a Main entry file and zero or more Modules. The
// bundle concatenates each module wrapped in a package.preload closure followed
// by the main file appended verbatim. This is the exact byte format the upstream
// `squish` tool emits with minification disabled, which Driverforge always uses, so
// the output is interchangeable with it. Minification is a non-goal.
package squish

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Module is a single bundled Lua module.
type Module struct {
	Name string // require-able module name, e.g. "lib.logger"
	Path string // source path relative to the squishy dir (slash-separated)
}

// Squishy is a parsed squishy file.
type Squishy struct {
	Main    string   // entry file, relative to the squishy dir
	Modules []Module // in declaration order
	Output  string   // declared output path (informational)
}

var (
	reMain   = regexp.MustCompile(`^Main\s+"([^"]+)"`)
	reModule = regexp.MustCompile(`^Module\s+"([^"]+)"(?:\s+"([^"]+)")?`)
	reOutput = regexp.MustCompile(`^Output\s+"([^"]+)"`)
	// shebang matches a leading shebang line (only at the very start of input).
	reShebang = regexp.MustCompile(`^#[^\n]*\n`)
)

// ParseFile reads and parses the squishy file at path.
func ParseFile(path string) (Squishy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Squishy{}, fmt.Errorf("read squishy: %w", err)
	}
	return Parse(data), nil
}

// Parse parses squishy directives. Unknown directives are ignored, matching the
// the upstream parser, which only recognises Main/Module/Output.
func Parse(data []byte) Squishy {
	var sq Squishy
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		if m := reMain.FindStringSubmatch(line); m != nil {
			sq.Main = m[1]
			continue
		}
		if m := reModule.FindStringSubmatch(line); m != nil {
			path := m[2]
			if path == "" {
				// 2-arg form: derive the path from the dotted module name.
				path = strings.ReplaceAll(m[1], ".", "/") + ".lua"
			}
			sq.Modules = append(sq.Modules, Module{Name: m[1], Path: path})
			continue
		}
		if m := reOutput.FindStringSubmatch(line); m != nil {
			sq.Output = m[1]
		}
	}
	return sq
}

// Bundle produces the squished Lua bundle from sources rooted at srcDir.
//
// Output format (no minification), per module in declaration order:
//
//	package.preload['<name>'] = (function (...)\n<file bytes verbatim> end)\n
//
// followed by the Main file's contents appended verbatim, with a leading
// shebang line stripped if present. No trailing newline is added.
func Bundle(srcDir string, sq Squishy) ([]byte, error) {
	if sq.Main == "" {
		return nil, fmt.Errorf("squishy: missing Main directive")
	}
	var buf bytes.Buffer
	for _, m := range sq.Modules {
		data, err := os.ReadFile(filepath.Join(srcDir, filepath.FromSlash(m.Path)))
		if err != nil {
			return nil, fmt.Errorf("squish module %q: %w", m.Name, err)
		}
		buf.WriteString("package.preload['")
		buf.WriteString(m.Name)
		buf.WriteString("'] = (function (...)\n")
		buf.Write(data)
		buf.WriteString(" end)\n")
	}
	main, err := os.ReadFile(filepath.Join(srcDir, filepath.FromSlash(sq.Main)))
	if err != nil {
		return nil, fmt.Errorf("squish main %q: %w", sq.Main, err)
	}
	buf.Write(reShebang.ReplaceAll(main, nil))
	return buf.Bytes(), nil
}
