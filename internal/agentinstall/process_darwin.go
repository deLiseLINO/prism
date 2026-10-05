package agentinstall

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

func processGroupGone(group int) (bool, error) {
	if err := syscall.Kill(-group, 0); errors.Is(err, syscall.ESRCH) {
		return true, nil
	} else if err != nil && !errors.Is(err, syscall.EPERM) {
		return false, err
	}
	members, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", group)
	if err != nil {
		return false, err
	}
	for _, member := range members {
		// SZOMB members cannot execute or mutate, even when signal returns EPERM.
		if member.Proc.P_stat != 5 {
			return false, nil
		}
	}
	if len(members) == 0 {
		if err := syscall.Kill(-group, 0); !errors.Is(err, syscall.ESRCH) {
			return false, err
		}
	}
	return true, nil
}
