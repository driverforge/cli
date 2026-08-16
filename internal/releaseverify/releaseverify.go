// Package releaseverify enforces the integrity policy for downloaded release
// artifacts (the Driverforge SDK bundle and the on-controller agent). Both are code
// that runs on the user's driver or controller, so a compromised release store
// (or a mis-issued cert for it) must not be able to ship unverified bytes.
//
// The policy has two links, and both are required:
//
//   - The manifest carries a detached signature made by a key held only in Key
//     Vault, verified against the public half compiled into this binary
//     (VerifyManifest). This is what a compromised origin cannot forge.
//   - The downloaded artifact matches the sha256 the manifest declares
//     (Verify). This is what ties the signed manifest to the bytes on disk.
//
// Neither alone is sufficient. A signature without the checksum proves the
// manifest is ours but says nothing about what was downloaded; a checksum
// without the signature compares two values an attacker controlling the origin
// supplies together. There is deliberately no way to skip either: this installs
// code onto customer hardware — and, via internal/selfupdate, replaces the CLI
// itself — so a bypass shipped here would compromise the very path a fix would
// have to travel.
package releaseverify

import (
	"fmt"
	"strings"
)

// Verify checks a downloaded artifact's computed hex checksum against the
// expected value from its release manifest. artifact names it for messages
// (e.g. "Driverforge SDK"). A missing or mismatched checksum is always refused.
func Verify(artifact, gotHex, expectedHex string) error {
	if expectedHex == "" {
		return fmt.Errorf("%s: release manifest has no sha256 checksum — refusing to install unverified code", artifact)
	}
	if !strings.EqualFold(strings.TrimSpace(gotHex), strings.TrimSpace(expectedHex)) {
		return fmt.Errorf("%s: checksum mismatch (manifest %s, downloaded %s) — refusing to install", artifact, expectedHex, gotHex)
	}
	return nil
}
