package c4z

import "testing"

func TestArtifactName(t *testing.T) {
	if got := artifactName("acme", "", false); got != "acme" {
		t.Errorf("default artifact = %q, want acme (naked)", got)
	}
	if got := artifactName("acme", "release", false); got != "acme-release" {
		t.Errorf("release artifact = %q, want acme-release", got)
	}
	if got := artifactName("acme", "release", true); got != "acme" {
		t.Errorf("no-suffix artifact = %q, want acme (naked)", got)
	}
}

func TestDisplayNameSuffix(t *testing.T) {
	if got := displayNameSuffix("", false); got != "" {
		t.Errorf("default suffix = %q, want empty", got)
	}
	if got := displayNameSuffix("release", false); got != " (release)" {
		t.Errorf("release suffix = %q", got)
	}
	if got := displayNameSuffix("release", true); got != "" {
		t.Errorf("no-suffix display suffix = %q, want empty", got)
	}
}
