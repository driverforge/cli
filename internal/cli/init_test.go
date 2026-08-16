package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/driverforge/cli/internal/auth"
	"github.com/driverforge/cli/internal/driverforgeapi"
	"github.com/driverforge/cli/internal/initdriver"
)

func TestDetectLogger(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"Log colon", `Log:Info("hi")`, "Log"},
		{"Log dot", `Log.info("hi")`, "Log"},
		{"PascalCase Logger", `Logger:Warn("x")`, "Logger"},
		{"lowercase logger", `logger:debug("x")`, "logger"},
		{"underscore logger", `_logger.trace("x")`, "_logger"},
		{"no logger", "local x = 1\ndoThing()\n", ""},
		{"substring is not a boundary match", `myLogger:Info("x")`, ""},
		{"logger only in a line comment", "-- Log:Info was removed\nlocal x = 1\n", ""},
		{"logger only in a string", `error("call Log:Info to trace")`, ""},
		{"commented logger, real call wins", "-- old logger removed\nLogger:Warn(\"x\")\n", "Logger"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "driver.lua"), []byte(tt.body), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := detectLogger(dir, "driver.lua"); got != tt.want {
				t.Errorf("detectLogger = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDetectLoggerMissingFile(t *testing.T) {
	if got := detectLogger(t.TempDir(), "nope.lua"); got != "" {
		t.Errorf("missing entry file should yield \"\", got %q", got)
	}
}

func TestFindProjectBySlug(t *testing.T) {
	projects := []driverforgeapi.Project{
		{ID: "p1", Slug: "lights", Name: "Lights"},
		{ID: "p2", Slug: "hvac", Name: "HVAC"},
	}
	if got := findProjectBySlug(projects, "hvac"); got == nil || got.ID != "p2" {
		t.Errorf("findProjectBySlug(hvac) = %+v", got)
	}
	if got := findProjectBySlug(projects, "missing"); got != nil {
		t.Errorf("expected nil for missing slug, got %+v", got)
	}
}

// stubGraphAuth isolates config and stores a token so driverforgeapi calls authorise.
func stubGraphAuth(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("DRIVERFORGE_CREDENTIALS_BACKEND", "file")
	t.Setenv("DRIVERFORGE_AUTH_URL", "https://auth.example.test")
	if err := auth.Save(auth.Credentials{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
}

// The logger global is spliced verbatim into generated Lua, so an invalid name
// (which could inject code) is rejected; valid identifiers pass.
func TestLuaGlobalValidation(t *testing.T) {
	valid := []string{"Log", "logger", "_log", "my.logger", "A1.b2.c3"}
	for _, v := range valid {
		if !luaGlobalRe.MatchString(v) {
			t.Errorf("%q should be a valid Lua global", v)
		}
	}
	invalid := []string{"", "1log", "Log }); os.exit((", "a b", "a-b", "a..b", ".a", "a."}
	for _, v := range invalid {
		if luaGlobalRe.MatchString(v) {
			t.Errorf("%q should be rejected", v)
		}
	}
}

// A non-ENOENT read (a directory where a file is expected) surfaces as an error
// from fileChange, not a Create that rollback could turn into a deletion.
func TestFileChangeSurfacesReadError(t *testing.T) {
	dir := t.TempDir()
	clash := filepath.Join(dir, "thing")
	if err := os.MkdirAll(clash, 0o755); err != nil {
		t.Fatal(err)
	}
	_, ok, err := fileChange(dir, clash, []byte("x"))
	if err == nil {
		t.Fatal("a non-ENOENT read must surface as an error")
	}
	if ok {
		t.Error("no change should be reported on a read error")
	}

	// A genuinely absent file is still a Create.
	ch, ok, err := fileChange(dir, filepath.Join(dir, "new.txt"), []byte("x"))
	if err != nil || !ok || ch.Kind != initdriver.Create {
		t.Errorf("absent file should plan a Create: ok=%v kind=%v err=%v", ok, ch.Kind, err)
	}
}
