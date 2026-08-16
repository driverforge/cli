package ui

import "testing"

func TestTabName(t *testing.T) {
	cases := map[string]string{
		"src/driver.lua":           "src/driver.lua",
		"src/vendor/anvil-sdk.lua": "vendor/anvil-sdk.lua",
		"src/manifest.c4zproj":     "src/manifest.c4zproj",
		".driverforge/config.json": ".driverforge/config.json",
		"driver.lua":               "driver.lua",
	}
	for in, want := range cases {
		if got := tabName(in); got != want {
			t.Errorf("tabName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReviewModelMoveWraps(t *testing.T) {
	m := newReviewModel([]ReviewFile{
		{Path: "a.lua", New: []byte("x")},
		{Path: "b.lua", New: []byte("y")},
		{Path: "c.lua", New: []byte("z")},
	})
	if len(m.bodies) != 3 || m.cur != 0 {
		t.Fatalf("unexpected initial model: cur=%d bodies=%d", m.cur, len(m.bodies))
	}
	m.move(1)
	m.move(1)
	if m.cur != 2 {
		t.Errorf("cur = %d, want 2", m.cur)
	}
	m.move(1) // wrap forward
	if m.cur != 0 {
		t.Errorf("cur = %d, want 0 after wrap", m.cur)
	}
	m.move(-1) // wrap backward
	if m.cur != 2 {
		t.Errorf("cur = %d, want 2 after backward wrap", m.cur)
	}
}
