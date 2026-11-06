// Package skills holds the agent skills the CLI ships and installs them into a
// coding agent's skills directory. A skill is a folder of Markdown an agent
// loads on demand: SKILL.md (frontmatter plus the main instructions) and the
// references and examples it points to. They are embedded in the binary so the
// installed skill always describes the CLI that installed it.
package skills

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

//go:embed all:anvil-driver-dev-cycle
var content embed.FS

// Marker is the file Install writes into every skill it installs. It proves a
// directory is one this CLI owns, so a reinstall may replace it.
const Marker = ".driverforge-skill.json"

// versionPlaceholder in a skill's files becomes the installing CLI's version.
const versionPlaceholder = "{{cli_version}}"

// Skill is one shipped skill.
type Skill struct {
	Name        string
	Description string
}

// ErrUnknown is returned for a skill name the CLI doesn't ship.
var ErrUnknown = errors.New("skills: no such skill")

// ErrNotOwned is returned when the target directory exists but wasn't
// installed by the CLI.
var ErrNotOwned = errors.New("skills: directory exists and wasn't installed by driverforge")

// List returns the shipped skills, sorted by name.
func List() ([]Skill, error) {
	dirs, err := fs.ReadDir(content, ".")
	if err != nil {
		return nil, fmt.Errorf("skills: %w", err)
	}
	var out []Skill
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		s, err := read(d.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b Skill) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// Files returns a skill's files, path relative to the skill, with the version
// placeholder replaced by cliVersion.
func Files(name, cliVersion string) (map[string][]byte, error) {
	if _, err := fs.Stat(content, path.Join(name, "SKILL.md")); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrUnknown, name)
	}
	files := map[string][]byte{}
	err := fs.WalkDir(content, name, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := content.ReadFile(p)
		if err != nil {
			return err
		}
		files[strings.TrimPrefix(p, name+"/")] = bytes.ReplaceAll(b, []byte(versionPlaceholder), []byte(cliVersion))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("skills: reading %s: %w", name, err)
	}
	return files, nil
}

// Action is what Install did to a skill's directory.
type Action string

const (
	Installed Action = "installed" // the directory didn't exist
	Updated   Action = "updated"   // it existed and its files changed
	Unchanged Action = "unchanged" // it already matched
)

// Result is one skill's install.
type Result struct {
	Name   string
	Path   string
	Files  int
	Action Action
}

type marker struct {
	Skill      string `json:"skill"`
	CLIVersion string `json:"cli_version"`
}

// Install writes the named skill into root/name, replacing a copy a previous
// install left there. A directory without the marker belongs to someone else
// and is left alone unless force is set.
func Install(root, name, cliVersion string, force bool) (Result, error) {
	files, err := Files(name, cliVersion)
	if err != nil {
		return Result{}, err
	}
	m, err := json.MarshalIndent(marker{Skill: name, CLIVersion: cliVersion}, "", "  ")
	if err != nil {
		return Result{}, fmt.Errorf("skills: %w", err)
	}
	files[Marker] = append(m, '\n')

	dest := filepath.Join(root, name)
	res := Result{Name: name, Path: dest, Files: len(files) - 1, Action: Installed}
	if _, err := os.Stat(dest); err == nil {
		if _, err := os.Stat(filepath.Join(dest, Marker)); err != nil && !force {
			return res, fmt.Errorf("%w: %s", ErrNotOwned, dest)
		}
		if same(dest, files) {
			res.Action = Unchanged
			return res, nil
		}
		res.Action = Updated
		if err := os.RemoveAll(dest); err != nil {
			return res, fmt.Errorf("skills: replacing %s: %w", dest, err)
		}
	}
	for rel, b := range files {
		p := filepath.Join(dest, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return res, fmt.Errorf("skills: %w", err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return res, fmt.Errorf("skills: %w", err)
		}
	}
	return res, nil
}

// InstalledIn returns the skills Install wrote into root: the directories whose
// marker names them. A missing root has none.
func InstalledIn(root string) ([]string, error) {
	dirs, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("skills: %w", err)
	}
	var names []string
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, d.Name(), Marker))
		if err != nil {
			continue
		}
		var m marker
		if json.Unmarshal(b, &m) == nil && m.Skill == d.Name() {
			names = append(names, d.Name())
		}
	}
	return names, nil
}

// same reports whether dir holds exactly files.
func same(dir string, files map[string][]byte) bool {
	n := 0
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		want, ok := files[filepath.ToSlash(rel)]
		if !ok {
			return fs.ErrNotExist
		}
		got, err := os.ReadFile(p)
		if err != nil || !bytes.Equal(got, want) {
			return fs.ErrInvalid
		}
		n++
		return nil
	})
	return err == nil && n == len(files)
}

// read parses a skill's name and description from its SKILL.md frontmatter.
func read(name string) (Skill, error) {
	b, err := content.ReadFile(path.Join(name, "SKILL.md"))
	if err != nil {
		return Skill{}, fmt.Errorf("skills: %w", err)
	}
	s := Skill{}
	front, ok := frontmatter(string(b))
	if !ok {
		return Skill{}, fmt.Errorf("skills: %s/SKILL.md has no frontmatter", name)
	}
	for _, line := range strings.Split(front, "\n") {
		k, v, found := strings.Cut(line, ":")
		if !found || strings.HasPrefix(line, " ") {
			continue
		}
		switch k {
		case "name":
			s.Name = strings.TrimSpace(v)
		case "description":
			s.Description = strings.TrimSpace(v)
		}
	}
	if s.Name != name {
		return Skill{}, fmt.Errorf("skills: %s/SKILL.md names itself %q", name, s.Name)
	}
	return s, nil
}

func frontmatter(doc string) (string, bool) {
	rest, ok := strings.CutPrefix(doc, "---\n")
	if !ok {
		return "", false
	}
	front, _, ok := strings.Cut(rest, "\n---\n")
	return front, ok
}
