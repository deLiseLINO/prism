//go:build unix

package agentinstall

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

func killProcessGroup(pid int) error {
	deadline := time.Now().Add(time.Second)
	for {
		err := syscall.Kill(-pid, syscall.SIGKILL)
		if err == nil || errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if !errors.Is(err, syscall.EPERM) || !time.Now().Before(deadline) {
			return fmt.Errorf("stop maintenance process group: %w", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func runCommand(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killProcessGroup(cmd.Process.Pid) }
	if err := cmd.Start(); err != nil {
		return err
	}
	processGroupID := cmd.Process.Pid
	err := cmd.Wait()
	cleanupErr := killProcessGroup(processGroupID)
	if cleanupErr != nil {
		return errors.Join(err, cleanupErr)
	}
	return err
}
