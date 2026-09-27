//go:build !windows

package agentinstall

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

var lookupPasswdShell = passwdShell

func passwdShell(name string) string {
	if name == "" || strings.ContainsAny(name, " \t/\\:") {
		return ""
	}
	if runtime.GOOS == "darwin" {
		return dsclShell(name)
	}
	return getentShell(name)
}

func dsclShell(name string) string {
	out, err := exec.Command("/usr/bin/dscl", ".", "-read", "/Users/"+name, "UserShell").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		value, ok := strings.CutPrefix(line, "UserShell: ")
		if ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func getentShell(name string) string {
	out, err := exec.Command("/usr/bin/getent", "passwd", name).Output()
	if err != nil {
		return passwdFileShell(name)
	}
	return shellField(string(out))
}

func passwdFileShell(name string) string {
	raw, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return ""
	}
	prefix := name + ":"
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, prefix) {
			return shellField(line)
		}
	}
	return ""
}

func shellField(line string) string {
	fields := strings.Split(strings.TrimSpace(line), ":")
	if len(fields) < 7 {
		return ""
	}
	return fields[6]
}
