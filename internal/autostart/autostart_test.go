package autostart

import (
	"bytes"
	"context"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fakeLauncher(dir string, spawned *atomic.Int32, up *atomic.Bool) *Launcher {
	return &Launcher{
		BaseURL:  "http://127.0.0.1:10200",
		StateDir: dir,
		Timeout:  2 * time.Second,
		Interval: time.Millisecond,
		Probe: func(context.Context, string) probeResult {
			if up.Load() {
				return daemonUp
			}
			return daemonDown
		},
		Spawn: func(string, *os.File) (int, error) {
			spawned.Add(1)
			time.Sleep(20 * time.Millisecond)
			up.Store(true)
			return os.Getpid(), nil
		},
		Alive: func(int) bool { return true },
	}
}

func TestEnsureConcurrentRunsSpawnOnce(t *testing.T) {
	dir := t.TempDir()
	var spawned atomic.Int32
	var up atomic.Bool
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fakeLauncher(dir, &spawned, &up).Ensure(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := spawned.Load(); n != 1 {
		t.Fatalf("spawned %d daemons, want 1", n)
	}
}

func TestEnsureSkipsRemoteHosts(t *testing.T) {
	var spawned atomic.Int32
	var up atomic.Bool
	l := fakeLauncher(t.TempDir(), &spawned, &up)
	l.BaseURL = "http://example.com:10200"
	l.Probe = func(context.Context, string) probeResult { t.Fatal("probed a remote host"); return daemonDown }
	if err := l.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if spawned.Load() != 0 {
		t.Fatal("spawned for a remote host")
	}
}

func TestEnsureRejectsForeignListener(t *testing.T) {
	var spawned atomic.Int32
	var up atomic.Bool
	l := fakeLauncher(t.TempDir(), &spawned, &up)
	l.Probe = func(context.Context, string) probeResult { return daemonForeign }
	err := l.Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not prism") {
		t.Fatalf("err = %v", err)
	}
	if spawned.Load() != 0 {
		t.Fatal("spawned over a foreign listener")
	}
}

func TestStopWithoutPidFileDoesNothing(t *testing.T) {
	var out bytes.Buffer
	if err := Stop(context.Background(), "http://127.0.0.1:1", t.TempDir(), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "nothing to stop") {
		t.Fatalf("out = %q", out.String())
	}
}
