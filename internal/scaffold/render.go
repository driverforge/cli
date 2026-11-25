// Package scaffold fetches a project template from GitHub and renders it into
// a new directory for `driverforge new`.
//
// A template is a directory in a GitHub repository holding the files of a
// driver project. Text files (and file names) may contain ${variable}
// placeholders, which are replaced when the project is created; $${ writes a
// literal ${. The template may carry a template.json describing itself and the
// extra variables it accepts; that file is not copied into the project.
package scaffold

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// ManifestName is the optional file in a template's root that describes it.
const ManifestName = "template.json"

// File is one file of a template, with its path relative to the template root.
type File struct {
	Path string
	Data []byte
	Mode fs.FileMode
}

// Variable is an extra placeholder a template accepts, set with --set.
type Variable struct {
	Description string `json:"description"`
	Default     string `json:"default"`
	Required    bool   `json:"required"`
}

// Manifest is a template's template.json.
type Manifest struct {
	Description string              `json:"description"`
	Variables   map[string]Variable `json:"variables"`
}

// builtin variables are derived from the project name and cannot be redeclared
// or overridden.
var builtin = []string{"name", "slug", "id", "title", "year"}

var (
	placeholderRe = regexp.MustCompile(`\$\$\{|\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
	varNameRe     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// ParseManifest reads a template.json.
func ParseManifest(data []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("scaffold: reading %s: %w", ManifestName, err)
	}
	for name := range m.Variables {
		if !varNameRe.MatchString(name) {
			return Manifest{}, fmt.Errorf("scaffold: %s declares an invalid variable name %q", ManifestName, name)
		}
		for _, b := range builtin {
			if name == b {
				return Manifest{}, fmt.Errorf("scaffold: %s redeclares the built-in variable %q", ManifestName, name)
			}
		}
	}
	return m, nil
}

// Names derives the built-in variables from the name the user typed:
//
//	name   My-Pool_pump   as typed
//	slug   my-pool-pump   lowercase, hyphenated
//	id     my_pool_pump   lowercase, underscored — safe in Lua identifiers and file names
//	title  My Pool Pump   each word capitalised
//	year   2026
//
// A name with no letters or digits is rejected.
func Names(name string, now time.Time) (map[string]string, error) {
	words := strings.FieldsFunc(name, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if len(words) == 0 {
		return nil, fmt.Errorf("scaffold: name %q has no letters or digits", name)
	}
	lower := make([]string, len(words))
	title := make([]string, len(words))
	for i, w := range words {
		lower[i] = strings.ToLower(w)
		r := []rune(lower[i])
		r[0] = unicode.ToUpper(r[0])
		title[i] = string(r)
	}
	return map[string]string{
		"name":  name,
		"slug":  strings.Join(lower, "-"),
		"id":    strings.Join(lower, "_"),
		"title": strings.Join(title, " "),
		"year":  fmt.Sprint(now.Year()),
	}, nil
}

// Resolve merges the built-in variables, the template's defaults and the
// user's --set values. It returns the names still without a value (required
// variables nobody answered) so the caller can ask or fail naming them all.
func Resolve(m Manifest, builtins, set map[string]string) (vars map[string]string, missing []string, err error) {
	vars = map[string]string{}
	for k, v := range builtins {
		vars[k] = v
	}
	for k, v := range set {
		if _, ok := m.Variables[k]; !ok {
			return nil, nil, fmt.Errorf("scaffold: this template has no variable %q (it accepts: %s)", k, strings.Join(declared(m), ", "))
		}
		vars[k] = v
	}
	for _, k := range declared(m) {
		if _, ok := vars[k]; ok {
			continue
		}
		v := m.Variables[k]
		switch {
		case v.Default != "":
			vars[k] = v.Default
		case v.Required:
			missing = append(missing, k)
		default:
			vars[k] = ""
		}
	}
	return vars, missing, nil
}

// declared lists the template's variable names, sorted.
func declared(m Manifest) []string {
	names := make([]string, 0, len(m.Variables))
	for k := range m.Variables {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// Declared lists the extra variable names a template accepts, sorted.
func (m Manifest) Declared() []string { return declared(m) }

// Substitute replaces ${var} placeholders in s. An unknown variable is an error
// rather than an empty string: a typo in a template should fail loudly.
func Substitute(s string, vars map[string]string) (string, error) {
	var bad string
	out := placeholderRe.ReplaceAllStringFunc(s, func(m string) string {
		if m == "$${" {
			return "${"
		}
		name := m[2 : len(m)-1]
		v, ok := vars[name]
		if !ok && bad == "" {
			bad = name
		}
		return v
	})
	if bad != "" {
		return "", fmt.Errorf("unknown variable ${%s}", bad)
	}
	return out, nil
}

// Render substitutes variables into every file's path and, for text files, its
// contents. Binary files (any NUL byte) are copied untouched. The manifest is
// dropped. Rendered paths must stay inside the project.
func Render(files []File, vars map[string]string) ([]File, error) {
	out := make([]File, 0, len(files))
	seen := map[string]string{}
	for _, f := range files {
		if f.Path == ManifestName {
			continue
		}
		p, err := Substitute(f.Path, vars)
		if err != nil {
			return nil, fmt.Errorf("scaffold: file name %q: %w", f.Path, err)
		}
		if !safeRel(p) {
			return nil, fmt.Errorf("scaffold: file name %q renders to %q, which is not a path inside the project", f.Path, p)
		}
		if prev, dup := seen[p]; dup {
			return nil, fmt.Errorf("scaffold: %q and %q both render to %q", prev, f.Path, p)
		}
		seen[p] = f.Path
		data := f.Data
		if !bytes.Contains(data, []byte{0}) {
			s, err := Substitute(string(data), vars)
			if err != nil {
				return nil, fmt.Errorf("scaffold: %s: %w", f.Path, err)
			}
			data = []byte(s)
		}
		out = append(out, File{Path: p, Data: data, Mode: f.Mode})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// safeRel reports whether p is a clean, relative, forward-slash path with no
// empty or dot segments.
func safeRel(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, `\`) || strings.Contains(p, "\x00") {
		return false
	}
	if path.Clean(p) != p {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." || seg == "." || seg == "" {
			return false
		}
	}
	return true
}
