package agentinstall

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func processGroupGone(group int) (bool, error) {
	if err := syscall.Kill(-group, 0); errors.Is(err, syscall.ESRCH) {
		return true, nil
	} else if err != nil && !errors.Is(err, syscall.EPERM) {
		return false, err
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false, err
	}
	found := false
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		data, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return false, err
		}
		end := strings.LastIndexByte(string(data), ')')
		if end < 0 {
			return false, fmt.Errorf("invalid process stat for pid %s", entry.Name())
		}
		fields := strings.Fields(string(data)[end+1:])
		if len(fields) < 3 {
			return false, fmt.Errorf("invalid process stat for pid %s", entry.Name())
		}
		pgid, err := strconv.Atoi(fields[2])
		if err != nil {
			return false, err
		}
		if pgid == group {
			found = true
			if fields[0] != "Z" && fields[0] != "X" {
				return false, nil
			}
		}
	}
	if !found {
		if err := syscall.Kill(-group, 0); !errors.Is(err, syscall.ESRCH) {
			return false, err
		}
	}
	return true, nil
}
