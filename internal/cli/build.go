package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/driverforgeconfig"
	"github.com/driverforge/cli/internal/project"
	"github.com/driverforge/cli/internal/ui"
	"github.com/driverforge/cli/internal/versioning"
	"github.com/driverforge/cli/pkg/c4z"
	"github.com/driverforge/cli/pkg/c4z/driverxml"
	"github.com/driverforge/cli/pkg/c4z/pack"
)

func newBuildCmd() *cobra.Command {
	var (
		configuration    string
		increment        bool
		versionFlag      string
		c4zproj          string
		outputDir        string
		unpack           bool
		sourcemap        bool
		encrypt          bool
		noSuffix         bool
		allowExecute     bool
		warningsAsErrors bool
	)
	cmd := &cobra.Command{
		Use:   "build",
		Short: "Build a .c4z driver for a build configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Flags().Changed("version") && strings.TrimSpace(versionFlag) == "" {
				return clierr.WithCode(clierr.User("--version needs a value", "e.g. --version 2026070501"), codeUsage)
			}
			proj, err := requireProject(flagProjectDir)
			if err != nil {
				return err
			}
			// The committed project config is optional for a plain build (any
			// driverpackager-style tree stays buildable), but when present it
			// supplies per-configuration defaults below.
			projCfg, err := driverforgeconfig.Load(proj.Dir)
			if err != nil {
				return err
			}
			// Version mutation needs to know how the driver is versioned, which
			// is decided during `driverforge init`. A plain build stays init-free, and
			// --version supplies the value explicitly so it needs no scheme.
			nextVersion := versionFlag
			if increment {
				if projCfg == nil {
					return versioningNotConfiguredErr("--increment")
				}
				nextVersion, err = nextProjectVersion(proj, projCfg, "--increment")
				if err != nil {
					return err
				}
			}
			// Encryption and suffixing default from the project config's
			// configurations block; explicit flags win (flag > config > driver.xml
			// as authored — --encrypt stays a tri-state).
			encryptOverride, noSuffixEffective := configurationDefaults(projCfg, configuration)
			if cmd.Flags().Changed("encrypt") {
				encryptOverride = &encrypt
			}
			if cmd.Flags().Changed("no-suffix") {
				noSuffixEffective = noSuffix
			}

			distDir := proj.DistDir
			if outputDir != "" {
				distDir = outputDir
			}
			res, err := executeBuild(cmd, c4z.Options{
				Configuration:    configuration,
				SrcDir:           proj.SrcDir,
				DistDir:          distDir,
				Version:          nextVersion,
				ManifestPath:     c4zproj,
				AllowExecute:     allowExecute,
				Unpack:           unpack,
				Sourcemap:        sourcemap,
				Encrypt:          encryptOverride,
				NoSuffix:         noSuffixEffective,
				Verbose:          flagVerbose,
				WarningsAsErrors: warningsAsErrors,
				Now:              time.Now(),
			}, configuration)
			if err != nil {
				return err
			}
			return emit(cmd, buildOutOf(res, configuration), noText)
		},
	}
	cmd.Flags().StringVarP(&configuration, "configuration", "c", "", "build configuration to apply (swaps in config.<configuration>.lua and suffixes the name; omit for a default build using config.lua)")
	cmd.Flags().BoolVarP(&increment, "increment", "i", false, "bump <version> per the project's versioning scheme (date or integer, chosen at driverforge init)")
	cmd.Flags().StringVar(&versionFlag, "version", "", "stamp an absolute <version> for this build (driverpackager --driver-version parity); persisted to driver.xml on success")
	cmd.Flags().StringVar(&c4zproj, "c4zproj", "", "path to the .c4zproj manifest (default src/manifest.c4zproj)")
	cmd.Flags().StringVarP(&outputDir, "output-dir", "o", "", "directory for the built .c4z (default dist/)")
	cmd.Flags().BoolVar(&allowExecute, "allow-execute", false, "development build: append C4:AllowExecute(true) to the driver script, enabling Director's Lua command window (never written to source)")
	cmd.Flags().BoolVarP(&unpack, "unpack", "u", false, "also leave an unpacked copy in dist")
	cmd.Flags().BoolVarP(&sourcemap, "sourcemap", "s", false, "generate a Lua source map")
	cmd.Flags().BoolVar(&encrypt, "encrypt", false, "encrypt the driver script for this build (--encrypt=false forces off); default follows the project config's configuration entry, then driver.xml")
	cmd.Flags().BoolVar(&noSuffix, "no-suffix", false, "build a named configuration under the naked driver name — no artifact or display-name suffix, replacing the default build's .c4z in dist (--no-suffix=false forces suffixing back on)")
	cmd.Flags().BoolVar(&warningsAsErrors, "warnings-as-errors", false, "fail the build (before packaging) if it emits any warning — for CI, where a warned build must not produce an artifact")
	cmd.MarkFlagsMutuallyExclusive("increment", "version")
	return cmd
}

// executeBuild runs the build with live progress (a run frame, or plain lines
// when non-interactive), emits build metrics, and maps anticipated failures to
// friendly errors. Used by the standalone build command; the ship commands
// (sync/deploy) embed the build in their own frame via buildInFrame.
func executeBuild(cmd *cobra.Command, opts c4z.Options, configuration string) (c4z.Result, error) {
	out := textOut(cmd)
	if ui.Interactive(flagNoTUI) || structured() {
		var res c4z.Result
		defer metricsClient.Flush(cmd.Context())
		_, err := runFrame(cmd.Context(), out, ui.FrameOptions{
			Step:        buildingLabel(configuration),
			DetailsOpen: flagVerbose,
		}, func(ctx context.Context, f ui.Progress) error {
			var berr error
			res, berr = buildInFrame(ctx, f, opts, configuration)
			if berr != nil {
				return berr
			}
			f.Step("Build complete")
			return nil
		})
		return res, err
	}
	opts.Progress = ui.PlainProgress(out, flagVerbose)
	start := time.Now()
	res, err := c4z.Build(cmd.Context(), opts)
	emitBuildMetrics(configuration, res, err, time.Since(start))
	defer metricsClient.Flush(cmd.Context())
	if err != nil {
		return res, clierr.AsFatal(clierr.WithCode(buildErr(err), codeBuildFailed))
	}
	ui.PrintResultPlain(out, res)
	return res, nil
}

// buildingLabel is the frame's initial current-step line for a build:
// "Building (default)…" until the first fine-grained step replaces it.
func buildingLabel(configuration string) string {
	if configuration == "" {
		configuration = "default"
	}
	return "Building (" + configuration + ")…"
}

// buildInFrame runs c4z.Build reporting into an existing run frame: the
// fine-grained step on the current-step line, each completed step as a
// timestamped log entry, and the driver/version/artifact facts as summary rows.
// It wraps Progress with a stepRecorder (whose durations feed the log-line
// suffixes) and emits the build event on completion (skipped when the run was
// cancelled, as before — a user cancel isn't a build failure); anticipated
// failures map through buildErr and are marked fatal. Shared by build, deploy,
// and sync, whose frames all start with a build.
func buildInFrame(ctx context.Context, f ui.Progress, opts c4z.Options, configuration string) (c4z.Result, error) {
	rec := newStepRecorder()
	// rec.wrapProgress observes before forwarding, so rec.durs already carries
	// the step's duration by the time the log line renders it.
	opts.Progress = rec.wrapProgress(func(e c4z.Event) {
		if e.Step == c4z.StepDone {
			return
		}
		label := ui.StepLabel(e.Step)
		suffix := ""
		if e.Msg != "" {
			suffix = " — " + e.Msg
		}
		switch e.Status {
		case c4z.StepStart:
			f.Step(label + "…")
		case c4z.StepOK:
			if d := rec.durs[e.Step]; d > 0 {
				suffix += fmt.Sprintf(" (%s)", d.Round(time.Millisecond))
			}
			f.Logf("✓ %s%s", label, suffix)
			// The resolve step reports the driver name (man.Name) as its msg —
			// surface it as a summary row as soon as it's known.
			if e.Step == c4z.StepResolve && e.Msg != "" {
				f.Row("driver", e.Msg)
			}
		case c4z.StepSkip:
			f.Logf("– %s%s", label, suffix)
		case c4z.StepWarn:
			f.Warnf("⚠ %s", e.Msg)
		case c4z.StepFail:
			f.Logf("✗ %s: %v", label, e.Err)
		}
	})
	start := time.Now()
	res, err := c4z.Build(ctx, opts)
	if ctx.Err() != nil {
		// A cancelled run (Ctrl-C tore the frame down) — don't skew the metrics.
		return res, err
	}
	emitBuildMetrics(configuration, res, err, time.Since(start))
	if err != nil {
		return res, clierr.AsFatal(clierr.WithCode(buildErr(err), codeBuildFailed))
	}
	f.Row("driver", res.Name)
	if res.Version != "" {
		f.Row("version", res.Version)
	}
	f.Row("artifact", res.C4zPath)
	if res.Encrypted {
		f.Row("encrypted", "yes")
	}
	if res.UnpackedDir != "" {
		f.Row("unpacked", res.UnpackedDir)
	}
	if res.SourceMapPath != "" {
		f.Row("sourcemap", res.SourceMapPath)
	}
	return res, nil
}

// configurationDefaults resolves the per-configuration defaults overlay from
// the committed project config for the named build configuration. A nil cfg
// (project not initialised), a default build, or a configuration without an
// entry all mean "no opinion": nil encrypt (driver.xml as authored) and the
// standard suffixing. Callers with flags apply them on top (flag > config).
func configurationDefaults(cfg *driverforgeconfig.Config, configuration string) (encrypt *bool, noSuffix bool) {
	if cfg == nil || configuration == "" {
		return nil, false
	}
	c, ok := cfg.Configurations[configuration]
	if !ok {
		return nil, false
	}
	return c.Encrypt, c.Suffix != nil && !*c.Suffix
}

// buildForShip builds the driver ahead of a ship command (sync/deploy) so the
// shipped artifact always matches the current source — shipping a stale dist/
// artifact is the footgun this exists to prevent. Ship builds use default
// options (no unpack/sourcemap) plus the project config's per-configuration
// defaults, so a ship of a configuration produces the same artifact a plain
// `driverforge build -c <name>` would. A sync ships the authored version
// unchanged (hot-swap bypasses Director's version check).
//
// Ship commands require an initialised project: deploy manages the driver's
// version (and sync escalates into deploy), which needs the versioning scheme
// `driverforge init` records.
func buildForShip(cmd *cobra.Command, configuration string) (c4z.Result, error) {
	opts, err := shipBuildOptions(configuration)
	if err != nil {
		return c4z.Result{}, err
	}
	return executeBuild(cmd, opts, configuration)
}

// shipBuildOptions assembles the build options a ship build (sync) uses — the
// authored version unchanged (hot-swap bypasses Director's version check).
// Shared by the plain path (via buildForShip) and the framed path, which embeds
// the build in the ship command's own frame.
func shipBuildOptions(configuration string) (c4z.Options, error) {
	proj, err := requireProject(flagProjectDir)
	if err != nil {
		return c4z.Options{}, err
	}
	cfg, err := requireInitialised(proj.Dir)
	if err != nil {
		return c4z.Options{}, err
	}
	encrypt, noSuffix := configurationDefaults(cfg, configuration)
	return c4z.Options{
		Configuration: configuration,
		SrcDir:        proj.SrcDir,
		DistDir:       proj.DistDir,
		Encrypt:       encrypt,
		NoSuffix:      noSuffix,
		Verbose:       flagVerbose,
		Now:           time.Now(),
	}, nil
}

// buildForDeploy builds with the next version per the project's scheme — a
// deploy must carry a higher <version> or Director won't reload the driver.
// The bump is persisted to the source driver.xml after the successful build.
func buildForDeploy(cmd *cobra.Command, configuration string) (c4z.Result, error) {
	opts, err := deployBuildOptions(configuration)
	if err != nil {
		return c4z.Result{}, err
	}
	return executeBuild(cmd, opts, configuration)
}

// deployBuildOptions assembles the build options a deploy build uses — the
// next version per the project's scheme. Shared by the plain path (via
// buildForDeploy) and the framed path, which embeds the build in the deploy's
// own frame.
func deployBuildOptions(configuration string) (c4z.Options, error) {
	proj, err := requireProject(flagProjectDir)
	if err != nil {
		return c4z.Options{}, err
	}
	cfg, err := requireInitialised(proj.Dir)
	if err != nil {
		return c4z.Options{}, err
	}
	next, err := nextProjectVersion(proj, cfg, "deploy")
	if err != nil {
		return c4z.Options{}, err
	}
	encrypt, noSuffix := configurationDefaults(cfg, configuration)
	return c4z.Options{
		Configuration: configuration,
		SrcDir:        proj.SrcDir,
		DistDir:       proj.DistDir,
		Version:       next,
		Encrypt:       encrypt,
		NoSuffix:      noSuffix,
		Verbose:       flagVerbose,
		Now:           time.Now(),
	}, nil
}

// nextProjectVersion computes the next <version> from the project's configured
// scheme and the authored driver.xml value. what names the operation asking
// (for error messages): "--increment" or "deploy".
func nextProjectVersion(proj project.Project, cfg *driverforgeconfig.Config, what string) (string, error) {
	if !versioning.Valid(cfg.Versioning) {
		return "", versioningNotConfiguredErr(what)
	}
	data, err := os.ReadFile(filepath.Join(proj.SrcDir, "driver.xml"))
	if err != nil {
		return "", err
	}
	current, ok := driverxml.ReadVersion(data)
	if !ok {
		return "", &clierr.UserError{
			Code:    codeVersioning,
			Title:   "No <version> element",
			Message: "driver.xml has no <version> element to bump",
			Hint:    "add one, e.g. <version>1</version>",
		}
	}
	next, err := versioning.Next(cfg.Versioning, current, time.Now())
	if err != nil {
		return "", clierr.WithCode(clierr.Wrap(err, err.Error(), "fix the <version> value in driver.xml, or re-run `driverforge init` to change the versioning scheme"), codeVersioning)
	}
	return next, nil
}

// versioningNotConfiguredErr is the friendly condition for a version-mutating
// operation on a project without a versioning scheme.
func versioningNotConfiguredErr(what string) *clierr.UserError {
	return &clierr.UserError{
		Code:    codeVersioning,
		Title:   "Versioning not configured",
		Message: fmt.Sprintf("%s needs to know how this driver is versioned", what),
		Hint:    "run `driverforge init` to choose a versioning scheme",
	}
}

// buildErr maps anticipated, user-caused build failures (from pkg/c4z, which is
// UI-agnostic) to friendly clierr.UserErrors so they render as a card and aren't
// reported to Sentry. Anything else is a genuine failure and passes through.
func buildErr(err error) error {
	var warned *c4z.WarningsAsErrorsError
	if errors.As(err, &warned) {
		return clierr.Wrap(err, "Build failed: "+warned.Error(),
			"the warnings are listed above — fix them, or build without --warnings-as-errors")
	}
	var cfgNotFound *c4z.ConfigNotFoundError
	if errors.As(err, &cfgNotFound) {
		return clierr.Wrap(err, cfgNotFound.Error(),
			fmt.Sprintf("create config.%s.lua, or omit --configuration for the default build", cfgNotFound.Configuration))
	}
	var unsupported *c4z.UnsupportedDriverTypeError
	if errors.As(err, &unsupported) {
		return clierr.Wrap(err, unsupported.Error(), "driverforge builds .c4z drivers — check the type in manifest.c4zproj")
	}
	var noScript *c4z.NoEncryptableScriptError
	if errors.As(err, &noScript) {
		return clierr.Wrap(err, noScript.Error(), `add a <script file="..."/> tag to driver.xml, or drop --encrypt`)
	}
	var noScriptFile *c4z.NoScriptFileError
	if errors.As(err, &noScriptFile) {
		return clierr.Wrap(err, noScriptFile.Error(), `add a <script file="..."/> tag to driver.xml, or drop the flag`)
	}
	var missing *pack.MissingTargetError
	if errors.As(err, &missing) {
		return clierr.Wrap(err, missing.Error(),
			fmt.Sprintf("list %s in manifest.c4zproj as a file item, or include it in a dir item", missing.Target))
	}
	var dup *pack.DuplicateEntryError
	if errors.As(err, &dup) {
		return clierr.Wrap(err, dup.Error(), "remove the overlapping item from manifest.c4zproj")
	}
	if errors.Is(err, driverxml.ErrNameNotFound) {
		return clierr.Wrap(err, err.Error(), "check the top-level <name> element in driver.xml, or omit --configuration")
	}
	return err
}

// buildOut is a finished build in structured output: what build reports, and
// what deploy and sync report about the build they shipped.
type buildOut struct {
	Driver        string    `json:"driver"`
	Configuration string    `json:"configuration"`
	Version       *string   `json:"version"`
	Artifact      string    `json:"artifact"`
	Encrypted     bool      `json:"encrypted"`
	Unpacked      *string   `json:"unpacked"`
	Sourcemap     *string   `json:"sourcemap"`
	Warnings      []warning `json:"warnings"`
}

func buildOutOf(res c4z.Result, configuration string) buildOut {
	o := buildOut{
		Driver: res.Name, Configuration: firstNonEmpty(configuration, "default"),
		Version: optional(res.Version), Artifact: res.C4zPath, Encrypted: res.Encrypted,
		Unpacked: optional(res.UnpackedDir), Sourcemap: optional(res.SourceMapPath),
		Warnings: []warning{},
	}
	for _, w := range res.Warnings {
		o.Warnings = append(o.Warnings, warning{Code: codeBuildWarning, Message: w})
	}
	return o
}
