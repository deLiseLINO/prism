//go:build unix

package agentinstall

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

const cleanupTimeout = time.Second

func killProcessGroup(pid int) error {
	signalErr := syscall.Kill(-pid, syscall.SIGKILL)
	if errors.Is(signalErr, syscall.ESRCH) {
		return nil
	}
	deadline := time.Now().Add(cleanupTimeout)
	for {
		gone, err := processGroupGone(pid)
		if gone && err == nil {
			return nil
		}
		if !time.Now().Before(deadline) {
			if signalErr == nil && err == nil {
				err = errors.New("process group still has live members after SIGKILL")
			}
			return &CleanupError{ProcessGroup: pid, Err: errors.Join(signalErr, err)}
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func runCommand(ctx context.Context, cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Wait owns reaping; cancellation only signals so denied signals cannot
	// block the bounded response behind an unbounded Process.Wait.
	canceled := make(chan error, 1)
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		canceled <- err
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	group := cmd.Process.Pid
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	var err error
	select {
	case err = <-waited:
	case <-ctx.Done():
		timer := time.NewTimer(cmd.WaitDelay + cleanupTimeout)
		defer timer.Stop()
		select {
		case err = <-waited:
		case <-timer.C:
			var signalErr error
			select {
			case signalErr = <-canceled:
			default:
			}
			return &CleanupError{ProcessGroup: group, Err: errors.Join(signalErr, fmt.Errorf("command exit not confirmed after cancellation: %w", ctx.Err()))}
		}
	}
	return errors.Join(err, killProcessGroup(group))
}
