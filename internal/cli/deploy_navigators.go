package cli

import (
	"context"

	"github.com/driverforge/cli/internal/agent"
	"github.com/driverforge/cli/internal/deploycache"
	"github.com/driverforge/cli/internal/orgctx"
)

// deployedDriver is what the post-deploy bookkeeping needs to know: which driver
// reached which controller, what that controller's agent can do, and where to
// report. It is a struct rather than a parameter list because eight positional
// arguments — three of them strings and bools in a row — is a call site no
// reader can check at a glance.
//
// Report and Warn are the only thing the plain and framed deploy paths ever
// differed on: fmt.Fprintf into the writer for one, the frame's Logf/Warnf for
// the other.
type deployedDriver struct {
	Client   *agent.Client
	Health   agent.Health
	Device   orgctx.Device
	Filename string
	Hash     string
	// Force refreshes navigators even when driver.xml is unchanged
	// (--refresh-navigators).
	Force bool

	Report func(format string, args ...any)
	Warn   func(format string, args ...any)
}

// recordBaselineAndRefresh is the post-deploy bookkeeping shared by the plain and
// framed paths, which previously carried a copy each: record the sync baseline,
// then kick navigators when the driver's UI schema changed.
//
// Call only when the restart was CONFIRMED (see deployConfirmed). An unconfirmed
// deploy means the old, structurally different driver may still be running, and
// caching this hash would let a later `driverforge sync` hot-swap into it — the
// exact hazard the baseline guards against.
//
// A refresh hiccup is reported, never returned: it must not hold a finished
// deploy hostage.
func recordBaselineAndRefresh(ctx context.Context, d deployedDriver) {
	prev := deploycache.Get(d.Device.ID, d.Filename)
	_ = deploycache.Set(d.Device.ID, d.Filename, d.Hash)

	if !shouldRefreshNavigators(d.Force, prev, d.Hash) {
		return
	}
	if !d.Health.Supports(agent.CapabilityNavigatorRefresh) {
		d.Report("– navigators not refreshed (agent doesn't support it — upgrade via `driverforge upgrade`)")
		return
	}

	rctx, rcancel := context.WithTimeout(ctx, navigatorRefreshTimeout)
	defer rcancel()
	if rerr := d.Client.RefreshNavigators(rctx); rerr != nil {
		d.Warn("couldn't refresh navigators: %v", rerr)
		return
	}
	d.Report("✓ Navigators refreshed")
}
