package agentinstall

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/deLiseLINO/prism/internal/integrations"
)

func TestMaintenancePiManagedMarkerOwnsSelectedLauncher(t *testing.T) {
	for _, kind := range []string{"configured-script", "configured-link", "root-link", "invalid-kind", "invalid-entry", "unrelated-link", "fresh-custom", "fresh-wrong-bin"} {
		t.Run(kind, func(t *testing.T) {
			f := maintenanceSandbox(t)
			agent := filepath.Join(f.home, "custom", "agent")
			root := filepath.Join(agent, "install")
			launcher := filepath.Join(agent, "bin", "pi")
			entry := launcher
			markerType := "script"
			if kind == "configured-link" || kind == "root-link" || kind == "unrelated-link" {
				entry = filepath.Join(f.home, "custom", "visible", "pi")
				markerType = "symlink"
			}
			f.env["PI_CODING_AGENT_DIR"] = agent
			if kind == "root-link" {
				delete(f.env, "PI_CODING_AGENT_DIR")
				f.env["PI_MANAGED_INSTALL_ROOT"] = root
			}
			marker := map[string]any{"kind": "pi-managed-install", "schemaVersion": 1, "layout": "releases-v1", "entrypoint": map[string]string{"type": markerType, "path": entry}}
			if kind == "invalid-kind" {
				marker["kind"] = "unknown"
			}
			if kind == "invalid-entry" {
				marker["entrypoint"] = map[string]string{"type": "script", "path": filepath.Join(f.home, "arbitrary", "pi")}
			}
			launcherBody := "if [ \"$1\" = --version ]; then if [ -f \"$MUTATIONS\" ]; then echo 1.2.4; else echo 1.2.3; fi; else printf '%s' \"$PI_MANAGED_INSTALL_ROOT\" > \"$MUTATIONS\"; fi"
			publish := func() {
				if err := os.MkdirAll(root, 0755); err != nil {
					t.Fatal(err)
				}
				data, _ := json.Marshal(marker)
				if err := os.WriteFile(filepath.Join(root, "managed-install.json"), data, 0644); err != nil {
					t.Fatal(err)
				}
				f.executable(launcher, launcherBody)
				if entry != launcher {
					f.link(entry, launcher)
				}
			}
			fresh := kind == "fresh-custom" || kind == "fresh-wrong-bin"
			if !fresh {
				publish()
			}
			if kind == "unrelated-link" {
				if err := os.Remove(entry); err != nil {
					t.Fatal(err)
				}
				other := filepath.Join(f.home, "arbitrary", "pi")
				f.executable(other, "echo 1.2.3")
				f.link(entry, other)
				f.env["PI_EXECUTABLE"] = entry
			}
			if fresh {
				f.link(filepath.Join(f.tools, "sh"), "/bin/sh")
				f.executable(filepath.Join(f.tools, "curl"), "exit 9")
			}
			m := f.manager()
			if fresh {
				m.fetchScript = func(context.Context, string) (string, error) {
					if kind == "fresh-wrong-bin" {
						entry = filepath.Join(f.home, ".local", "bin", "pi")
						markerType = "symlink"
						marker["entrypoint"] = map[string]string{"type": markerType, "path": entry}
					}
					data, _ := json.Marshal(marker)
					script := filepath.Join(f.home, "downloaded.sh")
					body := "set -e\n/bin/mkdir -p " + shellQuote(root) + " " + shellQuote(filepath.Dir(launcher)) + " " + shellQuote(filepath.Dir(entry)) + "\nprintf '%s' " + shellQuote(string(data)) + " > " + shellQuote(filepath.Join(root, "managed-install.json")) + "\nprintf '%s' " + shellQuote("#!/bin/sh\n"+launcherBody+"\n") + " > " + shellQuote(launcher) + "\n/bin/chmod 755 " + shellQuote(launcher)
					if entry != launcher {
						body += "\n/bin/ln -s " + shellQuote(launcher) + " " + shellQuote(entry)
					}
					f.executable(script, body)
					return script, nil
				}
			}
			id := integrations.Pi
			var job Job
			var err error
			if fresh {
				job, err = m.Install(id, false)
			} else {
				job, err = m.Update(id)
			}
			if err != nil {
				t.Fatal(err)
			}
			invalid := kind == "invalid-kind" || kind == "invalid-entry" || kind == "unrelated-link"
			if invalid {
				if job.State != StateUnsupported || f.mutations() != "" {
					t.Fatalf("invalid marker authorized: %+v", job)
				}
				return
			}
			want := StateSucceeded
			if kind == "fresh-wrong-bin" {
				want = StateFailed
			}
			requireJob(t, m, id, want)
			if !fresh && f.mutations() != root {
				t.Fatalf("managed root not pinned: %q, want %q", f.mutations(), root)
			}
			if want == StateSucceeded {
				st, _ := m.StatusOf(id)
				if !st.Installed || !st.CanUpdate {
					t.Fatalf("managed launcher lost: %+v", st)
				}
			}
		})
	}
}
