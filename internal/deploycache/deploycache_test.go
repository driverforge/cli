package deploycache

import "testing"

func TestRoundTripAndKeying(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)           // darwin: ~/Library/Caches
	t.Setenv("XDG_CACHE_HOME", dir) // linux

	if got := Get("d1", "acme-dev.c4z"); got != "" {
		t.Errorf("fresh Get = %q, want empty", got)
	}
	if err := Set("d1", "acme-dev.c4z", "sha256:aaa"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := Get("d1", "acme-dev.c4z"); got != "sha256:aaa" {
		t.Errorf("round-trip = %q, want sha256:aaa", got)
	}
	// Keyed by both device and filename.
	if got := Get("d2", "acme-dev.c4z"); got != "" {
		t.Errorf("device isolation failed: %q", got)
	}
	if got := Get("d1", "acme-prod.c4z"); got != "" {
		t.Errorf("filename isolation failed: %q", got)
	}
	// Overwrite.
	_ = Set("d1", "acme-dev.c4z", "sha256:bbb")
	if got := Get("d1", "acme-dev.c4z"); got != "sha256:bbb" {
		t.Errorf("overwrite = %q, want sha256:bbb", got)
	}
}
