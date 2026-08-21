package cli

import (
	"errors"
	"testing"

	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/driverforgeapi"
	"github.com/driverforge/cli/internal/orgctx"
)

// Release builds route org-scoped links through the shortener: the slug carries
// the org in the query string for the app's /a handler to swap into the path.
func TestOrgLinkURLShortened(t *testing.T) {
	isolateConfig(t)
	t.Setenv("DRIVERFORGE_SHORTLINK_URL", "https://go.driverforge.test")
	if err := orgctx.SetCurrent(orgctx.Org{ID: "org-1", Slug: "acme"}); err != nil {
		t.Fatal(err)
	}

	if got, want := orgLinkURL("plan", "settings/subscription"), "https://go.driverforge.test/plan?org=acme"; got != want {
		t.Errorf("orgLinkURL = %q, want %q", got, want)
	}
	if got, want := orgLinkURL("billing", "settings/billing"), "https://go.driverforge.test/billing?org=acme"; got != want {
		t.Errorf("orgLinkURL = %q, want %q", got, want)
	}
}

// With no short-link base (local, dev and fork builds) the link points straight at
// the page — the same destination /a would have redirected to, so a dev stack needs
// no /a route.
func TestOrgLinkURLDirect(t *testing.T) {
	isolateConfig(t)
	t.Setenv("DRIVERFORGE_SHORTLINK_URL", "")
	t.Setenv("DRIVERFORGE_APP_URL", "https://dev.driverforge.test/")
	if err := orgctx.SetCurrent(orgctx.Org{ID: "org-1", Slug: "acme"}); err != nil {
		t.Fatal(err)
	}

	if got, want := orgLinkURL("plan", "settings/subscription"), "https://dev.driverforge.test/acme/settings/subscription"; got != want {
		t.Errorf("orgLinkURL = %q, want %q", got, want)
	}
	if got, want := orgLinkURL("billing", "settings/billing"), "https://dev.driverforge.test/acme/settings/billing"; got != want {
		t.Errorf("orgLinkURL = %q, want %q", got, want)
	}
}

// No org context at all (shouldn't happen on the init path, which persists one
// before these errors can fire) degrades to the dashboard rather than emitting a
// link with an empty slug in it.
func TestOrgLinkURLNoOrgFallsBack(t *testing.T) {
	isolateConfig(t)
	t.Setenv("DRIVERFORGE_SHORTLINK_URL", "https://go.driverforge.test")

	if got := orgLinkURL("plan", "settings/subscription"); got != dashboardURL {
		t.Errorf("orgLinkURL with no org = %q, want %q", got, dashboardURL)
	}
}

// The plan-limit and suspension conditions carry the org-scoped links, so the user
// lands on the right organization's settings rather than a generic dashboard.
func TestExecuteErrCarriesOrgLinks(t *testing.T) {
	isolateConfig(t)
	t.Setenv("DRIVERFORGE_SHORTLINK_URL", "https://go.driverforge.test")
	if err := orgctx.SetCurrent(orgctx.Org{ID: "org-1", Slug: "acme"}); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name     string
		err      error
		wantHint string
	}{
		{"plan limit", driverforgeapi.ErrProjectLimit, "upgrade your plan at https://go.driverforge.test/plan?org=acme"},
		{"suspended", driverforgeapi.ErrSubscriptionSuspended, "update billing at https://go.driverforge.test/billing?org=acme"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := executeErr(c.err)
			var ue *clierr.UserError
			if !errors.As(err, &ue) {
				t.Fatalf("not a UserError: %T %v", err, err)
			}
			if ue.Hint != c.wantHint {
				t.Errorf("hint = %q, want %q", ue.Hint, c.wantHint)
			}
		})
	}
}
