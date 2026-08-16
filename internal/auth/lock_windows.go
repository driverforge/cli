//go:build windows

package auth

import (
	"os"

	"golang.org/x/sys/windows"
)

// flock takes an exclusive lock on an open file (blocking until it is
// available), returning a function that releases it.
func flock(f *os.File) (func(), error) {
	h := windows.Handle(f.Fd())
	ol := new(windows.Overlapped)
	if err := windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, ol); err != nil {
		return nil, err
	}
	return func() { _ = windows.UnlockFileEx(h, 0, 1, 0, ol) }, nil
}
