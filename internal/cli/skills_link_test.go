package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func claudeLinkTarget(t *testing.T, root string) string {
	t.Helper()
	got, err := os.Readlink(filepath.Join(root, ".claude", "skills", "anvil-driver-dev-cycle"))
	if err != nil {
		return ""
	}
	return got
}

const wantClaudeLink = "../../.agents/skills/anvil-driver-dev-cycle"

// With .claude at the repo root, install links the skill into it and reports
// the link; a reinstall leaves it alone.
func TestSkillsInstallLinksIntoClaude(t *testing.T) {
	repo, _ := skillsUpdateEnv(t)
	if err := os.Mkdir(filepath.Join(repo, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := run(t, repo, "skills", "install")
	if code != 0 {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	got := decode(t, stdout)["skills"].([]any)[0].(map[string]any)
	if got["linked"] != ".claude/skills/anvil-driver-dev-cycle" {
		t.Errorf("linked = %v", got["linked"])
	}
	if l := claudeLinkTarget(t, repo); l != wantClaudeLink {
		t.Errorf("link target = %q", l)
	}
	stdout, _, _ = run(t, repo, "skills", "install")
	if decode(t, stdout)["skills"].([]any)[0].(map[string]any)["linked"] == nil {
		t.Error("an existing correct link should still be reported")
	}
}

// Without .claude, install creates neither it nor a link.
func TestSkillsInstallWithoutDotClaude(t *testing.T) {
	repo, _ := skillsUpdateEnv(t)
	stdout, _, code := run(t, repo, "skills", "install")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stdout)
	}
	if _, ok := decode(t, stdout)["skills"].([]any)[0].(map[string]any)["linked"]; ok {
		t.Error("reported a link with no .claude")
	}
	if _, err := os.Stat(filepath.Join(repo, ".claude")); !os.IsNotExist(err) {
		t.Error(".claude was created")
	}
}

// A foreign directory at the link path is refused without --force.
func TestSkillsInstallRefusesForeignClaudeSkill(t *testing.T) {
	repo, _ := skillsUpdateEnv(t)
	foreign := filepath.Join(repo, ".claude", "skills", "anvil-driver-dev-cycle")
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	stdout, code := runFailing(t, "--project-dir", repo, "skills", "install")
	if code != 1 || decode(t, stdout)["error"].(map[string]any)["code"] != codeSkillConflict {
		t.Fatalf("exit %d, %s", code, stdout)
	}
	if _, _, code := run(t, repo, "skills", "install", "--force"); code != 0 {
		t.Fatalf("--force exit %d", code)
	}
	if l := claudeLinkTarget(t, repo); l != wantClaudeLink {
		t.Errorf("link target = %q", l)
	}
}

// --global links into ~/.claude/skills; --path never links.
func TestSkillsInstallGlobalLinksAndPathDoesNot(t *testing.T) {
	t.Setenv("DRIVERFORGE_OUTPUT", "json")
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.Mkdir(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, code := run(t, t.TempDir(), "skills", "install", "--global"); code != 0 {
		t.Fatal("install --global failed")
	}
	if l := claudeLinkTarget(t, home); l != wantClaudeLink {
		t.Errorf("global link target = %q", l)
	}

	other := t.TempDir()
	if err := os.Mkdir(filepath.Join(other, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(other, ".agents", "skills")
	if _, _, code := run(t, t.TempDir(), "skills", "install", "--path", dir); code != 0 {
		t.Fatal("install --path failed")
	}
	if l := claudeLinkTarget(t, other); l != "" {
		t.Errorf("--path linked: %q", l)
	}
}

// update adds the link alongside the refreshed copy when .claude exists.
func TestSkillsUpdateLinks(t *testing.T) {
	repo, _ := skillsUpdateEnv(t)
	if err := os.Mkdir(filepath.Join(repo, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, code := run(t, repo, "skills", "update"); code != 0 {
		t.Fatal("update failed")
	}
	if l := claudeLinkTarget(t, repo); l != wantClaudeLink {
		t.Errorf("link target = %q", l)
	}
}
