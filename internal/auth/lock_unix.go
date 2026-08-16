//go:build unix

package auth

import (
	"os"

	"golang.org/x/sys/unix"
)

// flock takes an exclusive advisory lock on an open file (blocking until it is
// available), returning a function that releases it.
func flock(f *os.File) (func(), error) {
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		return nil, err
	}
	return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }, nil
}
