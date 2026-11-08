package remote4

import (
	"encoding/json"
	"strings"
	"testing"
)

// The driver's logger, handed to Driverforge:Init, is wrapped as the SDK would
// wrap it: each line still reaches the driver's own method and is also emitted
// for the stub. The SDK's own `Driverforge = Driverforge or {...}` keeps the
// runtime's stand-in, so the driver's SDK never wraps the logger itself.
func TestRuntimeForwardsDriverLogs(t *testing.T) {
	rt := startDriver(t, map[string]string{
		"sdk.lua": "Driverforge = Driverforge or { Init = function() error('the real SDK wrapped the logger') end }\n",
		"driver.lua": `
local Log = { Info = function(self, msg) print('local ' .. tostring(msg)) end, warn = function(msg) end }
require('sdk')
Driverforge:Init('token', { logger = Log })
Log:Info('colon', { episodes = 3 })
Log.warn('dot')
`,
	}, nil)
	if m := awaitMessage(t, rt, "forward_log"); m.Level != "INFO" || string(m.Args) != `["colon",{"episodes":3}]` {
		t.Errorf("colon call forwarded as %s %s", m.Level, m.Args)
	}
	if m := awaitMessage(t, rt, "print"); m.Text != "local colon" {
		t.Errorf("the driver's own method printed %q, want local colon", m.Text)
	}
	if m := awaitMessage(t, rt, "forward_log"); m.Level != "WARN" || string(m.Args) != `["dot"]` {
		t.Errorf("dot call forwarded as %s %s", m.Level, m.Args)
	}
}

// Errors leave the runtime with the SDK's structured trace: a handler's own
// error is captured at the runtime boundary; an error the SDK already captured
// and rethrew keeps the SDK's original-site trace; and a driver's explicit
// Anvil:CaptureError is forwarded too.
func TestRuntimeForwardsDriverErrors(t *testing.T) {
	rt := startDriver(t, map[string]string{
		// Stands in for the SDK's bundled diagnostics module and Anvil table.
		"sdk.lua": `
package.preload['diagnostics'] = function()
    return { capture = function(o) return { schemaVersion = 1, captureKind = o.captureKind, site = 'runtime' } end }
end
local Diagnostics = require('diagnostics')
Anvil = Anvil or { CaptureError = function() end }
function Wrapped(fn)
    return function(...)
        local ok, err = pcall(fn, ...)
        if not ok then
            local trace = Diagnostics.capture({ captureKind = 'exception' })
            trace.site = 'original'
            error(err, 0)
        end
    end
end
`,
		"driver.lua": `
require('sdk')
function ExecuteCommand(cmd) error('boom in ' .. cmd, 0) end
OnPropertyChanged = Wrapped(function(name) error('wrapped ' .. name, 0) end)
function ReceivedFromProxy() Anvil:CaptureError('manual', nil, { eventName = 'Poll' }) end
`,
	}, nil)
	argc := 1
	call := func(id int, name, args string) Message {
		t.Helper()
		if err := rt.Send(Message{Type: "callback", ID: id, Name: name, Args: json.RawMessage(args), Argc: &argc}); err != nil {
			t.Fatal(err)
		}
		return awaitMessage(t, rt, "forward_error")
	}

	if m := call(1, "ExecuteCommand", `["PLAY"]`); m.Error != "boom in PLAY" || m.Name != "ExecuteCommand" || !strings.Contains(string(m.Trace), `"site":"runtime"`) || !strings.Contains(string(m.Trace), `"captureKind":"exception"`) {
		t.Errorf("handler error forwarded as %q %q %s", m.Error, m.Name, m.Trace)
	}
	if m := call(2, "OnPropertyChanged", `["Email"]`); m.Error != "wrapped Email" || !strings.Contains(string(m.Trace), `"site":"original"`) {
		t.Errorf("rethrown error should keep the SDK's original trace: %q %s", m.Error, m.Trace)
	}
	if m := call(3, "ReceivedFromProxy", `[5001]`); m.Error != "manual" || m.Name != "Poll" || !strings.Contains(string(m.Trace), `"captureKind":"callsite"`) {
		t.Errorf("explicit capture forwarded as %q %q %s", m.Error, m.Name, m.Trace)
	}
}

// The bridge relays the driver's forwarded logs and errors to the stub on the
// control topic, arguments and trace intact.
func TestBridgeRelaysForwardedTelemetry(t *testing.T) {
	cases := map[string]struct {
		line string
		want []string
	}{
		"log": {
			`{"v":1,"type":"forward_log","level":"INFO","args":["hello"],"argc":1}`,
			[]string{`"type":"log"`, `"level":"INFO"`, `"args":["hello"]`},
		},
		"error": {
			`{"v":1,"type":"forward_error","error":"boom","name":"ExecuteCommand","trace":{"schemaVersion":1,"frames":[]}}`,
			[]string{`"type":"error"`, `"error":"boom"`, `"name":"ExecuteCommand"`, `"trace":{"schemaVersion":1,"frames":[]}`},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := controlMessageFrom(t, c.line)
			for _, want := range c.want {
				if !strings.Contains(got, want) {
					t.Errorf("control message %s lacks %s", got, want)
				}
			}
		})
	}
}

// The stub sends a forwarded line through its SDK's Anvil:ForwardLog, and
// ignores a level the SDK doesn't define.
func TestStubForwardsDriverLogsThroughSDK(t *testing.T) {
	runStubCheck(t, StubConfig{SessionID: "s", UserID: "u"}, `
C4 = setmetatable({}, { __index = function() return function() end end })
local got = {}
Anvil = { ForwardLog = function(_, level, ...) got[#got + 1] = { level, ... } end }
dofile(arg[1])
local handle = Remote4._internals.handleMessage
handle('control', { type = 'log', level = 'INFO', args = { 'hello', { episodes = 3 }, n = 2 }, argc = 2 })
handle('control', { type = 'log', level = 'LOUD', args = { 'nope', n = 1 }, argc = 1 })
assert(#got == 1, 'expected one forwarded line, got ' .. #got)
assert(got[1][1] == 'INFO' and got[1][2] == 'hello' and got[1][3].episodes == 3, 'line forwarded wrongly')
`)
}

// The stub hands a forwarded error to its SDK with the laptop's trace — never
// letting the SDK capture the stub's own frames instead.
func TestStubForwardsDriverErrorsThroughSDK(t *testing.T) {
	runStubCheck(t, StubConfig{SessionID: "s", UserID: "u"}, `
C4 = setmetatable({}, { __index = function() return function() end end })
local got = {}
Anvil = { CaptureError = function(_, message, trace, context) got[#got + 1] = { message, trace, context } end }
dofile(arg[1])
local json, handle = require('json'), Remote4._internals.handleMessage
handle('control', json.decode('{"type":"error","error":"boom","name":"ExecuteCommand","trace":{"schemaVersion":1,"frames":[{"line":3}]}}'))
handle('control', { type = 'error', error = 'no trace' })
assert(#got == 2, 'expected two captures, got ' .. #got)
assert(got[1][1] == 'boom' and got[1][3].eventName == 'ExecuteCommand', 'error forwarded wrongly')
assert(got[1][2].frames[1].line == 3 and got[1][2].frames.n == nil, 'trace not passed through plainly')
assert(got[2][2].status == 'failed', 'a missing trace must not be captured on the stub')
`)
}
