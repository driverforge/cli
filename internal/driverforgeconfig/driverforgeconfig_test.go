package driverforgeconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/driverforge/cli/internal/clierr"
)

func TestLoadAbsentReturnsNil(t *testing.T) {
	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("absent config should not error: %v", err)
	}
	if cfg != nil {
		t.Fatalf("absent config should be nil, got %+v", cfg)
	}
}

func TestMarshalLoadRoundTrip(t *testing.T) {
	root := t.TempDir()
	in := Config{
		Org:          "acme",
		Project:      Project{Slug: "lights", Name: "Lights"},
		SDKVersion:   "1.2.3",
		BuildTargets: []string{"debug", "release"},
		Logger:       "Log",
		Entry:        "driver.lua",
	}
	data, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"schema": 1`) {
		t.Errorf("schema not stamped: %s", data)
	}
	if strings.Contains(string(data), "drv_") {
		t.Errorf("config must not contain secrets: %s", data)
	}

	if err := os.MkdirAll(Dir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(root), data, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("Load returned no config")
	}
	for _, f := range []struct{ name, got, want string }{
		{"Org", got.Org, "acme"},
		{"Project.Slug", got.Project.Slug, "lights"},
		{"Project.Name", got.Project.Name, "Lights"},
		{"SDKVersion", got.SDKVersion, "1.2.3"},
		{"Schema", strconv.Itoa(got.Schema), strconv.Itoa(Schema)},
	} {
		if f.got != f.want {
			t.Errorf("%s = %q, want %q", f.name, f.got, f.want)
		}
	}
	if len(got.BuildTargets) != 2 {
		t.Errorf("build targets lost: %+v", got.BuildTargets)
	}
}

// The configurations block is an optional defaults overlay: it must survive a
// round-trip when present and stay omitted from the JSON when absent, so
// existing config files and zero-config projects are untouched.
func TestMarshalLoadConfigurationsRoundTrip(t *testing.T) {
	root := t.TempDir()
	enc, sfx := true, false
	data, err := Marshal(Config{
		Project: Project{Slug: "lights"},
		Configurations: map[string]Configuration{
			"prod": {Encrypt: &enc, Suffix: &sfx},
			"dev":  {},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(Dir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(root), data, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	prod := got.Configurations["prod"]
	if prod.Encrypt == nil || !*prod.Encrypt || prod.Suffix == nil || *prod.Suffix {
		t.Errorf("prod entry lost in round-trip: %+v", prod)
	}
	if dev, ok := got.Configurations["dev"]; !ok || dev.Encrypt != nil || dev.Suffix != nil {
		t.Errorf("empty dev entry should round-trip with nil fields: %+v", dev)
	}

	bare, err := Marshal(Config{Project: Project{Slug: "p"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bare), "configurations") {
		t.Errorf("absent configurations must be omitted from the JSON: %s", bare)
	}
}

func TestMarshalNilTargetsBecomesEmptyArray(t *testing.T) {
	data, err := Marshal(Config{Project: Project{Slug: "p"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"buildTargets": []`) {
		t.Errorf("nil targets should render as []: %s", data)
	}
}

func TestPathLayout(t *testing.T) {
	if got, want := Path("/x"), filepath.Join("/x", ".driverforge", "config.json"); got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
}

// Broken JSON in the committed, hand-editable config is an anticipated
// condition — it must surface as a friendly UserError, not a crash.
func TestLoadCorruptConfigIsUserError(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(Dir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(root), []byte("{ definitely not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(root)
	var ue *clierr.UserError
	if !errors.As(err, &ue) {
		t.Fatalf("error = %v, want a clierr.UserError", err)
	}
	if ue.Title != "Corrupt .driverforge/config.json" {
		t.Errorf("title = %q, want Corrupt .driverforge/config.json", ue.Title)
	}
}
