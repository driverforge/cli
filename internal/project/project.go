// Package project locates a driver project on disk and resolves the
// directories the build needs. It is a shared concern used by the build, init,
// and publish commands.
package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrNotFound is returned by Locate when no driver project root is found in
// start or any of its ancestors. It is an expected outcome of running a
// project command outside a driver project, not a bug, so callers can use
// errors.Is to distinguish it from unexpected failures.
var ErrNotFound = errors.New("no driver project found")

// Project is a located driver project.
type Project struct {
	Dir     string // project root (contains src/)
	SrcDir  string // <Dir>/src
	DistDir string // <Dir>/dist
}

// Locate finds the nearest ancestor of start that is a driver project root and
// resolves its src/ and dist/ directories.
func Locate(start string) (Project, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return Project{}, err
	}
	dir := abs
	for {
		if isProjectRoot(dir) {
			return Project{
				Dir:     dir,
				SrcDir:  filepath.Join(dir, "src"),
				DistDir: filepath.Join(dir, "dist"),
			}, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return Project{}, fmt.Errorf("%w (looked for src/manifest.c4zproj in %s or any parent directory)", ErrNotFound, abs)
		}
		dir = parent
	}
}

// isProjectRoot reports whether dir is a driver project root. A project is
// identified by its src/manifest.c4zproj; a future marker (e.g. a .driverforge/
// project folder) can be added here.
func isProjectRoot(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "src", "manifest.c4zproj"))
	return err == nil
}
