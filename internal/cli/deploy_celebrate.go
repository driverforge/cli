package cli

import (
	"context"
	"io"
	"time"

	"github.com/driverforge/cli/internal/driverforgeapi"
	"github.com/driverforge/cli/internal/driverforgeconfig"
	"github.com/driverforge/cli/internal/orgctx"
	"github.com/driverforge/cli/internal/project"
	"github.com/driverforge/cli/internal/telemetry"
	"github.com/driverforge/cli/internal/ui"
)

// telemetrySetupTimeout bounds the graph round-trips done before the install
// (resolve the project id, mint the connection token).
const telemetrySetupTimeout = 10 * time.Second

// firstDeployWatch is a live subscription to the driver's project telemetry,
// opened before a first install so the driver's first event isn't missed, and
// drained afterwards to celebrate + link the installer to the event stream. A nil
// value is valid and makes every method a no-op, so callers never branch on setup
// success.
type firstDeployWatch struct {
	w        *telemetry.Watcher
	orgSlug  string
	projSlug string
}

// startFirstDeployWatch opens the telemetry subscription for the current
// project, best-effort. It returns nil (a working no-op) on anything that would
// make the celebration impossible — not a driver project, not linked to a
// project, offline, unauthenticated — because confirming a live driver must never
// jeopardise the deploy itself.
func startFirstDeployWatch(ctx context.Context, dev orgctx.Device) *firstDeployWatch {
	orgSlug, projSlug, projectID, ok := resolveProjectContext(ctx)
	if !ok {
		return nil
	}
	api := driverforgeapi.New()
	w, err := telemetry.NewWatcher(ctx, projectID, api.CentrifugoToken, telemetry.IsDriverEvent)
	if err != nil {
		return nil
	}
	return &firstDeployWatch{w: w, orgSlug: orgSlug, projSlug: projSlug}
}

// resolveProjectContext gathers the org slug, project slug, and project id needed
// to subscribe to and deep-link the driver's telemetry. ok is false whenever any
// piece is unavailable.
func resolveProjectContext(ctx context.Context) (orgSlug, projSlug, projectID string, ok bool) {
	proj, err := project.Locate(flagProjectDir)
	if err != nil {
		return "", "", "", false
	}
	cfg, err := driverforgeconfig.Load(proj.Dir)
	if err != nil || cfg == nil || cfg.Project.Slug == "" {
		return "", "", "", false
	}

	var orgID string
	if org := orgctx.Load().CurrentOrg; org != nil {
		orgID = org.ID
		orgSlug = org.Slug
	}
	if orgSlug == "" {
		orgSlug = cfg.Org
	}

	rctx, cancel := context.WithTimeout(ctx, telemetrySetupTimeout)
	defer cancel()
	projects, err := driverforgeapi.New().ListAllProjects(rctx)
	if err != nil {
		return "", "", "", false
	}
	for _, p := range projects {
		// Slugs are unique within an org; disambiguate by org when we know it, so
		// the same slug in another org can't be picked.
		if p.Slug == cfg.Project.Slug && (orgID == "" || p.OrganizationID == orgID) {
			return orgSlug, cfg.Project.Slug, p.ID, true
		}
	}
	return "", "", "", false
}

// Close releases the subscription. Nil-safe.
func (f *firstDeployWatch) Close() {
	if f == nil {
		return
	}
	f.w.Close()
}

// celebrate waits for the driver's first telemetry event, then shows a live
// "N events received" counter that settles into a celebration linking to the
// project's event stream in the Anvil web app. On timeout it prints nothing extra
// — the deploy already reported success. Nil-safe.
func (f *firstDeployWatch) celebrate(ctx context.Context, out io.Writer, driverName string) {
	if f == nil {
		return
	}
	// Bridge the event stream to plain pulses for the UI; the bridge stops when
	// the celebration finishes (cancel fires on return).
	bctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sig := make(chan struct{}, 256)
	go func() {
		defer close(sig)
		for {
			select {
			case <-f.w.Events():
				select {
				case sig <- struct{}{}:
				default:
				}
			case <-bctx.Done():
				return
			}
		}
	}()

	url := telemetry.EventsURL(f.orgSlug, f.projSlug)
	ui.RunCheckIn(ctx, out, ui.Interactive(flagNoTUI), driverName, url, sig)
}
