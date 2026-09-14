//go:build windows

package store

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func tryLockNonBlocking(f *os.File) error {
	// LockFileEx over the first byte of the file; the OVERLAPPED argument is
	// required by the signature but unused for synchronous handles.
	var overlapped windows.Overlapped
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1, 0,
		&overlapped,
	)
}

func unlock(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(
		windows.Handle(f.Fd()),
		0,
		1, 0,
		&overlapped,
	)
}

func wouldBlock(err error) bool {
	// LockFileEx reports contention as ERROR_LOCK_VIOLATION (33).
	return errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}

// sameInode: Windows does not expose stable file indexes through os.File, and
// the lock file is only removed by the lock holder itself, so the identity
// check from the unix path degrades to an existence check.
func sameInode(f *os.File, path string) bool {
	_ = f
	if _, err := os.Stat(path); err != nil {
		return false
	}
	return true
}
