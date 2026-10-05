package agentinstall

import "golang.org/x/sys/unix"

func groupAlive(group int) (bool, error) {
	if err := unix.Kill(-group, 0); err != nil {
		if err == unix.ESRCH {
			return false, nil
		}
		return true, err
	}
	members, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", group)
	if err != nil {
		return true, err
	}
	for _, member := range members {
		if member.Proc.P_stat != 5 {
			return true, nil
		}
	}
	return false, nil
}
