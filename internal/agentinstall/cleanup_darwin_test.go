//go:build darwin

package agentinstall

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/integrations"
)

type cleanupObservation struct {
	Job          Job
	BinaryDenied bool
	RootDenied   bool
	Mutations    string
	StopElapsed  time.Duration
	Recovered    bool
}

func TestMaintenanceCleanupFixture(t *testing.T) {
	if os.Getenv("CLEANUP_FIXTURE") == "" {
		return
	}
	control, err := net.Dial("unix", os.Getenv("CLEANUP_CONTROL"))
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	f := maintenanceSandbox(t)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fixture := "exec " + shellQuote(binary) + " -test.run='^TestMaintenanceProcessFixture$'"
	mode := os.Getenv("CLEANUP_FIXTURE")
	f.env["PROCESS_FIXTURE"] = "exit"
	if mode == "shutdown" || mode == "deadline" || mode == "verify-timeout" {
		f.env["PROCESS_FIXTURE"] = "wait"
	}
	for _, key := range []string{"PROCESS_SOCKET", "PROCESS_GROUP_FILE", "PROCESS_CHILD_FILE", "PROCESS_MARKER"} {
		f.env[key] = os.Getenv(key)
	}
	f.env["PROCESS_PIPES"] = "retain"
	version := "echo 1.2.3"
	if strings.HasPrefix(mode, "verify") {
		version = fixture
	}
	f.global(f.prefix, "@openai/codex", "codex", version)
	f.global(f.prefix, "opencode-ai", "opencode", "echo 1.2.3")
	if strings.HasPrefix(mode, "verify") {
		f.npm("exit 0")
	} else {
		f.npm(fixture)
	}
	m := f.manager()
	if mode == "deadline" {
		m.jobTimeout = 500 * time.Millisecond
	}
	if mode == "verify-timeout" {
		m.verifyTimeout = 500 * time.Millisecond
		m.verifyRetry = time.Second
	}
	if _, err := m.Update(integrations.Codex); err != nil {
		t.Fatal(err)
	}
	var stopElapsed time.Duration
	if mode == "shutdown" {
		until := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(f.env["PROCESS_CHILD_FILE"]); err == nil {
				break
			}
			if time.Now().After(until) {
				t.Fatal("child did not start before shutdown")
			}
			time.Sleep(10 * time.Millisecond)
		}
		started := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		m.Stop(ctx)
		cancel()
		stopElapsed = time.Since(started)
	}
	job := requireJob(t, m, integrations.Codex, StateFailed)
	f.npm("exit 0")
	if strings.HasPrefix(mode, "verify") {
		real, err := filepath.EvalSymlinks(filepath.Join(f.prefix, "bin", "codex"))
		if err != nil {
			t.Fatal(err)
		}
		f.executable(real, "echo 1.2.3")
	}
	_, binaryErr := m.Update(integrations.Codex)
	if binaryErr == nil {
		requireJob(t, m, integrations.Codex, StateSucceeded)
	}
	_, rootErr := m.Update(integrations.Opencode)
	if rootErr == nil {
		requireJob(t, m, integrations.Opencode, StateSucceeded)
	}
	observation := cleanupObservation{Job: job, BinaryDenied: errors.Is(binaryErr, ErrInstallActive), RootDenied: errors.Is(rootErr, ErrInstallActive), StopElapsed: stopElapsed}
	if mode == "shutdown" {
		observation.BinaryDenied = binaryErr != nil
		observation.RootDenied = rootErr != nil
	}
	if err := json.NewEncoder(control).Encode(observation); err != nil {
		t.Fatal(err)
	}
	var recover [1]byte
	if _, err := io.ReadFull(control, recover[:]); err != nil {
		t.Fatal(err)
	}
	if mode == "shutdown" {
		observation.Mutations = f.mutations()
		if err := json.NewEncoder(control).Encode(observation); err != nil {
			t.Fatal(err)
		}
		return
	}
	_, err = m.Update(integrations.Opencode)
	observation.Recovered = err == nil
	if err == nil {
		requireJob(t, m, integrations.Opencode, StateSucceeded)
	}
	_, err = m.Update(integrations.Codex)
	observation.Recovered = observation.Recovered && err == nil
	if err == nil {
		requireJob(t, m, integrations.Codex, StateSucceeded)
	}
	observation.Mutations = f.mutations()
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	m.Stop(ctx)
	cancel()
	observation.StopElapsed = time.Since(started)
	if err := json.NewEncoder(control).Encode(observation); err != nil {
		t.Fatal(err)
	}
}

func TestMaintenanceUnconfirmedCleanupRetainsReservations(t *testing.T) {
	for _, mode := range []string{"mutation", "verify", "deadline", "verify-timeout", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			dir, err := os.MkdirTemp("", "prism-cleanup-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			listen := func(name string) *net.UnixListener {
				listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, name), Net: "unix"})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { listener.Close() })
				_ = listener.SetDeadline(time.Now().Add(20 * time.Second))
				return listener
			}
			children, controls := listen("child"), listen("control")
			profile := filepath.Join(dir, "policy.sb")
			if err := os.WriteFile(profile, []byte("(version 1) (allow default) (deny signal)\n"), 0600); err != nil {
				t.Fatal(err)
			}
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			groupFile, childFile, marker := filepath.Join(dir, "group"), filepath.Join(dir, "pid"), filepath.Join(dir, "mutation")
			cmd := exec.Command("/usr/bin/sandbox-exec", "-f", profile, binary, "-test.run=^TestMaintenanceCleanupFixture$")
			cmd.Env = append(os.Environ(), "CLEANUP_FIXTURE="+mode, "CLEANUP_CONTROL="+controls.Addr().String(), "PROCESS_SOCKET="+children.Addr().String(), "PROCESS_GROUP_FILE="+groupFile, "PROCESS_CHILD_FILE="+childFile, "PROCESS_MARKER="+marker)
			var output strings.Builder
			cmd.Stdout, cmd.Stderr = &output, &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			readPID := func(path string) int {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				pid, err := strconv.Atoi(string(data))
				if err != nil || pid <= 0 {
					t.Fatalf("invalid owned pid %q", data)
				}
				return pid
			}
			var group int
			defer func() {
				if group > 0 {
					_ = syscall.Kill(-group, syscall.SIGKILL)
				}
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}()
			control, err := controls.AcceptUnix()
			if err != nil {
				t.Fatalf("fixture did not connect: %v", err)
			}
			defer control.Close()
			_ = control.SetDeadline(time.Now().Add(20 * time.Second))
			child, err := children.AcceptUnix()
			if err != nil {
				t.Fatalf("actual child did not connect: %v", err)
			}
			defer child.Close()
			_ = child.SetDeadline(time.Now().Add(20 * time.Second))
			var ready [1]byte
			if _, err := io.ReadFull(child, ready[:]); err != nil {
				t.Fatal(err)
			}
			group, pid := readPID(groupFile), readPID(childFile)
			decoder := json.NewDecoder(bufio.NewReader(control))
			var first cleanupObservation
			if err := decoder.Decode(&first); err != nil {
				t.Fatalf("bounded cleanup response missing: %v", err)
			}
			groupErr, childErr := syscall.Kill(-group, 0), syscall.Kill(pid, 0)
			t.Logf("owned group=%d child=%d, group signal-zero=%v child signal-zero=%v, job=%+v, binaryDenied=%v rootDenied=%v", group, pid, groupErr, childErr, first.Job, first.BinaryDenied, first.RootDenied)
			if groupErr != nil || childErr != nil {
				t.Fatal("failure trigger did not leave a live child")
			}
			if first.StopElapsed >= 4*time.Second {
				t.Errorf("shutdown waited for its deadline despite bounded cleanup: %s", first.StopElapsed)
			}
			if !first.BinaryDenied || !first.RootDenied {
				t.Errorf("successor admitted while owned child lives: binaryDenied=%v rootDenied=%v", first.BinaryDenied, first.RootDenied)
			}
			if !strings.Contains(first.Job.Error, "cleanup") || !strings.Contains(first.Job.Error, "operation not permitted") {
				t.Errorf("unconfirmed cleanup reason hidden: %q", first.Job.Error)
			}
			if err := syscall.Kill(-group, syscall.SIGKILL); err != nil {
				t.Fatal(err)
			}
			group = 0
			_, _ = child.Write([]byte("release"))
			_, err = child.Read(ready[:])
			if !errors.Is(err, io.EOF) {
				t.Fatalf("owned child survived supervisor cleanup: %v", err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("late child mutation exists: %v", err)
			}
			if _, err := control.Write([]byte("r")); err != nil {
				t.Fatal(err)
			}
			var final cleanupObservation
			if err := decoder.Decode(&final); err != nil {
				t.Fatalf("recovery result missing: %v", err)
			}
			if !final.Recovered && mode != "shutdown" {
				t.Error("confirmed dead group did not permit safe recovery")
			}
			if final.StopElapsed >= time.Second && mode != "shutdown" {
				t.Errorf("Stop remained blocked: %s", final.StopElapsed)
			}
			wantCommands := 3
			if mode == "shutdown" {
				wantCommands = 1
			}
			if got := len(strings.Split(final.Mutations, "\n")); got != wantCommands {
				t.Errorf("overlapping mutations ran: got %d mutation commands, want %d; %q", got, wantCommands, final.Mutations)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatalf("fixture failed: %v", err)
			}
			t.Logf("confirmed recovery=%v Stop=%s, mutation commands=%q", final.Recovered, final.StopElapsed, final.Mutations)
		})
	}
}
