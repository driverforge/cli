package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/driverforge/cli/internal/clierr"
)

// docsBaseURL is the short-link domain fronting the docs. Each top-level command
// has a slug of its name behind commandSlugPrefix (go.driverforge.com/cli-build →
// docs.driverforge.com/cli/build/), so a new top-level command needs its slug
// minted in Shlink or its usage hint will 404.
//
// Published contract: these slugs are baked into released binaries and keep being
// printed for as long as those versions stay installed. When the docs move, repoint
// the slug in Shlink — never retire one, and never repoint it at a page that no
// longer exists (the docs site soft-404s to its homepage with HTTP 200, so a dead
// target looks fine until someone reads it).
const docsBaseURL = "https://go.driverforge.com"

// overviewSlug is the CLI docs landing page — the root command's hint.
const overviewSlug = "cli"

// commandSlugPrefix sets command slugs apart from the domain's other links, some
// of which share a command's name (go.driverforge.com/get is the install script).
const commandSlugPrefix = "cli-"

// usageError wraps a cobra command-usage error (unknown command/flag, wrong
// argument count) as an expected clierr.UserError pointing at the relevant docs
// page — so misuse renders as a friendly notice and is never reported to Sentry.
func usageError(cmd *cobra.Command, err error) error {
	return clierr.WithCode(clierr.WrapT(err, "Invalid usage", capitalise(err.Error()), "see "+docsURL(cmd)), codeUsage)
}

// docsURL is the docs short link for cmd: the per-command slug for a real
// subcommand (e.g. go.driverforge.com/cli-build), or the overview for the root.
func docsURL(cmd *cobra.Command) string {
	if name := topLevelName(cmd); name != "" {
		return docsBaseURL + "/" + commandSlugPrefix + name
	}
	return docsBaseURL + "/" + overviewSlug
}

// topLevelName returns the first command segment beneath root (e.g. "device"
// for "driverforge device select"), or "" for the root itself.
func topLevelName(cmd *cobra.Command) string {
	if cmd == nil || !cmd.HasParent() {
		return ""
	}
	c := cmd
	for c.Parent().HasParent() {
		c = c.Parent()
	}
	return c.Name()
}

// installUsageErrors makes cobra usage errors friendly across the whole command
// tree: flag errors via FlagErrorFunc (inherited from root), and argument-count
// / "unknown command" errors by wrapping each command's Args validator.
func installUsageErrors(root *cobra.Command) {
	root.SetFlagErrorFunc(usageError)
	walk(root, func(c *cobra.Command) {
		if c.Args == nil {
			return // a non-runnable parent; cobra raises its own error (see friendlyUsage)
		}
		validate := c.Args
		c.Args = func(cmd *cobra.Command, args []string) error {
			if err := validate(cmd, args); err != nil {
				return usageError(cmd, err)
			}
			return nil
		}
	})
}

// friendlyUsage catches the one usage error that no hook covers: cobra raises a
// bare "unknown command …" for an unrecognised subcommand of a non-runnable
// parent (e.g. `driverforge foo`), before any Args validator runs. cmd is the resolved
// parent (root for `driverforge foo`), so the docs link lands on the right page.
func friendlyUsage(cmd *cobra.Command, err error) error {
	if err == nil || clierr.IsUser(err) {
		return err
	}
	if strings.HasPrefix(err.Error(), "unknown command") {
		return usageError(cmd, err)
	}
	return err
}

func walk(c *cobra.Command, fn func(*cobra.Command)) {
	fn(c)
	for _, child := range c.Commands() {
		walk(child, fn)
	}
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:] // cobra messages are ASCII
}
