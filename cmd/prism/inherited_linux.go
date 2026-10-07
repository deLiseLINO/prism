package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

func closeInheritedDescriptors() error {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return fmt.Errorf("inspect inherited descriptors: %w", err)
	}
	for _, entry := range entries {
		fd, err := strconv.Atoi(entry.Name())
		if err != nil || fd < 3 {
			continue
		}
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if errors.Is(err, unix.EBADF) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect descriptor %d: %w", fd, err)
		}
		if flags&unix.FD_CLOEXEC != 0 {
			continue
		}
		if err := unix.Close(fd); err != nil && !errors.Is(err, unix.EBADF) {
			return fmt.Errorf("close inherited descriptor %d: %w", fd, err)
		}
	}
	return nil
}
