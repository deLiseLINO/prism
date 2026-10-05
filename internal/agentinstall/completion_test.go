package agentinstall

import (
	"runtime"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/integrations"
)

func TestMaintenanceTerminalStateAdmitsSuccessor(t *testing.T) {
	for _, state := range []JobState{StateSucceeded, StateFailed} {
		for _, shape := range []struct {
			name string
			ids  []integrations.ID
		}{
			{"same-binary", []integrations.ID{integrations.Codex}},
			{"shared-root", []integrations.ID{integrations.Codex, integrations.Pi}},
		} {
			t.Run(string(state)+"/"+shape.name, func(t *testing.T) {
				f := maintenanceSandbox(t)
				f.global(f.prefix, "@openai/codex", "codex", "echo 1.2.3")
				f.global(f.prefix, "@earendil-works/pi-coding-agent", "pi", "echo 1.2.3")
				body := "exit 0"
				if state == StateFailed {
					body = "exit 1"
				}
				f.npm(body)
				m := f.manager()
				deadline := time.Now().Add(20 * time.Second)
				for i := 0; i < 50; i++ {
					id := shape.ids[i%len(shape.ids)]
					if _, err := m.Install(id, true); err != nil {
						t.Fatalf("successor %d refused after terminal state: %v", i, err)
					}
					for {
						job := m.JobOf(id)
						if job.State == state {
							break
						}
						if job.State == StateFailed || job.State == StateSucceeded || job.State == StateInterrupted || job.State == StateUnsupported || time.Now().After(deadline) {
							t.Fatalf("job did not reach %s: %+v", state, job)
						}
						runtime.Gosched()
					}
				}
			})
		}
	}
}
