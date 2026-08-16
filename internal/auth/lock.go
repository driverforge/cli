package auth

import (
	"os"
	"path/filepath"
)

// lockCredentials takes a cross-process exclusive lock that serialises token
// refresh, so two concurrent `driverforge` invocations (two terminals, a CI matrix,
// a shell-prompt integration) don't both consume — and rotate — the same
// refresh token. With Hydra's rotation, the loser of that race gets
// invalid_grant and reuse detection can revoke the whole grant chain, killing
// both sessions.
//
// It is best-effort: if the lock can't be created or taken (an odd filesystem,
// a permission quirk), it returns a no-op release and no error, degrading to
// the previous unsynchronised behaviour rather than blocking sign-in. The
// caller re-loads credentials under the lock so it picks up a token another
// process just refreshed.
func lockCredentials() (release func(), err error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return func() {}, nil
	}
	dir = filepath.Join(dir, "driverforge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return func() {}, nil
	}
	f, err := os.OpenFile(filepath.Join(dir, "refresh.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return func() {}, nil
	}
	unlock, err := flock(f)
	if err != nil {
		_ = f.Close()
		return func() {}, nil
	}
	return func() {
		unlock()
		_ = f.Close()
	}, nil
}
