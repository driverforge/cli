package skills

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const devCycle = "anvil-driver-dev-cycle"

func TestListReadsFrontmatter(t *testing.T) {
	list, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != devCycle {
		t.Fatalf("List() = %+v, want the one dev-cycle skill", list)
	}
	if !strings.Contains(list[0].Description, "Control4 driver") {
		t.Errorf("description = %q", list[0].Description)
	}
}

func TestFilesStampTheCLIVersion(t *testing.T) {
	files, err := Files(devCycle, "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	for rel, b := range files {
		if strings.Contains(string(b), versionPlaceholder) {
			t.Errorf("%s still holds the version placeholder", rel)
		}
	}
	if !strings.Contains(string(files["SKILL.md"]), `driverforge-cli: "1.2.3"`) {
		t.Error("SKILL.md frontmatter doesn't carry the CLI version")
	}
}

func TestFilesLinkOnlyToFilesThatExist(t *testing.T) {
	files, err := Files(devCycle, "dev")
	if err != nil {
		t.Fatal(err)
	}
	for rel, b := range files {
		for _, link := range markdownLinks(string(b)) {
			if strings.Contains(link, "://") {
				continue
			}
			target := filepath.ToSlash(filepath.Join(filepath.Dir(rel), link))
			if _, ok := files[target]; !ok {
				t.Errorf("%s links to %s, which the skill doesn't ship", rel, link)
			}
		}
	}
}

// markdownLinks returns the targets of [text](target) links.
func markdownLinks(doc string) []string {
	var out []string
	for {
		i := strings.Index(doc, "](")
		if i < 0 {
			return out
		}
		doc = doc[i+2:]
		j := strings.IndexByte(doc, ')')
		if j < 0 {
			return out
		}
		out = append(out, doc[:j])
		doc = doc[j:]
	}
}

func TestInstallThenReinstall(t *testing.T) {
	root := t.TempDir()
	res, err := Install(root, devCycle, "1.0.0", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != Installed || res.Files < 2 {
		t.Fatalf("first install = %+v", res)
	}
	skill := filepath.Join(root, devCycle, "SKILL.md")
	if _, err := os.Stat(skill); err != nil {
		t.Fatal(err)
	}

	if res, err = Install(root, devCycle, "1.0.0", false); err != nil || res.Action != Unchanged {
		t.Fatalf("same install again = %+v, %v; want unchanged", res, err)
	}

	stale := filepath.Join(root, devCycle, "references", "removed.md")
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res, err = Install(root, devCycle, "1.1.0", false); err != nil || res.Action != Updated {
		t.Fatalf("install from a newer CLI = %+v, %v; want updated", res, err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("a file the previous install didn't ship survived the update")
	}
	b, _ := os.ReadFile(skill)
	if !strings.Contains(string(b), `"1.1.0"`) {
		t.Error("the update didn't restamp the version")
	}
}

func TestInstallLeavesAForeignDirectoryAlone(t *testing.T) {
	root := t.TempDir()
	mine := filepath.Join(root, devCycle, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(mine), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mine, []byte("someone else's"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(root, devCycle, "1.0.0", false); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("err = %v, want ErrNotOwned", err)
	}
	if b, _ := os.ReadFile(mine); string(b) != "someone else's" {
		t.Fatal("the foreign file was overwritten")
	}
	if res, err := Install(root, devCycle, "1.0.0", true); err != nil || res.Action != Updated {
		t.Fatalf("forced install = %+v, %v", res, err)
	}
}

func TestInstallUnknownSkill(t *testing.T) {
	if _, err := Install(t.TempDir(), "nope", "1.0.0", false); !errors.Is(err, ErrUnknown) {
		t.Fatalf("err = %v, want ErrUnknown", err)
	}
}

func TestInstalledInListsOnlyWhatInstallWrote(t *testing.T) {
	root := t.TempDir()
	if names, err := InstalledIn(filepath.Join(root, "missing")); err != nil || names != nil {
		t.Fatalf("missing root = %v, %v", names, err)
	}
	if _, err := Install(root, devCycle, "1.0.0", false); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "someone-elses"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A copied directory: the marker names another skill.
	if err := os.CopyFS(filepath.Join(root, "renamed"), os.DirFS(filepath.Join(root, devCycle))); err != nil {
		t.Fatal(err)
	}
	names, err := InstalledIn(root)
	if err != nil || len(names) != 1 || names[0] != devCycle {
		t.Fatalf("InstalledIn = %v, %v; want only %s", names, err, devCycle)
	}
}
