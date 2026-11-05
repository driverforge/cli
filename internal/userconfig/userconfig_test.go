package userconfig

import (
	"os"
	"path/filepath"
	"testing"
)

type settings struct {
	Name string `json:"name"`
}

func TestSaveLoad(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if got := Load[settings]("s.json"); got != (settings{}) {
		t.Errorf("missing file = %+v, want the zero value", got)
	}
	if err := Save("s.json", settings{Name: "acme"}); err != nil {
		t.Fatal(err)
	}
	if got := Load[settings]("s.json"); got.Name != "acme" {
		t.Errorf("Load = %+v, want what was saved", got)
	}
	info, err := os.Stat(Path("s.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v (%v), want 0600", info.Mode().Perm(), err)
	}
	if err := os.WriteFile(Path("s.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Load[settings]("s.json"); got != (settings{}) {
		t.Errorf("corrupt file = %+v, want the zero value", got)
	}
	if filepath.Base(filepath.Dir(Path("s.json"))) != "driverforge" {
		t.Errorf("Path = %q, want it under driverforge/", Path("s.json"))
	}
}
