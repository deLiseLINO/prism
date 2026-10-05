//go:build unix

package agentinstall

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/integrations"
)

func TestMaintenanceProcessFixture(t *testing.T) {
	mode := os.Getenv("PROCESS_FIXTURE")
	if mode == "" {
		return
	}
	if mode == "child" {
		conn, err := net.Dial("unix", os.Getenv("PROCESS_SOCKET"))
		if err != nil {
			os.Exit(10)
		}
		defer conn.Close()
		ready := os.NewFile(3, "ready")
		_, _ = conn.Write([]byte("r"))
		_, _ = ready.Write([]byte("r"))
		_ = ready.Close()
		var release [1]byte
		if _, err := io.ReadFull(conn, release[:]); err == nil {
			_ = os.WriteFile(os.Getenv("PROCESS_MARKER"), []byte("late mutation"), 0600)
			_, _ = conn.Write([]byte("w"))
		}
		os.Exit(0)
	}
	r, w, err := os.Pipe()
	if err != nil {
		os.Exit(11)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestMaintenanceProcessFixture$")
	child.Env = append(os.Environ(), "PROCESS_FIXTURE=child")
	child.ExtraFiles = []*os.File{w}
	if os.Getenv("PROCESS_PIPES") == "retain" {
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
	}
	if err := child.Start(); err != nil {
		os.Exit(12)
	}
	_ = w.Close()
	var ready [1]byte
	if _, err := io.ReadFull(r, ready[:]); err != nil {
		os.Exit(13)
	}
	_ = r.Close()
	if mode == "exit" {
		os.Exit(0)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestMaintenanceProcessGroupCleanup(t *testing.T) {
	for _, mode := range []string{"leader-exit", "deadline", "shutdown"} {
		for _, pipes := range []string{"retain", "close"} {
			t.Run(mode+"/"+pipes, func(t *testing.T) {
				f := maintenanceSandbox(t)
				// Unix socket paths must fit the platform's address limit.
				dir, err := os.MkdirTemp("", "prism-process-")
				if err != nil {
					t.Fatal(err)
				}
				defer os.RemoveAll(dir)
				listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "socket"), Net: "unix"})
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
				_ = listener.SetDeadline(time.Now().Add(10 * time.Second))
				binary, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				marker := filepath.Join(dir, "mutation")
				f.env["PROCESS_SOCKET"] = listener.Addr().String()
				f.env["PROCESS_MARKER"] = marker
				f.env["PROCESS_PIPES"] = pipes
				f.env["PROCESS_FIXTURE"] = "wait"
				if mode == "leader-exit" {
					f.env["PROCESS_FIXTURE"] = "exit"
				}
				f.global(f.prefix, "@openai/codex", "codex", "echo 1.2.3")
				f.npm("exec " + shellQuote(binary) + " -test.run='^TestMaintenanceProcessFixture$'")
				m := f.manager()
				if mode == "deadline" {
					m.jobTimeout = 2 * time.Second
				}
				if _, err := m.Update(integrations.Codex); err != nil {
					t.Fatal(err)
				}
				conn, err := listener.AcceptUnix()
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				var ready [1]byte
				if _, err := io.ReadFull(conn, ready[:]); err != nil {
					t.Fatal(err)
				}
				if mode == "shutdown" {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					m.Stop(ctx)
				}
				want := StateFailed
				if mode == "shutdown" {
					want = StateInterrupted
				} else if mode == "leader-exit" && pipes == "close" {
					want = StateSucceeded
				}
				job := requireJob(t, m, integrations.Codex, want)
				if mode == "leader-exit" && pipes == "retain" && job.Error != exec.ErrWaitDelay.Error() {
					t.Errorf("pipe-drain failure hidden: %+v", job)
				}
				if mode == "deadline" && job.Error != "job timed out" {
					t.Errorf("deadline classification changed: %+v", job)
				}
				_, _ = conn.Write([]byte("release"))
				var reply [1]byte
				_, err = conn.Read(reply[:])
				if !errors.Is(err, io.EOF) {
					t.Errorf("descendant can execute after terminal state: reply %q, error %v", reply, err)
				}
				if data, err := os.ReadFile(marker); !os.IsNotExist(err) {
					t.Errorf("descendant wrote after terminal state: %q, error %v", data, err)
				}
			})
		}
	}
}
