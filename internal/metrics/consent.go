package metrics

import (
	"os"
	"time"

	"charm.land/huh/v2"
	"golang.org/x/term"
)

// EnsureConsent returns the current config, prompting once for metrics consent
// on first interactive launch. It never blocks in non-interactive contexts and
// never errors out a command:
//
//   - already decided (granted/denied) -> returned unchanged.
//   - opted out via env (DO_NOT_TRACK / DRIVERFORGE_NO_METRICS / CI) -> not prompted,
//     left unset so a later interactive run can still ask.
//   - not an interactive TTY -> not prompted, left unset (disabled this run).
//   - interactive + undecided -> prompt, persist the decision + an anonymous id.
func EnsureConsent() Config {
	cfg := Load()
	if cfg.MetricsConsent != ConsentUnset {
		return cfg
	}
	if optedOut() || !interactive() {
		return cfg
	}

	granted := false
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Help improve driverforge?").
				Description("Share usage data with the Driverforge team — commands run, build timings, and errors, associated with your Driverforge account.\nNo source code, file contents, or driver data is ever sent. Opt out anytime with DO_NOT_TRACK=1.").
				Affirmative("Yes, share").
				Negative("No thanks").
				Value(&granted),
		),
	)
	if err := form.Run(); err != nil {
		// User aborted (e.g. Ctrl-C) — leave undecided, ask again next time.
		return cfg
	}

	if granted {
		cfg.MetricsConsent = ConsentGranted
	} else {
		cfg.MetricsConsent = ConsentDenied
	}
	cfg.ConsentAskedAt = time.Now()
	if cfg.AnonymousID == "" {
		cfg.AnonymousID = newAnonymousID()
	}
	_ = Save(cfg) // best-effort; a save failure just means we ask again
	return cfg
}

// interactive reports whether both stdin and stdout are terminals, so a consent
// prompt can be shown and answered.
func interactive() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}
