package cli

import (
	"strings"
	"testing"

	"github.com/driverforge/cli/internal/agent"
	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/orgctx"
	"github.com/driverforge/cli/internal/ui"
)

func TestToTreeNodesMapsHierarchy(t *testing.T) {
	roots := []agent.ProjectNode{{
		ID: "site", Name: "Home", Type: "site",
		Children: []agent.ProjectNode{
			{ID: "lr", Name: "Living Room", Type: "room", Selectable: true},
		},
	}}
	got := toTreeNodes(roots)
	if len(got) != 1 || got[0].Name != "Home" || got[0].Type != "site" {
		t.Fatalf("root not mapped: %+v", got)
	}
	if len(got[0].Children) != 1 || !got[0].Children[0].Selectable || got[0].Children[0].ID != "lr" {
		t.Errorf("child not mapped: %+v", got[0].Children)
	}
}

func TestAnySelectable(t *testing.T) {
	none := []ui.TreeNode{{Name: "Site", Children: []ui.TreeNode{{Name: "Floor"}}}}
	if anySelectable(none) {
		t.Error("tree with no selectable nodes should report false")
	}
	some := []ui.TreeNode{{Name: "Site", Children: []ui.TreeNode{{Name: "Room", Selectable: true}}}}
	if !anySelectable(some) {
		t.Error("nested selectable node should report true")
	}
}

func TestDriverDisplayName(t *testing.T) {
	withName := []byte(`<devicedata>
  <name>Acme Thermostat</name>
  <version>000001</version>
</devicedata>`)
	if got := driverDisplayName(withName, "acme-dev.c4z"); got != "Acme Thermostat" {
		t.Errorf("should read driver.xml <name>, got %q", got)
	}
	// No <name> → fall back to the filename stem.
	if got := driverDisplayName([]byte(`<devicedata></devicedata>`), "acme-dev.c4z"); got != "acme-dev" {
		t.Errorf("fallback name = %q, want acme-dev", got)
	}
}

func TestBootstrapAgentErrIsFriendly(t *testing.T) {
	ae := &agent.Error{Status: 503, Code: "director_unreachable", Message: "director is offline"}
	err := bootstrapAgentErr(orgctx.Device{Name: "Ctrl"}, ae)
	if !clierr.IsUser(err) {
		t.Fatalf("an agent-reported bootstrap failure should be friendly, got %T", err)
	}
	if !strings.Contains(err.Error(), "irector is offline") {
		t.Errorf("agent message should be surfaced: %q", err.Error())
	}
}
