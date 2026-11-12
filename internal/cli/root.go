// Package cli wires the Cobra command tree for the driverforge CLI. Commands are thin
// adapters: they parse flags, choose the UI mode, and call the domain libraries
// (pkg/c4z, internal/...). No build logic lives here.
package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/driverforge/cli/internal/auth"
	"github.com/driverforge/cli/internal/buildinfo"
	"github.com/driverforge/cli/internal/errreport"
	"github.com/driverforge/cli/internal/metrics"
	"github.com/driverforge/cli/internal/orgctx"
	"github.com/driverforge/cli/internal/selfupdate"
	"github.com/driverforge/cli/internal/ui"
	"github.com/driverforge/cli/internal/updatecheck"
)

var (
	flagVerbose       bool
	flagProjectDir    string
	flagNoTUI         bool
	flagNoUpdateCheck bool

	// metricsClient is initialised in the root PersistentPreRun (after the
	// first-launch consent prompt) and used by the commands. It is safe to use
	// when nil (all methods no-op).
	metricsClient *metrics.Client

	// lastInvocation describes the command that ran (full path + args + flags),
	// captured for error-report tagging (set in PersistentPreRun).
	lastInvocation errreport.Invocation
)

// Execute runs the root command and returns a process exit code. It renders any
// error nicely and, for unexpected errors, offers to report it to Sentry.
func Execute() (code int) {
	defer func() {
		if r := recover(); r != nil {
			code = 1
			if structured() {
				e := &cmdErr{Code: codeInternal, Message: "The CLI crashed.",
					Hint: "Try again; if it persists, contact support with the reference."}
				e.Reference = errreport.ReportPanicQuietly(r, lastInvocation)
				writeJSON(os.Stdout, map[string]any{"error": e})
				writeJSON(os.Stderr, map[string]any{"error": e})
				return
			}
			ui.RenderCrash(os.Stderr, fmt.Errorf("driverforge crashed: %v", r))
			errreport.HandlePanic(r, lastInvocation, os.Stderr)
		}
	}()
	cmd, err := newRootCmd().ExecuteC()
	if err != nil {
		// Cobra usage errors (unknown command/flag, bad args) arrive here unwrapped;
		// make them friendly and pointed at the docs. cmd is the command cobra
		// resolved, so the link lands on the right page.
		err = friendlyUsage(cmd, err)
		// Structured output reports every failure as an error document, and
		// reports a bug quietly (with its reference) rather than offering to.
		if structured() {
			return writeFailure(os.Stdout, os.Stderr, err)
		}
		// An unexpected error (a bug) is reportable: apologise and offer Sentry.
		// An expected condition is wrapped as a clierr.UserError: render it
		// nicely and exit non-zero, but never report it.
		if errreport.Reportable(err) {
			ui.RenderCrash(os.Stderr, err)
		} else {
			ui.RenderUserError(os.Stderr, err)
		}
		errreport.Handle(err, lastInvocation, os.Stderr)
		return exitFor(err)
	}
	return 0
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "driverforge",
		Short: "Driverforge CLI",
		Long: `driverforge builds and manages Control4 drivers.

Under a coding agent, or with DRIVERFORGE_OUTPUT=json, every command writes one
JSON document to stdout (its result, or {"error": …} with a stable code),
progress as JSON lines on stderr, and never prompts. DRIVERFORGE_OUTPUT=text
keeps text output.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		// Version enables cobra's root --version flag. It prints and exits
		// before any hooks run, so it skips the consent prompt and update
		// check — the `version` subcommand stays the richer report (it adds
		// the selected controller's versions).
		Version: buildinfo.Version,
		// Runs before any command. On first interactive launch it prompts once
		// for metrics consent, then initialises the (possibly disabled) client.
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			lastInvocation = invocationOf(cmd, args)
			// Clear the previous binary a Windows self-upgrade had to move
			// aside; a no-op everywhere else, and on every run after the first.
			// Deliberately before the meta/auth bail-out so it happens whatever
			// the user ran next.
			selfupdate.SweepOldBinary()
			if isAuthOrMetaCmd(cmd) {
				return // don't stack the consent prompt on meta/auth/org commands
			}
			// Structured output (a coding agent) can't answer the consent
			// prompt; it keeps whatever the person chose (or the default).
			cfg := metrics.Load()
			if !structured() {
				cfg = metrics.EnsureConsent()
			}
			metricsClient = metrics.New(cfg)
			// Attach the signed-in account and active org (stored state only —
			// no network, expiry irrelevant for attribution). Signed-out runs
			// stay anonymous; org-less runs stay un-dimensioned.
			if creds, err := auth.Load(); err == nil {
				metricsClient.SetUser(creds.Subject)
			}
			if org, err := orgctx.Current(); err == nil {
				metricsClient.SetOrg(org.ID)
			}
		},
		// Runs only after a command succeeds (skipped on error). Prints a
		// "newer version available" notice when appropriate.
		PersistentPostRun: func(cmd *cobra.Command, _ []string) {
			if flagNoUpdateCheck || structured() || isAuthOrMetaCmd(cmd) || ownsTheUpdateStory(cmd) {
				return // don't nag on meta/auth/org commands
			}
			updatecheck.Notify(cmd.Context(), buildinfo.Version, os.Stderr)
		},
	}
	root.PersistentFlags().BoolVarP(&flagVerbose, "verbose", "v", false, "verbose output")
	root.PersistentFlags().StringVar(&flagProjectDir, "project-dir", ".", "driver project directory")
	root.PersistentFlags().BoolVar(&flagNoTUI, "no-tui", false, "disable the interactive TUI (plain output)")
	root.PersistentFlags().BoolVar(&flagNoUpdateCheck, "no-update-check", false, "do not check for a newer driverforge release")
	// Match the `version` subcommand's CLI line (cobra's default template says
	// just "driverforge version dev", dropping the commit and build date).
	root.SetVersionTemplate(buildinfo.String() + "\n")
	root.AddCommand(newBuildCmd(), newInitCmd(), newPublishCmd(), newDeployCmd(), newSyncCmd(), newRunCmd(), newVersionCmd())
	root.AddCommand(newLoginCmd(), newLogoutCmd(), newWhoamiCmd(), newOrgCmd(), newDeviceCmd())
	root.AddCommand(newUpgradeCmd(), newDoctorCmd(), newSkillsCmd())
	root.AddCommand(newLogsCmd(), newErrorsCmd(), newEventsCmd(), newGetCmd())
	installUsageErrors(root) // make unknown command/flag/arg errors friendly
	installStructuredHelp(root)
	return root
}

// isAuthOrMetaCmd reports whether a command is a meta, auth, or org-context
// command — for these we skip the metrics-consent prompt and update-check nag
// (they're session/plumbing, and several drive their own interactive UI).
// ownsTheUpdateStory reports whether a command has already told the user
// everything the post-run update notice would.
//
// Only `upgrade` qualifies: its table reports the same thing, and after a
// successful self-upgrade this process still carries the version it started
// with — so the notice would announce an update the user had just installed.
// Kept separate from isAuthOrMetaCmd because that predicate also gates the
// metrics consent prompt, and `upgrade` does real work worth measuring.
func ownsTheUpdateStory(cmd *cobra.Command) bool { return cmd.Name() == "upgrade" }

func isAuthOrMetaCmd(cmd *cobra.Command) bool {
	switch cmd.Name() {
	case "version", "help", "completion", "login", "logout", "whoami":
		return true
	}
	if p := cmd.Parent(); p != nil && (p.Name() == "org" || p.Name() == "device") {
		return true
	}
	return false
}

// invocationOf captures the full command path, the positional-arg count, and
// the names of any flags that were set — so an error report identifies
// precisely which command failed (e.g. "org list", not just "list"). Arg and
// flag VALUES are never captured; they carry user data.
func invocationOf(cmd *cobra.Command, args []string) errreport.Invocation {
	return errreport.Invocation{
		Command:  commandLabel(cmd),
		ArgCount: len(args),
		Flags:    changedFlags(cmd),
	}
}

// commandLabel is the subcommand path without the root binary name
// ("org list", "build"); for the bare root it is the root name.
func commandLabel(cmd *cobra.Command) string {
	path := cmd.CommandPath() // e.g. "driverforge org list"
	if root := cmd.Root().Name(); strings.HasPrefix(path, root+" ") {
		return path[len(root)+1:]
	}
	return path
}

// changedFlags returns the names (not values, to avoid leaking data) of flags
// that were explicitly set, including inherited persistent flags.
func changedFlags(cmd *cobra.Command) []string {
	var names []string
	cmd.Flags().Visit(func(f *pflag.Flag) { names = append(names, f.Name) })
	return names
}
