package cli

import (
	"errors"

	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/driverforgeconfig"
	"github.com/driverforge/cli/internal/project"
)

// requireProject locates the driver project rooted at dir. It translates the
// expected "this isn't a driver project" case into a friendly clierr.UserError
// (rendered nicely, never reported to Sentry); any other failure is returned
// as-is so it surfaces as an unexpected error.
func requireProject(dir string) (project.Project, error) {
	return requireProjectManifest(dir, "")
}

// requireProjectManifest locates a driver project, optionally using the given
// manifest to choose between multiple compatible driver definitions.
func requireProjectManifest(dir, manifestPath string) (project.Project, error) {
	var (
		proj project.Project
		err  error
	)
	if manifestPath != "" {
		proj, err = project.LocateManifest(dir, manifestPath)
	} else {
		proj, err = project.Locate(dir)
	}
	if errors.Is(err, project.ErrNotFound) {
		return project.Project{}, clierr.WithCode(clierr.WrapT(err, "No project found",
			"This doesn't look like a driver project.",
			"run `driverforge init` to create one, or cd into an existing project"), codeNotAProject)
	}
	if errors.Is(err, project.ErrAmbiguous) {
		return project.Project{}, clierr.WithCode(clierr.UserT("Multiple driver manifests found",
			"This project has more than one .c4zproj file.",
			"Pass --c4zproj <path> to select the driver to use."), codeUsage)
	}
	if err != nil {
		if manifestPath != "" {
			return project.Project{}, clierr.WithCode(clierr.UserT("Invalid driver manifest",
				"couldn't use "+manifestPath,
				err.Error()), codeUsage)
		}
		return project.Project{}, err
	}
	return proj, nil
}

func requireProjectForInit(dir, manifestPath string) (project.Project, error) {
	if manifestPath != "" {
		return requireProjectManifest(dir, manifestPath)
	}
	proj, err := project.LocateForInit(dir)
	if err == nil {
		return proj, nil
	}
	if errors.Is(err, project.ErrNotFound) {
		return project.Project{}, clierr.WithCode(clierr.WrapT(err, "No project found",
			"No .c4zproj manifest was found in this directory or its parents.",
			"run `driverforge init` from the directory containing driver.lua and a .c4zproj manifest"), codeNotAProject)
	}
	if errors.Is(err, project.ErrAmbiguous) {
		return project.Project{}, clierr.WithCode(clierr.UserT("Multiple driver source directories found",
			"This directory contains .c4zproj files in more than one source directory.",
			"Pass --c4zproj <path> to select the driver to set up."), codeUsage)
	}
	return project.Project{}, err
}

// requireInitialised loads the project's committed .driverforge/config.json,
// translating its absence into a friendly "run `driverforge init`" card. Commands
// that manage the driver's version (deploy, sync, build --increment) require
// it; plain `driverforge build` deliberately does not, so any driverpackager-style
// tree stays buildable without an account.
func requireInitialised(projectRoot string) (*driverforgeconfig.Config, error) {
	cfg, err := driverforgeconfig.Load(projectRoot)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, &clierr.UserError{
			Code:    codeNotInitialised,
			Title:   "Not set up for Driverforge",
			Message: "this driver hasn't been initialised with Driverforge yet",
			Hint:    "run `driverforge init` first",
		}
	}
	return cfg, nil
}
