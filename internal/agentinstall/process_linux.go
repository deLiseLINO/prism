package agentinstall

import (
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func groupAlive(group int) (bool, error) {
	if err := unix.Kill(-group, 0); err != nil {
		if err == unix.ESRCH {
			return false, nil
		}
		return true, err
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return true, err
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		data, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return true, err
		}
		end := strings.LastIndexByte(string(data), ')')
		if end < 0 {
			continue
		}
		fields := strings.Fields(string(data)[end+1:])
		if len(fields) < 3 {
			continue
		}
		pgid, err := strconv.Atoi(fields[2])
		if err == nil && pgid == group && fields[0] != "Z" && fields[0] != "X" {
			return true, nil
		}
	}
	return false, nil
}
