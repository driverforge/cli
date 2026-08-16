package releaseverify

import (
	"strings"
	"testing"
)

func TestVerify(t *testing.T) {
	const good = "abc123"
	cases := []struct {
		name, got, expected string
		wantErr             string // substring; "" = no error
	}{
		{"match", good, good, ""},
		{"match case-insensitive", "ABC123", good, ""},
		{"match trims space", " " + good + " ", good, ""},
		{"mismatch refused", "deadbeef", good, "checksum mismatch"},
		// There is no way to skip either check. The
		// signature proves the manifest is ours; this proves the bytes match
		// what it declared. Neither is optional.
		{"missing refused", good, "", "no sha256 checksum"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Verify("Test Artifact", tc.got, tc.expected)
			if tc.wantErr == "" {
				if err != nil {
					t.Errorf("Verify = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Verify = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}
