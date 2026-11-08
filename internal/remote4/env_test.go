package remote4

import (
	"encoding/json"
	"testing"
)

// The stub captures Director's flat constant globals — C4SystemEvents and the
// like — and nothing that is code, the stub's own config, or driver state.
func TestStubCapturesDirectorConstants(t *testing.T) {
	runStubCheck(t, StubConfig{SessionID: "s", UserID: "u", Handlers: []string{"ExecuteCommand"}}, `
C4 = setmetatable({}, { __index = function() return function() end end })
C4SystemEvents = { OnPIP = 7, OnProjectChanged = 12 }
DEFAULT_PORT = 5020
Helpers = { f = function() end }
Properties = { Email = 'x' }
dofile(arg[1])
local c = Remote4._internals.constants
assert(c.C4SystemEvents and c.C4SystemEvents.OnPIP == 7, 'C4SystemEvents not captured')
assert(c.DEFAULT_PORT == 5020, 'scalar global not captured')
assert(c.Helpers == nil, 'table of functions captured')
assert(c.Properties == nil, 'driver state captured')
assert(c.REMOTE4 == nil, 'stub config captured')
assert(c.string == nil and c.Log == nil, 'library or stub table captured')
`)
}

// The runtime installs the constants announced with the load, before the
// driver runs, without replacing anything the local interpreter defines.
func TestRuntimeInstallsDirectorConstants(t *testing.T) {
	rt := startDriver(t, map[string]string{
		"driver.lua": "print('pip=' .. tostring(C4SystemEvents.OnPIP) .. ' string=' .. type(string))\n",
	}, json.RawMessage(`{"env":{"C4SystemEvents":{"OnPIP":7},"string":"not the library"}}`))
	if m := awaitMessage(t, rt, "print"); m.Text != "pip=7 string=table" {
		t.Errorf("driver printed %q, want pip=7 string=table", m.Text)
	}
}
