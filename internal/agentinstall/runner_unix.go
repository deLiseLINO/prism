//go:build darwin || linux

package agentinstall

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

type streamWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (w streamWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}

func runOwned(ctx context.Context, path string, env, args []string, stdout, stderr io.Writer) error {
	cmd := exec.Command(path, args...)
	cmd.Env = env
	cmd.SysProcAttr = &unix.SysProcAttr{Setpgid: true}
	outR, outW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer outR.Close()
	defer outW.Close()
	errR, errW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer errR.Close()
	defer errW.Close()
	cmd.Stdout, cmd.Stderr = outW, errW
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	outW.Close()
	errW.Close()
	drained := make(chan error, 2)
	var streams sync.Mutex
	for _, stream := range []struct {
		r *os.File
		w io.Writer
	}{{outR, stdout}, {errR, stderr}} {
		go func(r *os.File, w io.Writer) {
			if w == nil {
				w = io.Discard
			}
			_, err := io.Copy(streamWriter{mu: &streams, w: w}, r)
			r.Close()
			drained <- err
		}(stream.r, stream.w)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	var waitErr error
	var reaped bool
	select {
	case waitErr = <-waited:
		reaped = true
	case <-ctx.Done():
		waitErr = ctx.Err()
	}
	group := cmd.Process.Pid
	unix.Kill(-group, unix.SIGTERM)
	grace := time.Now().Add(200 * time.Millisecond)
	for {
		if !reaped {
			select {
			case err := <-waited:
				reaped = true
				if waitErr == nil {
					waitErr = err
				}
			default:
			}
		}
		alive, err := groupAlive(group)
		if err == nil && !alive {
			break
		}
		if time.Now().After(grace) {
			unix.Kill(-group, unix.SIGKILL)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !reaped {
		waitErr = errors.Join(waitErr, <-waited)
	}
	outR.SetReadDeadline(time.Now().Add(time.Second))
	errR.SetReadDeadline(time.Now().Add(time.Second))
	return errors.Join(waitErr, <-drained, <-drained)
}
