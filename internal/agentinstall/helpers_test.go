package agentinstall

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/integrations"
)

// fakeStat answers from a path -> mode map, emulating os.Stat: mode 0 marks
// "exists but not executable", absence returns not-exist.
type fakeStat struct {
	entries map[string]os.FileMode
}

func (f fakeStat) stat(path string) (os.FileInfo, error) {
	if mode, ok := f.entries[path]; ok {
		return fakeFileInfo{name: path, mode: mode}, nil
	}
	return nil, os.ErrNotExist
}

type fakeFileInfo struct {
	name string
	mode os.FileMode
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() os.FileMode  { return f.mode }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeFileInfo) Sys() any           { return nil }

// statWith builds a fakeStat where every named path is an executable file.
func statWith(paths ...string) fakeStat {
	entries := make(map[string]os.FileMode, len(paths))
	for _, p := range paths {
		entries[p] = 0755
	}
	return fakeStat{entries: entries}
}

// pathEnv builds an Env whose PATH lists dirs in order.
func pathEnv(dirs ...string) integrations.Env {
	return integrations.Env{"PATH": strings.Join(dirs, ":")}
}

func testNow() time.Time { return time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC) }

// waitTerminal polls the manager until the job for key reaches a terminal
// state or the timeout expires.
func waitTerminal(t *testing.T, m *Manager, key string, timeout time.Duration) Job {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		job, ok := m.jobs[key]
		if ok {
			snapshot := *job
			m.mu.Unlock()
			switch snapshot.State {
			case StateSucceeded, StateFailed, StateUnsupported, StateInterrupted:
				return snapshot
			}
		} else {
			m.mu.Unlock()
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("job %s never reached a terminal state", key)
	return Job{}
}
