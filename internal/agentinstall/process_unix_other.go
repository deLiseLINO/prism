//go:build unix && !darwin && !linux

package agentinstall

import (
	"errors"
	"syscall"
)

func processGroupGone(group int) (bool, error) {
	err := syscall.Kill(-group, 0)
	if errors.Is(err, syscall.ESRCH) {
		return true, nil
	}
	return false, err
}
