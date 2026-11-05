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
	proj, err := project.Locate(dir)
	if errors.Is(err, project.ErrNotFound) {
		return project.Project{}, clierr.WithCode(clierr.WrapT(err, "No project found",
			"This doesn't look like a driver project.",
			"run `driverforge init` to create one, or cd into an existing project"), codeNotAProject)
	}
	if err != nil {
		return project.Project{}, err
	}
	return proj, nil
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
