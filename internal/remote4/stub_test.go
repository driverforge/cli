package remote4

import (
	"archive/zip"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBundleBakesConfigAndPreloads(t *testing.T) {
	cfg := StubConfig{
		SessionID: "s1", UserID: "u1", Token: "jwt \"quoted\"", Handlers: []string{"OnDriverInit", "ReceivedFromProxy"},
		BrokerHost: "broker.test", BrokerPort: 8883, BrokerTLS: true, SkipHostnameCheck: true,
		CAPEM:       "-----BEGIN CERTIFICATE-----\nAAA\n-----END CERTIFICATE-----\n",
		DriverToken: "drv_x", StubVersion: "1.2.3", RelayKey: "abcd",
	}
	b := string(Bundle(cfg, []byte("-- sdk\nDriverforge = {}\n")))
	for _, want := range []string{
		`session = "s1"`, `userId = "u1"`, `token = "jwt \"quoted\""`, `driverToken = "drv_x"`, `relayKey = "abcd"`,
		`handlers = {"OnDriverInit", "ReceivedFromProxy"}`,
		`host = "broker.test", port = 8883, tls = true, skipHostnameCheck = true, caPem = [[`,
		`package.preload["json"] = function(...)`,
		`package.preload["vendor.driverforge-sdk"] = function(...)`,
		"-- The Remote4 stub",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("bundle missing %q", want)
		}
	}
	if !strings.HasPrefix(b, "-- Remote4 stub") {
		t.Errorf("bundle should open with its banner, got %q", b[:40])
	}
}

func TestNewRelayKeyIsLongRandomHex(t *testing.T) {
	a, err := NewRelayKey()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewRelayKey()
	if len(a) != 32 || a == b || strings.Trim(a, "0123456789abcdef") != "" {
		t.Errorf("keys %q %q", a, b)
	}
}

func TestBundleOmitsSDKWithoutDriverToken(t *testing.T) {
	b := string(Bundle(StubConfig{SessionID: "s", UserID: "u", Handlers: nil}, []byte("sdk")))
	if strings.Contains(b, `package.preload["vendor.driverforge-sdk"]`) {
		t.Error("SDK preloaded without a driver token")
	}
	if !strings.Contains(b, "handlers = {}") {
		t.Error("empty handlers should render as an empty table")
	}
}

func TestLuaStringEscapesLongBracketsInside(t *testing.T) {
	got := luaString("a\n]]b")
	if !strings.HasPrefix(got, "[=[\n") || !strings.HasSuffix(got, "]=]") {
		t.Errorf("long string with ]] inside must raise the bracket level, got %q", got)
	}
}

// The generated bundle must at least compile under the dialect it ships to.
func TestBundleCompilesUnderLua(t *testing.T) {
	lua, err := FindLua("")
	if err != nil {
		t.Skip("no lua interpreter on PATH")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "stub.lua")
	cfg := StubConfig{SessionID: "s", UserID: "u", Handlers: []string{"ReceivedFromProxy"}, CAPEM: "x\ny"}
	if err := os.WriteFile(path, Bundle(cfg, []byte("Driverforge = { Init = function() end }\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	// luac is not always installed; loading without running is enough.
	out, err := exec.Command(lua, "-e", `local f, e = loadfile(arg[1]); if not f then error(e) end`, path).CombinedOutput()
	if err != nil {
		t.Fatalf("bundle does not compile: %v\n%s", err, out)
	}
}

func TestRewriteC4zReplacesOnlyTheScript(t *testing.T) {
	xml := `<devicedata><config><script file="driver.lua" jit="1"/></config></devicedata>`
	src := zipWithFiles(t, map[string]string{
		"driver.xml":  xml,
		"driver.lua":  "real driver",
		"www/doc.htm": "<p>docs</p>",
	})
	out := filepath.Join(t.TempDir(), "stub.c4z")
	if err := RewriteC4z(src, out, []byte("STUB")); err != nil {
		t.Fatal(err)
	}
	got := readZip(t, out)
	if got["driver.lua"] != "STUB" {
		t.Errorf("driver.lua = %q, want STUB", got["driver.lua"])
	}
	if got["driver.xml"] != xml {
		t.Error("driver.xml must be byte-identical")
	}
	if got["www/doc.htm"] != "<p>docs</p>" {
		t.Error("other entries must be preserved")
	}
}

func TestRewriteC4zRefusesEncryptedAndMissingScript(t *testing.T) {
	enc := zipWithFiles(t, map[string]string{
		"driver.xml":           `<devicedata><config><script file="driver.lua.encrypted" encryption="2"/></config></devicedata>`,
		"driver.lua.encrypted": "x",
	})
	if err := RewriteC4z(enc, filepath.Join(t.TempDir(), "o.c4z"), []byte("S")); err == nil || !strings.Contains(err.Error(), "encrypted") {
		t.Errorf("want encrypted refusal, got %v", err)
	}
	missing := zipWithFiles(t, map[string]string{
		"driver.xml": `<devicedata><config><script file="driver.lua"/></config></devicedata>`,
	})
	if err := RewriteC4z(missing, filepath.Join(t.TempDir(), "o.c4z"), []byte("S")); err == nil || !strings.Contains(err.Error(), "no driver.lua entry") {
		t.Errorf("want missing-entry refusal, got %v", err)
	}
}

func readZip(t *testing.T, path string) map[string]string {
	t.Helper()
	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	out := map[string]string{}
	for _, f := range r.File {
		b, err := readEntry(f)
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name] = string(bytes.TrimSpace(b))
	}
	return out
}

// zipWithFiles writes a zip with the given entries and returns its path.
func zipWithFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "in.c4z")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	for name, content := range files {
		e, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// The controller's MQTT client logs a line per packet. The SDK ships every Log
// call to Anvil, so a stub that forwarded those lines flooded the project's
// logs with its own transport chatter. Connection trouble already arrives via
// OnConnect/OnDisconnect; the per-packet log must stay unhooked.
func TestStubLeavesMQTTClientLogUnhooked(t *testing.T) {
	runStubCheck(t, StubConfig{SessionID: "s", UserID: "u", BrokerHost: "h", BrokerPort: 1883}, `
local hooked = false
local client = setmetatable({ OnLog = function() hooked = true end },
    { __index = function() return function() end end })
C4 = setmetatable({ MQTT = function() return client end },
    { __index = function() return function() end end })
dofile(arg[1])
OnDriverInit('DIT_STARTUP')
assert(not hooked, 'stub registered an MQTT client log hook')
`)
}
