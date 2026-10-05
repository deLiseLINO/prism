package agentinstall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deLiseLINO/prism/internal/integrations"
)

func TestMaintenanceNpmRequiresNode(t *testing.T) {
	f := maintenanceSandbox(t)
	f.global(f.prefix, "opencode-ai", "opencode", "echo 1.2.3")
	f.npm("exit 0")
	if err := os.Remove(filepath.Join(f.tools, "node")); err != nil {
		t.Fatal(err)
	}
	m := f.manager()
	status, _ := m.StatusOf(integrations.Opencode)
	if status.CanUpdate || !strings.Contains(status.Reason, "node") {
		t.Fatalf("missing runtime authorized: %+v", status)
	}
	job, err := m.Update(integrations.Opencode)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != StateUnsupported || job.Error != status.Reason || f.mutations() != "" {
		t.Fatalf("missing runtime refusal: %+v", job)
	}
}

func TestMaintenancePreferredFreshPackages(t *testing.T) {
	for _, key := range []string{"opencode", "omp"} {
		t.Run(key, func(t *testing.T) {
			f := maintenanceSandbox(t)
			pkg := "@opencode/cli"
			if key == "opencode" {
				target := f.packageEntry(filepath.Join(f.prefix, "lib", "node_modules"), pkg, key, "echo 2.3.4")
				f.npm("/bin/mkdir -p " + shellQuote(filepath.Join(f.prefix, "bin")) + "; /bin/ln -s " + shellQuote(target) + " " + shellQuote(filepath.Join(f.prefix, "bin", key)))
			} else {
				pkg = "@oh-my-pi/pi-coding-agent"
				root := filepath.Join(f.home, ".bun", "install", "global")
				bin := filepath.Join(f.home, ".bun", "bin")
				target := f.packageEntry(filepath.Join(root, "node_modules"), pkg, key, "echo 18.6.1")
				f.env["PATH"] = bin + ":" + f.env["PATH"]
				f.executable(filepath.Join(f.tools, "bun"), "printf '%s\\n' \"$*\" >> \"$MUTATIONS\"; /bin/mkdir -p \"$BUN_INSTALL_BIN\"; /bin/ln -s "+shellQuote(target)+" \"$BUN_INSTALL_BIN/omp\"")
			}
			m := f.manager()
			if _, err := m.Install(integrations.ID(key), false); err != nil {
				t.Fatal(err)
			}
			requireJob(t, m, integrations.ID(key), StateSucceeded)
			if !strings.HasSuffix(f.mutations(), pkg+"@latest") {
				t.Fatalf("invalid fresh package: %q", f.mutations())
			}
		})
	}
}
