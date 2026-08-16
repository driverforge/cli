package cli

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestCommandLabel(t *testing.T) {
	root := &cobra.Command{Use: "driverforge"}
	org := &cobra.Command{Use: "org"}
	list := &cobra.Command{Use: "list"}
	build := &cobra.Command{Use: "build"}
	org.AddCommand(list)
	root.AddCommand(org, build)

	if got := commandLabel(list); got != "org list" {
		t.Errorf("nested command = %q, want \"org list\"", got)
	}
	if got := commandLabel(build); got != "build" {
		t.Errorf("leaf command = %q, want \"build\"", got)
	}
	if got := commandLabel(root); got != "driverforge" {
		t.Errorf("root command = %q, want \"driverforge\"", got)
	}
}

func TestChangedFlags(t *testing.T) {
	cmd := &cobra.Command{Use: "build"}
	var env string
	var deploy bool
	cmd.Flags().StringVar(&env, "env", "", "")
	cmd.Flags().BoolVar(&deploy, "deploy", false, "")
	_ = cmd.Flags().Set("env", "dev")

	got := changedFlags(cmd)
	if len(got) != 1 || got[0] != "env" {
		t.Errorf("changedFlags = %v, want [env]", got)
	}
}

func TestInvocationOf(t *testing.T) {
	root := &cobra.Command{Use: "driverforge"}
	org := &cobra.Command{Use: "org"}
	sel := &cobra.Command{Use: "select"}
	org.AddCommand(sel)
	root.AddCommand(org)

	inv := invocationOf(sel, []string{"acme-corp"})
	if inv.Command != "org select" {
		t.Errorf("Command = %q, want \"org select\"", inv.Command)
	}
	// Only the COUNT of positional args travels — never the values, which
	// carry user data (slugs, device names, paths).
	if inv.ArgCount != 1 {
		t.Errorf("ArgCount = %d, want 1", inv.ArgCount)
	}
}

// The post-run "an update is available" notice must not fire after `upgrade`:
// its table already reports the same thing, and after a successful self-upgrade
// this process still carries the version it started with, so the notice would
// announce an update the user had just installed.
func TestUpgradeSuppressesTheUpdateNotice(t *testing.T) {
	if !ownsTheUpdateStory(&cobra.Command{Use: "upgrade"}) {
		t.Error("upgrade should suppress the post-run update notice")
	}
	for _, name := range []string{"build", "deploy", "init"} {
		if ownsTheUpdateStory(&cobra.Command{Use: name}) {
			t.Errorf("%s should still get the update notice", name)
		}
	}
}

// The suppression must not ride on isAuthOrMetaCmd, which also gates the
// metrics consent prompt — `upgrade` does real work worth measuring.
func TestUpgradeStillCountsAsAMeasuredCommand(t *testing.T) {
	if isAuthOrMetaCmd(&cobra.Command{Use: "upgrade"}) {
		t.Error("upgrade must not be treated as a meta command; that would drop its metrics")
	}
}
