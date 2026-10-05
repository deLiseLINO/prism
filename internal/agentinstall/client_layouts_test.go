package agentinstall

import (
	"path/filepath"
	"testing"

	"github.com/deLiseLINO/prism/internal/integrations"
)

func TestMaintenanceKnownLaunchersUpdateWithoutAuthorizingReinstall(t *testing.T) {
	for _, kind := range []string{"hermes-bin", "hermes-generated", "claude-local", "pnpm"} {
		t.Run(kind, func(t *testing.T) {
			f := maintenanceSandbox(t)
			id := integrations.Hermes
			entry := filepath.Join(f.home, ".hermes", "bin", "hermes")
			body := "if [ \"$1\" = --version ]; then echo 1.2.3; elif [ \"$2\" = --check ]; then echo 'Update available: 2 commits behind origin/main'; else printf '%s\\n' \"$*\" >> \"$MUTATIONS\"; fi"
			switch kind {
			case "hermes-generated":
				entry = filepath.Join(f.home, ".local", "bin", "hermes")
				target := filepath.Join(f.home, ".hermes", "hermes-agent", ".hermes", "bin", "hermes")
				f.executable(target, body)
				f.link(entry, target)
			case "claude-local":
				id = integrations.Claude
				entry = filepath.Join(f.home, ".claude", "local", "claude")
				body = "if [ \"$1\" = --version ]; then echo 1.2.3; elif [ \"$1\" = doctor ]; then echo 'unknown channel'; else printf '%s\\n' \"$*\" >> \"$MUTATIONS\"; fi"
				target := f.packageEntry(filepath.Join(f.home, ".claude", "local", "node_modules"), "@anthropic-ai/claude-code", "claude", body)
				f.link(entry, target)
			case "pnpm":
				id = integrations.Pi
				entry = filepath.Join(f.home, "Library", "pnpm", "pi")
				body = "if [ \"$1\" = --version ]; then if [ -f \"$MUTATIONS\" ]; then echo 1.2.4; else echo 1.2.3; fi; else printf '%s\\n' \"$PNPM_HOME $*\" >> \"$MUTATIONS\"; fi"
			}
			if kind == "hermes-bin" || kind == "pnpm" {
				f.executable(entry, body)
			}
			f.env["PATH"] = filepath.Dir(entry) + ":" + f.env["PATH"]
			m := f.manager()
			st, _ := m.StatusOf(id)
			if !st.CanUpdate || st.Path != entry {
				t.Fatalf("known launcher refused: %+v", st)
			}
			if _, err := m.Update(id); err != nil {
				t.Fatal(err)
			}
			requireJob(t, m, id, StateSucceeded)
			if f.mutations() == "" {
				t.Fatal("updater did not execute")
			}
			job, err := m.Install(id, true)
			if err != nil || job.State != StateUnsupported {
				t.Fatalf("update authority authorized reinstall: %+v / %v", job, err)
			}
		})
	}
}
