package remote4

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestKnownCallbacksIsVendored(t *testing.T) {
	k := KnownCallbacks()
	for _, name := range []string{"OnDriverInit", "OnDriverLateInit", "ReceivedFromProxy", "ExecuteCommand", "OnTimerExpired", "UIRequest"} {
		if !k[name] {
			t.Errorf("%s missing from callbacks.txt — re-run scripts/vendor-remote4.sh", name)
		}
	}
	if len(k) < 100 {
		t.Errorf("only %d known callbacks; the vendored list looks truncated", len(k))
	}
}

func TestScanHandlersFindsTopLevelCallbacksOnly(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("driver.lua", `
function OnDriverInit(strDIR) end
  function ReceivedFromProxy(id, cmd, params)
end
OnPropertyChanged = function(name) end
local function OnTimerExpired() end        -- local: not a global handler
function Log:Info() end                    -- a method
function ToFunctionName(s) end             -- a helper, not a callback
`)
	write("lib/handlers.lua", "function ExecuteCommand(cmd, params) end\n")
	write("vendor/driverforge-sdk.lua", "function UIRequest() end\nfunction OnDriverLateInit() end\n")
	write("vendor/framework/global/handlers.lua", "function OnSystemEvent(event) end\n")
	write(".hidden/x.lua", "function OnBindingChanged() end\n")
	got, err := ScanHandlers(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ExecuteCommand", "OnDriverInit", "OnPropertyChanged", "OnSystemEvent", "ReceivedFromProxy"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ScanHandlers = %v, want %v", got, want)
	}
}

func TestMissing(t *testing.T) {
	got := Missing([]string{"OnDriverInit"}, []string{"OnDriverInit", "ReceivedFromProxy", "ToFunctionName"})
	if !reflect.DeepEqual(got, []string{"ReceivedFromProxy"}) {
		t.Errorf("Missing = %v", got)
	}
}
