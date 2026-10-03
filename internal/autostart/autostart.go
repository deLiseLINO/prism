package autostart

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const healthPath = "/api/v1/health"

type probeResult int

const (
	daemonDown probeResult = iota
	daemonUp
	daemonForeign
)

type Launcher struct {
	BaseURL  string
	StateDir string
	Timeout  time.Duration
	Interval time.Duration
	Probe    func(ctx context.Context, baseURL string) probeResult
	Spawn    func(listen string, logFile *os.File) (int, error)
	Alive    func(pid int) bool
}

func New(baseURL, stateDir string) *Launcher {
	return &Launcher{
		BaseURL:  baseURL,
		StateDir: stateDir,
		Timeout:  15 * time.Second,
		Interval: 100 * time.Millisecond,
		Probe:    probe,
		Spawn:    spawnSelf,
		Alive:    processAlive,
	}
}

func (l *Launcher) Ensure(ctx context.Context) error {
	u, err := url.Parse(l.BaseURL)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("invalid PRISM_URL %q", l.BaseURL)
	}
	if !isLocalHost(u.Hostname()) {
		return nil
	}
	switch l.Probe(ctx, l.BaseURL) {
	case daemonUp:
		return nil
	case daemonForeign:
		return foreignError(u.Host)
	}
	if u.Port() == "" {
		return fmt.Errorf("PRISM_URL %q has no port; cannot start a daemon", l.BaseURL)
	}
	if err := os.MkdirAll(l.StateDir, 0o700); err != nil {
		return fmt.Errorf("state directory: %w", err)
	}
	unlock, err := lockFile(filepath.Join(l.StateDir, "prism.lock"))
	if err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	defer unlock()

	switch l.Probe(ctx, l.BaseURL) {
	case daemonUp:
		return nil
	case daemonForeign:
		return foreignError(u.Host)
	}
	logPath := filepath.Join(l.StateDir, "prism.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("daemon log: %w", err)
	}
	defer logFile.Close()
	pid, err := l.Spawn(u.Host, logFile)
	if err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}
	if err := os.WriteFile(pidPath(l.StateDir), []byte(strconv.Itoa(pid)+"\n"), 0o600); err != nil {
		return fmt.Errorf("pid file: %w", err)
	}
	fmt.Fprintf(os.Stderr, "prism: started daemon pid %d on %s, log %s\n", pid, u.Host, logPath)
	return l.waitHealthy(ctx, pid, logPath)
}

func (l *Launcher) waitHealthy(ctx context.Context, pid int, logPath string) error {
	ctx, cancel := context.WithTimeout(ctx, l.Timeout)
	defer cancel()
	ticker := time.NewTicker(l.Interval)
	defer ticker.Stop()
	for {
		switch l.Probe(ctx, l.BaseURL) {
		case daemonUp:
			return nil
		case daemonForeign:
			return foreignError(l.BaseURL)
		}
		if !l.Alive(pid) {
			return fmt.Errorf("daemon exited during startup; see %s", logPath)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("daemon did not become healthy within %s; see %s", l.Timeout, logPath)
		case <-ticker.C:
		}
	}
}

func foreignError(addr string) error {
	return fmt.Errorf("%s is in use by something that is not prism; free the port or set PRISM_URL", addr)
}

func pidPath(stateDir string) string { return filepath.Join(stateDir, "prism.pid") }

func isLocalHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func probe(ctx context.Context, baseURL string) probeResult {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+healthPath, nil)
	if err != nil {
		return daemonForeign
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		var opErr *net.OpError
		if errors.As(err, &opErr) && opErr.Op == "dial" {
			return daemonDown
		}
		return daemonForeign
	}
	defer resp.Body.Close()
	var body struct {
		Status string `json:"status"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&body) != nil || body.Status != "ok" {
		return daemonForeign
	}
	return daemonUp
}

func spawnSelf(listen string, logFile *os.File) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(exe, "daemon", "--listen", listen)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	return pid, cmd.Process.Release()
}

func Stop(ctx context.Context, baseURL, stateDir string, w io.Writer) error {
	data, err := os.ReadFile(pidPath(stateDir))
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(w, "no daemon started by prism (no pid file); nothing to stop")
		return nil
	}
	if err != nil {
		return fmt.Errorf("pid file: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return fmt.Errorf("pid file %s is corrupt", pidPath(stateDir))
	}
	if !processAlive(pid) || probe(ctx, baseURL) != daemonUp {
		os.Remove(pidPath(stateDir))
		fmt.Fprintf(w, "daemon pid %d is not running; removed stale pid file\n", pid)
		return nil
	}
	if err := terminate(pid); err != nil {
		return fmt.Errorf("stop daemon pid %d: %w", pid, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			return fmt.Errorf("daemon pid %d did not exit within 10s", pid)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	os.Remove(pidPath(stateDir))
	fmt.Fprintf(w, "stopped daemon pid %d\n", pid)
	return nil
}
