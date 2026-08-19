package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/driverforge/cli/internal/driverforgeapi"
	"github.com/driverforge/cli/internal/driverforgeconfig"
	"github.com/driverforge/cli/internal/initdriver"
	"github.com/driverforge/cli/internal/orgctx"
	"github.com/driverforge/cli/internal/project"
	"github.com/driverforge/cli/internal/sdkrelease"
	"github.com/driverforge/cli/internal/ui"
)

// buildInitChanges computes the file change set for a project and its token.
// Review and execute both call it: linking an existing project passes the same
// token to each, so the reviewed diff is exactly what gets written. Creating one
// passes a placeholder for review, since the token does not exist until the
// project does.
func buildInitChanges(proj project.Project, dp driverPlan, org orgctx.Org, linked driverforgeapi.Project, projectToken string) (*initdriver.Result, error) {
	plan, err := initdriver.Plan(proj, initdriver.Decision{
		SDKBundle:    dp.bundle,
		SDKVersion:   dp.version,
		ProjectToken: projectToken,
		BuildTargets: dp.buildTargets,
		Targets:      dp.targets,
		LoggerGlobal: dp.logger,
	})
	if err != nil {
		return nil, err
	}
	cfgBytes, err := driverforgeconfig.Marshal(driverforgeconfig.Config{
		Org:          org.Slug,
		Project:      driverforgeconfig.Project{Slug: linked.Slug, Name: linked.Name},
		SDKVersion:   dp.version,
		BuildTargets: dp.targets,
		Logger:       dp.logger,
		Entry:        dp.entryRel,
		Versioning:   dp.scheme,
	})
	if err != nil {
		return nil, err
	}
	ch, ok, err := fileChange(proj.Dir, driverforgeconfig.Path(proj.Dir), cfgBytes)
	if err != nil {
		return nil, err
	}
	if ok {
		plan.Changes = append(plan.Changes, ch)
	}
	return plan, nil
}

// reviewableChanges splits the plan into the changes worth reviewing (the user's
// own files) and whether the SDK bundle is among them. The SDK is a vendored
// blob — applied, but not shown in the reviewer.
func reviewableChanges(changes []initdriver.Change) (reviewable []initdriver.Change, sdkEmbedded bool) {
	for _, c := range changes {
		if isSDKBundle(c.RelPath) {
			sdkEmbedded = true
			continue
		}
		reviewable = append(reviewable, c)
	}
	return reviewable, sdkEmbedded
}

func isSDKBundle(rel string) bool {
	return strings.HasSuffix(filepath.ToSlash(rel), "vendor/"+sdkrelease.BundleName)
}

// reviewCodeChanges presents the given file changes in a tabbed, scrollable
// reviewer and returns whether the user accepted them into the plan. Nothing is
// written here — acceptance just adds "apply changes" to the recipe.
func reviewCodeChanges(ctx context.Context, out io.Writer, warnings []string, changes []initdriver.Change) (bool, error) {
	for _, w := range warnings {
		fmt.Fprintf(out, "\n⚠ %s\n", w)
	}
	files := make([]ui.ReviewFile, len(changes))
	for i, c := range changes {
		files[i] = ui.ReviewFile{Path: c.RelPath, Old: c.Old, New: c.New}
	}
	return ui.ReviewChanges(ctx, out, files)
}

// confirmPlan shows the built-up recipe and asks for the single, final go-ahead.
// Nothing has been changed until this returns true.
func confirmPlan(out io.Writer, pp projectPlan, buildTargets bool, branch, commitMsg string, fileChanges int, sdkEmbedded bool) (bool, error) {
	fmt.Fprintf(out, "\n%s\n", ui.Heading("On apply, Driverforge will:"))
	if pp.create != nil {
		fmt.Fprintf(out, "  • Create project %q (%s)\n", pp.create.name, pp.create.platform.Name)
	} else {
		fmt.Fprintf(out, "  • Use project %q\n", pp.existing.Name)
	}
	if branch != "" {
		fmt.Fprintf(out, "  • Create git branch %s\n", branch)
	}
	if sdkEmbedded {
		fmt.Fprintln(out, "  • Embed the Driverforge SDK")
	}
	fmt.Fprintf(out, "  • Write %d file change%s\n", fileChanges, plural(fileChanges))
	if buildTargets {
		fmt.Fprintln(out, "  • Set up build config (config.lua + config.release.lua)")
	}
	if branch != "" && commitMsg != "" {
		fmt.Fprintf(out, "  • Commit the changes (%q)\n", commitMsg)
	}

	return ui.Confirm("Apply this plan?", "Nothing has changed yet — this links/creates the project and writes the files.", true)
}

// fileChange builds a Change for writing content to abs, classifying it as a
// Create or Modify and skipping it (ok=false) when the content is unchanged.
func fileChange(root, abs string, content []byte) (initdriver.Change, bool, error) {
	old, err := os.ReadFile(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return initdriver.Change{RelPath: relTo(root, abs), AbsPath: abs, Kind: initdriver.Create, New: content}, true, nil
	case err != nil:
		// A permission-denied (or other) read must NOT be planned as a Create —
		// that would show no Old in the diff and, on rollback, os.Remove the
		// user's existing file.
		return initdriver.Change{}, false, fmt.Errorf("reading %s: %w", relTo(root, abs), err)
	}
	if bytes.Equal(old, content) {
		return initdriver.Change{}, false, nil
	}
	return initdriver.Change{RelPath: relTo(root, abs), AbsPath: abs, Kind: initdriver.Modify, Old: old, New: content}, true, nil
}
