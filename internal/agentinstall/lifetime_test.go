package agentinstall

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/integrations"
)

func TestMaintenanceLeaseOutlivesLeaderAndStopBudget(t *testing.T) {
	f := maintenanceSandbox(t)
	f.global(f.prefix, "@openai/codex", "codex", "echo 1.2.3")
	ready := filepath.Join(f.home, "descendant-ready")
	late := filepath.Join(f.home, "late-write")
	f.npm("(trap '' TERM; printf ready > " + shellQuote(ready) + "; /bin/sleep 2; printf late > " + shellQuote(late) + ") >/dev/null 2>&1 &\nwhile [ ! -f " + shellQuote(ready) + " ]; do :; done\nexit 0")
	m := f.manager()
	if _, err := m.Install(integrations.Codex, true); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("descendant never started")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := m.Install(integrations.Codex, true); !errors.Is(err, ErrInstallActive) {
		t.Fatalf("lease released before child termination: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := m.Stop(ctx); err == nil {
		t.Fatal("Stop reported settled cleanup within insufficient budget")
	}
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	job := m.JobOf(integrations.Codex)
	if job.State != StateInterrupted {
		t.Fatalf("job did not settle after cleanup: %+v", job)
	}
	time.Sleep(2200 * time.Millisecond)
	if _, err := os.Stat(late); !os.IsNotExist(err) {
		t.Fatal("descendant wrote after Stop returned")
	}
}
