package cli

import (
	"bytes"
	"testing"

	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/orgctx"
)

func isolateConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)            // darwin
	t.Setenv("XDG_CONFIG_HOME", dir) // linux
}

// `driverforge org current` prints just the slug when an org is selected.
func TestOrgCurrentPrintsSlug(t *testing.T) {
	isolateConfig(t)
	if err := orgctx.SetCurrent(orgctx.Org{ID: "id-1", Name: "Acme", Slug: "acme"}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newOrgCurrentCmd()
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("org current = %v", err)
	}
	if got := out.String(); got != "acme\n" {
		t.Errorf("stdout = %q, want \"acme\\n\"", got)
	}
}

// With no org selected it stays silent (renders nothing) and signals a silent,
// non-reportable error so the process exits non-zero without noise.
func TestOrgCurrentSilentWhenUnset(t *testing.T) {
	isolateConfig(t)

	var out bytes.Buffer
	cmd := newOrgCurrentCmd()
	cmd.SetOut(&out)
	// Mirror the root command's settings, under which usage/errors are silenced
	// and rendering is handled by Execute (which prints nothing for a Silent).
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected a (silent) error when no org is selected")
	}
	if !clierr.IsUser(err) {
		t.Errorf("silent error should be a non-reportable UserError, got %T", err)
	}
	if out.Len() != 0 {
		t.Errorf("expected no stdout, got %q", out.String())
	}
}
