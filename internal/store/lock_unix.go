//go:build !windows

package store

import (
	"errors"
	"os"
	"syscall"
)

func tryLockNonBlocking(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func unlock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

func wouldBlock(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK)
}

// sameInode reports whether the open file f still refers to the same inode as
// path (the lock file may have been replaced by another process in between).
func sameInode(f *os.File, path string) bool {
	var fStat, pStat syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &fStat); err != nil {
		return false
	}
	if err := syscall.Stat(path, &pStat); err != nil {
		return false
	}
	return fStat.Ino == pStat.Ino
}
