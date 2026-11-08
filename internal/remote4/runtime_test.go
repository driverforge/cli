package remote4

import "testing"

// A require resolves through the squishy's Module declarations, as the build
// bundles it — here a vendored library registered under its upstream name,
// which does not sit at that name's dotted path.
func TestRuntimeResolvesSquishyModules(t *testing.T) {
	rt := startDriver(t, map[string]string{
		"squishy":                 "Main \"driver.lua\"\nModule \"upstream.lib\" \"vendor/upstream/lib.lua\"\n",
		"vendor/upstream/lib.lua": "return { name = 'vendored' }\n",
		"driver.lua":              "print('lib=' .. require('upstream.lib').name)\n",
	}, nil)
	if m := awaitMessage(t, rt, "print"); m.Text != "lib=vendored" {
		t.Errorf("driver printed %q, want lib=vendored", m.Text)
	}
}
