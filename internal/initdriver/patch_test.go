package initdriver

import (
	"bytes"
	"strings"
	"testing"

	"github.com/driverforge/cli/pkg/c4z/manifest"
)

func TestPatchEntryWrapsBodyInline(t *testing.T) {
	src := []byte(`require('lib.logger')

function OnDriverInit()
    Log:Info("hi")
    for k, v in pairs(Properties) do
        OnPropertyChanged(k)
    end
end

function OnDriverLateInit(strDIR)
    Log:Info("loaded")
end
`)
	out, warn := PatchEntry(src, Decision{ProjectToken: "drv_abc", LoggerGlobal: "Log"})
	if warn != "" {
		t.Fatalf("unexpected warning: %s", warn)
	}
	s := string(out)

	if !strings.Contains(s, `require('vendor.anvil-sdk')`) {
		t.Error("missing vendor require")
	}
	if !strings.Contains(s, `Anvil:Init("drv_abc", C4:GetDriverFileName(), { logger = Log })`) {
		t.Errorf("Anvil:Init not rendered as expected:\n%s", s)
	}
	if !strings.Contains(s, "Anvil:OnDriverInit(function(strDIR)") {
		t.Error("body not wrapped in Anvil:OnDriverInit")
	}
	// The original body must survive, re-indented, inside the wrapper.
	if !strings.Contains(s, `        OnPropertyChanged(k)`) {
		t.Errorf("original body not preserved/re-indented:\n%s", s)
	}
	// OnDriverLateInit (and its own end) must be untouched and still present.
	if !strings.Contains(s, "function OnDriverLateInit(strDIR)") {
		t.Error("late init clobbered")
	}
	// Balanced: equal function/end counts are a quick structural sanity check.
	if got := strings.Count(s, "function ") - strings.Count(s, "\nend"); got < 0 {
		t.Errorf("unbalanced ends after patch:\n%s", s)
	}
	// No config require in inline mode.
	if strings.Contains(s, "require('config')") {
		t.Error("inline mode should not add require('config')")
	}
}

func TestPatchEntryReusesFirstParamAsDir(t *testing.T) {
	src := []byte(`function OnDriverInit(strDIR)
    doStuff(strDIR)
end
`)
	out, _ := PatchEntry(src, Decision{ProjectToken: "k"})
	s := string(out)
	if !strings.Contains(s, "Anvil:OnDriverInit(function(strDIR)") {
		t.Errorf("expected strDIR param reused:\n%s", s)
	}
	if !strings.Contains(s, "end, strDIR)") {
		t.Errorf("expected strDIR passed through:\n%s", s)
	}
}

func TestPatchEntryBuildTargetsAddConfigRequireButStillInlineKey(t *testing.T) {
	src := []byte("function OnDriverInit()\nend\n")
	out, _ := PatchEntry(src, Decision{ProjectToken: "drv_k", BuildTargets: true, Targets: []string{"debug"}})
	s := string(out)
	if !strings.HasPrefix(s, "require('config')\n") {
		t.Errorf("build-target mode should prepend require('config'):\n%s", s)
	}
	// The Driverforge key is always inlined — build targets are for other per-build
	// config, not for routing this (non-secret) key through the CONFIG table.
	if !strings.Contains(s, `Anvil:Init("drv_k", C4:GetDriverFileName())`) {
		t.Errorf("key should be inlined even with build targets:\n%s", s)
	}
	if strings.Contains(s, "CONFIG.DRIVERFORGE_API_KEY") {
		t.Errorf("Driverforge key must not route through the CONFIG table:\n%s", s)
	}
}

func TestPatchEntryIdempotent(t *testing.T) {
	src := []byte("function OnDriverInit()\n    x()\nend\n")
	once, _ := PatchEntry(src, Decision{ProjectToken: "k", LoggerGlobal: "Log"})
	twice, _ := PatchEntry(once, Decision{ProjectToken: "k", LoggerGlobal: "Log"})
	if string(once) != string(twice) {
		t.Errorf("second patch changed the file:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
}

func TestPatchEntryNoHandlerAddsOne(t *testing.T) {
	src := []byte("-- a driver with no init\nlocal x = 1\n")
	out, warn := PatchEntry(src, Decision{ProjectToken: "k"})
	if warn == "" {
		t.Error("expected a warning when adding a fresh OnDriverInit")
	}
	if !strings.Contains(string(out), "function OnDriverInit(...)") {
		t.Errorf("expected a new OnDriverInit:\n%s", out)
	}
}

func TestPatchEntryUnbalancedFallsBackWithTodo(t *testing.T) {
	// Missing the closing end — matchingEnd cannot balance.
	src := []byte("function OnDriverInit()\n    if x then\n        y()\n")
	out, warn := PatchEntry(src, Decision{ProjectToken: "k"})
	if warn == "" {
		t.Error("expected a warning on unbalanced body")
	}
	s := string(out)
	if !strings.Contains(s, "TODO(driverforge)") {
		t.Errorf("expected a TODO marker in fallback:\n%s", s)
	}
	if !strings.Contains(s, "require('vendor.anvil-sdk')") {
		t.Error("fallback should still wire the SDK require")
	}
}

func TestPatchEntryIgnoresKeywordsInStringsAndComments(t *testing.T) {
	src := []byte(`function OnDriverInit()
    -- this comment says end end end function if do
    local s = "end end if function"
    real()
end
trailer()
`)
	out, warn := PatchEntry(src, Decision{ProjectToken: "k"})
	if warn != "" {
		t.Fatalf("unexpected warning: %s", warn)
	}
	s := string(out)
	if !strings.Contains(s, "trailer()") {
		t.Errorf("matched the wrong end — trailer lost:\n%s", s)
	}
	if !strings.Contains(s, "real()") {
		t.Errorf("body lost:\n%s", s)
	}
}

func TestPatchManifestAddsVendorAndSquish(t *testing.T) {
	src := []byte(`<Driver type="c4z" name="acme_driver">
  <Items>
    <Item type="file" name="driver.lua"/>
    <Item type="file" name="driver.xml"/>
  </Items>
</Driver>
`)
	out, err := PatchManifest(src)
	if err != nil {
		t.Fatal(err)
	}
	d, err := manifest.ParseBytes(out, "manifest.c4zproj")
	if err != nil {
		t.Fatalf("patched manifest no longer parses: %v\n%s", err, out)
	}
	if !d.SquishLua {
		t.Errorf("squishLua not enabled:\n%s", out)
	}
	if !hasVendorItem(d) {
		t.Errorf("vendor item not added:\n%s", out)
	}
}

func TestPatchManifestIdempotent(t *testing.T) {
	src := []byte(`<Driver type="c4z" name="acme_driver" squishLua="true">
  <Items>
    <Item type="dir" c4zDir="vendor" name="vendor" recurse="true" exclude="false"/>
    <Item type="file" name="driver.lua"/>
  </Items>
</Driver>
`)
	out, err := PatchManifest(src)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(src) {
		t.Errorf("already-satisfied manifest was modified:\n%s", out)
	}
}

// A multi-line long-string literal in the OnDriverInit body must survive
// wrapping byte-for-byte — reindent must not prepend padding inside [[ … ]].
func TestPatchEntryPreservesLongStringLiteral(t *testing.T) {
	src := []byte(`function OnDriverInit()
    local xml = [[
<config>
  <item value="1"/>
</config>
]]
    C4:SetVariable("X", xml)
end
`)
	out, warn := PatchEntry(src, Decision{ProjectToken: "drv_abc"})
	if warn != "" {
		t.Fatalf("unexpected warning: %s", warn)
	}
	s := string(out)
	// The literal's interior lines must be unchanged (no leading spaces added).
	for _, want := range []string{"\n<config>\n", "\n  <item value=\"1\"/>\n", "\n</config>\n"} {
		if !strings.Contains(s, want) {
			t.Errorf("long-string interior was reindented (missing %q):\n%s", want, s)
		}
	}
	// The code lines around it are still reindented one level deeper.
	if !strings.Contains(s, `        local xml = [[`) {
		t.Errorf("opening code line not reindented:\n%s", s)
	}
	if !strings.Contains(s, `        C4:SetVariable("X", xml)`) {
		t.Errorf("code after the literal not reindented:\n%s", s)
	}
}

// A fresh OnDriverInit (driver had none) must not pass an undefined strDIR
// global to the callback — a hard error under strict-globals _G.
func TestNewOnDriverInitDoesNotPassUndefinedGlobal(t *testing.T) {
	src := []byte("function OnDriverLateInit() end\n") // no OnDriverInit
	out, _ := PatchEntry(src, Decision{ProjectToken: "drv_x"})
	s := string(out)
	if !strings.Contains(s, "end)\nend") {
		t.Errorf("callback should be closed with `end)`, not `end, strDIR)`:\n%s", s)
	}
	if strings.Contains(s, "end, strDIR)") {
		t.Errorf("fresh OnDriverInit still passes the undefined strDIR:\n%s", s)
	}
}

// A Driver tag whose attributes contain a `$` must be preserved verbatim when
// squishLua is set — Regexp.ReplaceAll would expand $-sequences.
func TestPatchManifestPreservesDollarInDriverTag(t *testing.T) {
	src := []byte(`<Driver name="promo$100" type="c4z">
  <Items>
    <Item type="file" name="driver.lua"/>
  </Items>
</Driver>`)
	out, err := PatchManifest(src)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, `name="promo$100"`) {
		t.Errorf("$ in the Driver tag was mangled:\n%s", s)
	}
	if !strings.Contains(s, `squishLua="true"`) {
		t.Errorf("squishLua not set:\n%s", s)
	}
}

// Only the matched <Driver> tag is rewritten, even if the string "<Driver" also
// appears elsewhere (e.g. in a prepackage command).
func TestPatchManifestRewritesOnlyTheDriverTag(t *testing.T) {
	src := []byte(`<Driver name="d" type="c4z">
  <Items><Item type="file" name="driver.lua"/></Items>
  <PrepackageCommands><PrepackageCommand>echo "&lt;Driver stuff"</PrepackageCommand></PrepackageCommands>
</Driver>`)
	out, err := PatchManifest(src)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(out), `squishLua="true"`); n != 1 {
		t.Errorf("expected exactly one squishLua insertion, got %d:\n%s", n, out)
	}
}

// A commented-out Anvil:Init must not make the driver look already-wired: the
// SDK wiring still gets added.
func TestPatchEntryCommentedInitIsNotAlreadyPatched(t *testing.T) {
	src := []byte("-- Anvil:Init(\"old\", C4:GetDriverFileName())\nfunction OnDriverInit()\n    doThing()\nend\n")
	out, _ := PatchEntry(src, Decision{ProjectToken: "drv_k"})
	if !bytes.Contains(out, []byte("require('vendor.anvil-sdk')")) {
		t.Errorf("a commented-out Anvil:Init must not suppress patching:\n%s", out)
	}
}

// A real Anvil:Init in code is still detected as already patched (no double patch).
func TestPatchEntryRealInitIsAlreadyPatched(t *testing.T) {
	src := []byte("function OnDriverInit()\n    Anvil:Init(\"k\", C4:GetDriverFileName())\nend\n")
	out, _ := PatchEntry(src, Decision{ProjectToken: "k"})
	if !bytes.Equal(out, src) {
		t.Errorf("an already-wired driver must be left untouched:\n%s", out)
	}
}

// For a build-targets driver, a commented-out require('config') must not stop
// the real one being prepended.
func TestEnsureConfigRequireIgnoresCommentedRequire(t *testing.T) {
	src := []byte("-- require('config')\nfunction OnDriverInit()\nend\n")
	out, _ := PatchEntry(src, Decision{ProjectToken: "k", BuildTargets: true, Targets: []string{"debug"}})
	if !bytes.HasPrefix(out, []byte("require('config')\n")) {
		t.Errorf("commented require('config') must not suppress the real one:\n%s", out)
	}
}

// PatchSquishy inserts the SDK (and, with build targets, config) Module lines
// before the Output directive — the squisher's preload only carries declared
// modules, so a require init writes without a matching Module line is a
// driver-load error on the controller.
func TestPatchSquishyAddsInitModules(t *testing.T) {
	src := []byte(`Main "driver.lua"

Module "lib.log" "lib/log.lua"

Output "../dist/driver-squished.lua"
`)
	got := string(PatchSquishy(src, true))
	for _, want := range []string{
		`Module "vendor.anvil-sdk" "vendor/anvil-sdk.lua"`,
		`Module "config" "config.lua"`,
		`Module "lib.log" "lib/log.lua"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("patched squishy missing %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "vendor.anvil-sdk") > strings.Index(got, "Output") {
		t.Errorf("modules must be declared before the Output directive:\n%s", got)
	}

	// Without build targets, no config module is declared.
	if noCfg := string(PatchSquishy(src, false)); strings.Contains(noCfg, `"config.lua"`) {
		t.Errorf("config module must not be added without build targets:\n%s", noCfg)
	}
}

// A squishy that already declares the files (under any module name) is left
// byte-for-byte alone.
func TestPatchSquishyIdempotent(t *testing.T) {
	src := []byte(`Main "driver.lua"

Module "vendor.anvil_sdk" "vendor/anvil-sdk.lua"
Module "config" "config.lua"

Output "../dist/driver-squished.lua"
`)
	if got := PatchSquishy(src, true); !bytes.Equal(got, src) {
		t.Errorf("already-declared squishy must be unchanged, got:\n%s", got)
	}
}

// A squishy without an Output directive gets the modules appended.
func TestPatchSquishyAppendsWithoutOutput(t *testing.T) {
	src := []byte("Main \"driver.lua\"\n")
	got := string(PatchSquishy(src, true))
	if !strings.Contains(got, `Module "vendor.anvil-sdk"`) || !strings.Contains(got, `Module "config"`) {
		t.Errorf("modules not appended:\n%s", got)
	}
}

// NewSquishy renders a valid squishy for a driver that had none — init forces
// squishLua on, so the build needs one.
func TestNewSquishy(t *testing.T) {
	got := string(NewSquishy("driver.lua", true))
	for _, want := range []string{`Main "driver.lua"`, `Module "vendor.anvil-sdk"`, `Module "config" "config.lua"`} {
		if !strings.Contains(got, want) {
			t.Errorf("new squishy missing %q:\n%s", want, got)
		}
	}
	if noCfg := string(NewSquishy("driver.lua", false)); strings.Contains(noCfg, "config.lua") {
		t.Errorf("config module must not appear without build targets:\n%s", noCfg)
	}
}
