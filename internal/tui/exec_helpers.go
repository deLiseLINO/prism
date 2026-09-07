package tui

import (
	"os/exec"
	"strings"
)

func execCommand(name string, args ...string) error {
	return exec.Command(name, args...).Run()
}

func lookPath(name string) (string, error) {
	return exec.LookPath(name)
}

func runWithStdin(name string, args []string, input string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(input)
	return cmd.Run()
}
