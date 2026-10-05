package agentinstall

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deLiseLINO/prism/internal/integrations"
)

func TestMaintenanceNativeReinstallPreservesDestinationOrRefuses(t *testing.T) {
	for _, key := range []string{"codex", "claude", "grok", "omp", "hermes", "pi"} {
		t.Run(key, func(t *testing.T) {
			f := maintenanceSandbox(t)
			entry := filepath.Join(f.home, ".local", "bin", key)
			target := entry
			if key == "grok" {
				target = filepath.Join(f.home, ".grok", "bin", key)
			}
			if key == "claude" {
				target = filepath.Join(f.home, ".local", "share", "claude", "versions", "1.2.3")
			}
			f.executable(target, "echo 1.2.3")
			if target != entry {
				f.link(entry, target)
			}
			f.env["PATH"] = filepath.Dir(entry) + ":" + f.env["PATH"]
			f.link(filepath.Join(f.tools, "bash"), "/bin/bash")
			f.link(filepath.Join(f.tools, "sh"), "/bin/sh")
			f.executable(filepath.Join(f.tools, "curl"), "exit 0")
			f.executable(filepath.Join(f.tools, "git"), "exit 0")
			m := f.manager()
			fetched := filepath.Join(f.home, "installer.sh")
			m.fetchScript = func(context.Context, string) (string, error) {
				f.executable(fetched, "printf 'reinstall\\n' >> \"$MUTATIONS\"")
				return fetched, nil
			}
			if key == "hermes" || key == "pi" {
				m.fetchScript = func(context.Context, string) (string, error) {
					t.Error("unproven reinstall fetched script")
					return "", errors.New("unexpected")
				}
				job, err := m.Install(integrations.ID(key), true)
				if err != nil {
					t.Fatal(err)
				}
				if job.State != StateUnsupported || job.Error == "" || f.mutations() != "" {
					t.Fatalf("unproven reinstall mutated: %+v", job)
				}
				return
			}
			if _, err := m.Install(integrations.ID(key), true); err != nil {
				t.Fatal(err)
			}
			requireJob(t, m, integrations.ID(key), StateSucceeded)
			if f.mutations() != "reinstall" {
				t.Fatalf("native reinstall not run: %q", f.mutations())
			}
			if _, err := os.Stat(fetched); !os.IsNotExist(err) {
				t.Fatalf("script not removed: %v", err)
			}
		})
	}
}

func TestMaintenanceExistingDestinationOutsidePathIsNotFresh(t *testing.T) {
	f := maintenanceSandbox(t)
	f.global(f.prefix, "opencode-ai", "opencode", "echo 1.2.3")
	f.env["PATH"] = f.tools
	f.npm("printf 'wrong mutation\\n' >> \"$MUTATIONS\"")
	m := f.manager()
	job, err := m.Install(integrations.Opencode, true)
	if err != nil {
		t.Fatal(err)
	}
	if job.State == StateUnsupported {
		t.Fatalf("known selected destination refused: %+v", job)
	}
	requireJob(t, m, integrations.Opencode, StateSucceeded)
	if !strings.Contains(f.mutations(), "install -g --prefix "+f.prefix+" opencode-ai@latest") {
		t.Fatalf("selected destination not preserved: %q", f.mutations())
	}
}

func TestMaintenanceOmpReinstallRepairsSelectedBinaryWhenBunIsAvailable(t *testing.T) {
	for _, custom := range []bool{false, true} {
		name := "default-destination"
		if custom {
			name = "custom-destination"
		}
		t.Run(name, func(t *testing.T) {
			f := maintenanceSandbox(t)
			dir := filepath.Join(f.home, ".local", "bin")
			if custom {
				dir = filepath.Join(f.home, "custom-bin")
				f.env["PI_INSTALL_DIR"] = dir
			}
			selected := filepath.Join(dir, "omp")
			f.executable(selected, "echo 1.2.3")
			f.env["PATH"] = dir + ":" + f.env["PATH"]
			f.link(filepath.Join(f.tools, "sh"), "/bin/sh")
			f.executable(filepath.Join(f.tools, "curl"), "exit 0")
			f.link(filepath.Join(f.tools, "mkdir"), "/bin/mkdir")
			bunCopy := filepath.Join(f.home, ".bun", "bin", "omp")
			f.executable(filepath.Join(f.tools, "bun"), "printf 'bun\\n' >> \"$MUTATIONS\"")
			argsFile := filepath.Join(f.home, "installer-args")
			installer := filepath.Join(f.home, "installer.sh")
			m := f.manager()
			m.fetchScript = func(context.Context, string) (string, error) {
				f.executable(installer, `printf '%s' "$*" > `+shellQuote(argsFile)+`
if [ "$1" = "--binary" ]; then
	printf '#!/bin/sh\necho 2.0.0\n' > "${PI_INSTALL_DIR:-$HOME/.local/bin}/omp"
else
	printf 'bun\n' >> "$MUTATIONS"
	mkdir -p `+shellQuote(filepath.Dir(bunCopy))+`
	printf '#!/bin/sh\necho 9.9.9\n' > `+shellQuote(bunCopy)+`
fi`)
				return installer, nil
			}
			if _, err := m.Install(integrations.Omp, true); err != nil {
				t.Fatal(err)
			}
			requireJob(t, m, integrations.Omp, StateSucceeded)
			if args, err := os.ReadFile(argsFile); err != nil || string(args) != "--binary" {
				t.Fatalf("installer args %q, err %v", args, err)
			}
			out, err := exec.Command(selected).Output()
			if err != nil || strings.TrimSpace(string(out)) != "2.0.0" {
				t.Fatalf("selected copy not repaired: %q, err %v", out, err)
			}
			if _, err := os.Stat(bunCopy); !os.IsNotExist(err) || f.mutations() != "" {
				t.Fatalf("alternate copy installed: stat %v, mutations %q", err, f.mutations())
			}
		})
	}
}
