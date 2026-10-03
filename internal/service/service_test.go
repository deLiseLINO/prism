package service

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

type fake struct {
	t        *testing.T
	svc      *Service
	events   []string
	daemonUp bool
	liveID   string
	alive    map[int]bool
}

func newFake(t *testing.T) *fake {
	f := &fake{t: t, alive: map[int]bool{}}
	f.svc = &Service{
		StateDir: t.TempDir(),
		Version:  "2.0.0",
		Interval: time.Millisecond,
		Tries:    20,
		Probe: func(_ context.Context, reg Registration) bool {
			return f.daemonUp && reg.ID == f.liveID
		},
		Spawn: func(args []string) error {
			f.events = append(f.events, "spawn")
			f.daemonUp = true
			f.liveID = "new"
			f.alive[500] = true
			return WriteRegistration(f.svc.StateDir, Registration{ID: "new", Version: "2.0.0", URL: "http://new", PID: 500})
		},
		Alive: func(pid int) bool { return f.alive[pid] },
		Terminate: func(pid int) error {
			f.events = append(f.events, "term")
			f.alive[pid] = false
			f.daemonUp = false
			return nil
		},
		Kill:     func(pid int) error { f.events = append(f.events, "kill"); f.alive[pid] = false; return nil },
		PortBusy: func(string) bool { return false },
	}
	return f
}

func (f *fake) register(reg Registration, up bool) {
	f.t.Helper()
	if err := WriteRegistration(f.svc.StateDir, reg); err != nil {
		f.t.Fatal(err)
	}
	f.daemonUp = up
	f.liveID = reg.ID
	f.alive[reg.PID] = true
}

func TestStartReusesSameVersionDaemon(t *testing.T) {
	f := newFake(t)
	f.register(Registration{ID: "a", Version: "2.0.0", URL: "http://a", PID: 100}, true)
	url, err := f.svc.Start(context.Background(), Daemon{})
	if err != nil || url != "http://a" {
		t.Fatalf("Start = %q, %v", url, err)
	}
	if len(f.events) != 0 {
		t.Fatalf("unexpected events %v", f.events)
	}
}

func TestStartReplacesDifferentVersionStoppingFirst(t *testing.T) {
	f := newFake(t)
	f.register(Registration{ID: "a", Version: "1.0.0", URL: "http://a", PID: 100}, true)
	url, err := f.svc.Start(context.Background(), Daemon{})
	if err != nil || url != "http://new" {
		t.Fatalf("Start = %q, %v", url, err)
	}
	if len(f.events) != 2 || f.events[0] != "term" || f.events[1] != "spawn" {
		t.Fatalf("events = %v, want [term spawn]", f.events)
	}
}

func TestStaleRegistrationIsRemovedWithoutSignalling(t *testing.T) {
	for name, reg := range map[string]Registration{
		"dead process": {ID: "a", Version: "2.0.0", URL: "http://a", PID: 100},
		"id mismatch":  {ID: "stale", Version: "2.0.0", URL: "http://a", PID: 100},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			f.register(reg, name == "id mismatch")
			f.liveID = "other"
			f.alive[100] = true
			if err := f.svc.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(f.events) != 0 {
				t.Fatalf("stale stop signalled: %v", f.events)
			}
			if _, err := ReadRegistration(f.svc.StateDir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("registration not removed: %v", err)
			}
		})
	}
}

func TestStopKillsDaemonIgnoringTerm(t *testing.T) {
	f := newFake(t)
	f.register(Registration{ID: "a", Version: "2.0.0", URL: "http://a", PID: 100}, true)
	f.svc.Terminate = func(int) error { f.events = append(f.events, "term"); return nil }
	if err := f.svc.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != 2 || f.events[1] != "kill" {
		t.Fatalf("events = %v, want [term kill]", f.events)
	}
	if _, err := ReadRegistration(f.svc.StateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("registration not removed: %v", err)
	}
}

func TestStartReportsForeignPortHolder(t *testing.T) {
	f := newFake(t)
	f.svc.Spawn = func([]string) error { return nil }
	f.svc.PortBusy = func(string) bool { return true }
	_, err := f.svc.Start(context.Background(), Daemon{Listen: "127.0.0.1:1"})
	if err == nil || !strings.Contains(err.Error(), "non-prism process") {
		t.Fatalf("err = %v", err)
	}
}

func TestWatchFiresWhenRegistrationReplaced(t *testing.T) {
	dir := t.TempDir()
	if err := WriteRegistration(dir, Registration{ID: "me"}); err != nil {
		t.Fatal(err)
	}
	lost := make(chan struct{})
	go Watch(context.Background(), dir, "me", time.Millisecond, func() { close(lost) })
	select {
	case <-lost:
		t.Fatal("fired while registration still ours")
	case <-time.After(20 * time.Millisecond):
	}
	if err := WriteRegistration(dir, Registration{ID: "other"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-lost:
	case <-time.After(time.Second):
		t.Fatal("watch did not fire after id change")
	}
}

func TestRemoveOwnRegistrationKeepsForeignFile(t *testing.T) {
	dir := t.TempDir()
	if err := WriteRegistration(dir, Registration{ID: "other"}); err != nil {
		t.Fatal(err)
	}
	if err := RemoveOwnRegistration(dir, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRegistration(dir); err != nil {
		t.Fatalf("foreign registration removed: %v", err)
	}
}
