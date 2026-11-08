package remote4

import (
	"encoding/json"
	"strings"
	"testing"
)

// The stub encodes a map keyed by numbers — GetDevices' { [deviceid] = … } —
// so the key survives JSON's string-only object keys, and decodes it back.
// Arrays and string-keyed maps keep their plain wire shape.
func TestStubCodecKeepsNonStringKeys(t *testing.T) {
	runStubCheck(t, StubConfig{SessionID: "s", UserID: "u"}, `
C4 = setmetatable({}, { __index = function() return function() end end })
dofile(arg[1])
local I, json = Remote4._internals, require('json')
local wire = json.encode(I.encodeValue({ [7] = { driverFileName = 'x.c4z' }, [true] = 'yes', name = 'n' }))
local back = I.decodeValue(json.decode(wire))
assert(back[7] and back[7].driverFileName == 'x.c4z', 'numeric key lost: ' .. wire)
assert(back[true] == 'yes', 'boolean key lost: ' .. wire)
assert(back.name == 'n', 'string key lost: ' .. wire)
assert(json.encode(I.encodeValue({ 1, 2, 3 })) == '[1,2,3]', 'array changed shape')
assert(json.encode(I.encodeValue({ a = 1 })) == '{"a":1}', 'string map changed shape')
`)
}

// The runtime decodes a numeric-keyed result so the driver's own lookup by
// number finds it, and encodes a numeric-keyed argument the same way.
func TestRuntimeCodecKeepsNonStringKeys(t *testing.T) {
	rt := startDriver(t, map[string]string{"driver.lua": `
local devices = C4:GetDevices({}, 7)
print('name=' .. tostring(devices[7] and devices[7].driverFileName))
C4:Record({ [5] = 'five' })
`}, nil)
	call := awaitMessage(t, rt, "c4_call")
	ok, argc := true, 1
	if err := rt.Send(Message{Type: "c4_response", ID: call.ID, OK: &ok, Argc: &argc,
		Results: json.RawMessage(`[{"$kv":[[7,{"driverFileName":"x.c4z"}]]}]`)}); err != nil {
		t.Fatal(err)
	}
	if p := awaitMessage(t, rt, "print"); p.Text != "name=x.c4z" {
		t.Errorf("driver saw %q, want name=x.c4z", p.Text)
	}
	if rec := awaitMessage(t, rt, "c4_call"); !strings.Contains(string(rec.Args), `"$kv":[[5,"five"]]`) {
		t.Errorf("numeric-keyed argument sent as %s", rec.Args)
	}
}
