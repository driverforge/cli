// Package project locates a driver project on disk and resolves the
// directories the build needs. It is a shared concern used by the build, init,
// and publish commands.
package project

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ErrNotFound is returned by Locate when no driver project root is found in
// start or any of its ancestors. It is an expected outcome of running a
// project command outside a driver project, not a bug, so callers can use
// errors.Is to distinguish it from unexpected failures.
var ErrNotFound = errors.New("no driver project found")

// ErrAmbiguous is returned when a directory contains more than one possible
// driver manifest. Callers should ask the user to select one explicitly.
var ErrAmbiguous = errors.New("multiple driver manifests found")

// Project is a located driver project.
type Project struct {
	Dir           string   // project root
	SrcDir        string   // directory containing the selected manifest
	DistDir       string   // <Dir>/dist
	ManifestPath  string   // selected .c4zproj manifest
	ManifestPaths []string // all manifests in SrcDir, for init to keep compatible builds aligned
}

// LocateForInit finds a project for initialization. Compatibility manifests in
// one source directory are initialized together; manifests in different source
// directories remain ambiguous because they may be separate drivers.
func LocateForInit(start string) (Project, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return Project{}, err
	}
	for dir := abs; ; dir = filepath.Dir(dir) {
		if candidates, err := manifests(dir); err != nil {
			if !errors.Is(err, fs.ErrPermission) {
				return Project{}, err
			}
		} else if len(candidates) != 0 {
			src := filepath.Dir(candidates[0])
			for _, candidate := range candidates[1:] {
				if filepath.Dir(candidate) != src {
					return Project{}, fmt.Errorf("%w: %s", ErrAmbiguous, strings.Join(candidates, ", "))
				}
			}
			return fromManifests(dir, candidates), nil
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return Project{}, fmt.Errorf("%w (looked for a .c4zproj file in %s or its parent directories)", ErrNotFound, abs)
}

// Locate finds the nearest ancestor of start that contains a driver manifest.
// A manifest in the root takes precedence. Otherwise, manifests in immediate
// child directories are considered, preserving the conventional src/ layout
// while supporting root-level and named source directories.
func Locate(start string) (Project, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return Project{}, err
	}
	for dir := abs; ; dir = filepath.Dir(dir) {
		if candidates, err := manifests(dir); err != nil {
			if !errors.Is(err, fs.ErrPermission) {
				return Project{}, err
			}
		} else if len(candidates) == 1 {
			return fromManifest(dir, candidates[0]), nil
		} else if len(candidates) > 1 {
			return Project{}, fmt.Errorf("%w: %s", ErrAmbiguous, strings.Join(candidates, ", "))
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return Project{}, fmt.Errorf("%w (looked for a .c4zproj file in %s or its parent directories)", ErrNotFound, abs)
}

// LocateManifest resolves an explicitly selected manifest relative to start.
func LocateManifest(start, manifestPath string) (Project, error) {
	absStart, err := filepath.Abs(start)
	if err != nil {
		return Project{}, err
	}
	path := manifestPath
	if !filepath.IsAbs(path) {
		path = filepath.Join(absStart, path)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return Project{}, err
	}
	if filepath.Ext(path) != ".c4zproj" {
		return Project{}, fmt.Errorf("manifest must have a .c4zproj extension: %s", manifestPath)
	}
	if info, err := os.Stat(path); err != nil {
		return Project{}, fmt.Errorf("reading manifest %s: %w", manifestPath, err)
	} else if info.IsDir() {
		return Project{}, fmt.Errorf("manifest is a directory: %s", manifestPath)
	}
	src := filepath.Dir(path)
	root := src
	for dir := absStart; ; dir = filepath.Dir(dir) {
		if src == dir || filepath.Dir(src) == dir {
			root = dir
			break
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	paths, err := manifestFiles(src)
	if err != nil {
		return Project{}, err
	}
	return Project{Dir: root, SrcDir: src, DistDir: filepath.Join(root, "dist"), ManifestPath: path, ManifestPaths: paths}, nil
}

func manifests(dir string) ([]string, error) {
	root, err := manifestFiles(dir)
	if err != nil || len(root) != 0 {
		return root, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading project directory: %w", err)
	}
	var found []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		files, err := manifestFiles(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		found = append(found, files...)
	}
	sort.Strings(found)
	return found, nil
}

func manifestFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading project directory: %w", err)
	}
	var found []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".c4zproj") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("reading manifest: %w", err)
		}
		if info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 {
			found = append(found, filepath.Join(dir, entry.Name()))
		}
	}
	sort.Strings(found)
	return found, nil
}

func fromManifest(root, path string) Project {
	return fromManifests(root, []string{path})
}

func fromManifests(root string, paths []string) Project {
	path := paths[0]
	src := filepath.Dir(path)
	if root == src && filepath.Base(src) == "src" {
		root = filepath.Dir(src)
	}
	return Project{Dir: root, SrcDir: src, DistDir: filepath.Join(root, "dist"), ManifestPath: path, ManifestPaths: paths}
}
