package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/deLiseLINO/prism/internal/buildinfo"
)

const (
	DefaultListen = "127.0.0.1:10200"
	pollInterval  = 50 * time.Millisecond
	pollTries     = 100
)

type Daemon struct {
	Listen string
	Args   []string
}

type Service struct {
	StateDir  string
	Version   string
	Probe     func(ctx context.Context, reg Registration) bool
	Spawn     func(args []string) error
	Alive     func(pid int) bool
	Terminate func(pid int) error
	Kill      func(pid int) error
	PortBusy  func(addr string) bool
	Interval  time.Duration
	Tries     int
}

func New() *Service {
	dir := StateDir()
	return &Service{
		StateDir:  dir,
		Version:   buildinfo.Version,
		Probe:     Probe,
		Spawn:     func(args []string) error { return spawn(dir, args) },
		Alive:     processAlive,
		Terminate: terminate,
		Kill:      kill,
		PortBusy:  portBusy,
		Interval:  pollInterval,
		Tries:     pollTries,
	}
}

func (s *Service) healthy(ctx context.Context) (Registration, bool) {
	reg, err := ReadRegistration(s.StateDir)
	if err != nil || !s.Probe(ctx, reg) {
		return Registration{}, false
	}
	return reg, true
}

func (s *Service) Status(ctx context.Context) (string, bool) {
	reg, ok := s.healthy(ctx)
	if !ok || reg.Version != s.Version {
		return "", false
	}
	return reg.URL, true
}

func (s *Service) Start(ctx context.Context, d Daemon) (string, error) {
	if reg, ok := s.healthy(ctx); ok {
		if reg.Version == s.Version {
			return reg.URL, nil
		}
		if err := s.Stop(ctx); err != nil {
			return "", err
		}
	}
	if err := s.Spawn(append([]string{"daemon", "--register"}, d.Args...)); err != nil {
		return "", fmt.Errorf("start daemon: %w", err)
	}
	for i := 0; i < s.Tries; i++ {
		if err := sleep(ctx, s.Interval); err != nil {
			return "", err
		}
		if url, ok := s.Status(ctx); ok {
			return url, nil
		}
	}
	if s.PortBusy(d.Listen) {
		return "", fmt.Errorf("address %s is held by a non-prism process; free the port or choose another --listen", d.Listen)
	}
	return "", fmt.Errorf("daemon did not become healthy; see %s", filepath.Join(s.StateDir, "prism.log"))
}

func (s *Service) Stop(ctx context.Context) error {
	reg, err := ReadRegistration(s.StateDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return removeRegistration(s.StateDir)
	}
	if !s.Probe(ctx, reg) {
		return removeRegistration(s.StateDir)
	}
	if err := s.Terminate(reg.PID); err != nil && s.Alive(reg.PID) {
		return fmt.Errorf("stop daemon pid %d: %w", reg.PID, err)
	}
	if err := s.waitExit(ctx, reg.PID); err != nil {
		return err
	}
	if s.Alive(reg.PID) {
		if cur, ok := s.healthy(ctx); ok && cur.ID == reg.ID {
			if err := s.Kill(reg.PID); err != nil && s.Alive(reg.PID) {
				return fmt.Errorf("kill daemon pid %d: %w", reg.PID, err)
			}
			if err := s.waitExit(ctx, reg.PID); err != nil {
				return err
			}
		}
	}
	return RemoveOwnRegistration(s.StateDir, reg.ID)
}

func (s *Service) waitExit(ctx context.Context, pid int) error {
	for i := 0; i < s.Tries && s.Alive(pid); i++ {
		if err := sleep(ctx, s.Interval); err != nil {
			return err
		}
	}
	return nil
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

func spawn(stateDir string, args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(stateDir, "prism.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	cmd := exec.Command(exe, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func portBusy(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
