package agentinstall

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/deLiseLINO/prism/internal/integrations"
)

func TestMaintenanceBunPinsDocumentedGlobalDirectories(t *testing.T) {
	for _, kind := range []string{"default", "explicit", "unproven"} {
		t.Run(kind, func(t *testing.T) {
			f := maintenanceSandbox(t)
			root := filepath.Join(f.home, ".bun", "install", "global")
			bin := filepath.Join(f.home, ".bun", "bin")
			if kind == "explicit" {
				root = filepath.Join(f.home, "custom-root")
				bin = filepath.Join(f.home, "custom-bin")
				f.env["BUN_INSTALL_GLOBAL_DIR"] = root
				f.env["BUN_INSTALL_BIN"] = bin
			}
			packageRoot := filepath.Join(root, "node_modules")
			if kind == "unproven" {
				packageRoot = filepath.Join(f.home, "node_modules")
			}
			target := f.packageEntry(packageRoot, "@oh-my-pi/pi-coding-agent", "omp", "echo 18.6.1")
			f.link(filepath.Join(bin, "omp"), target)
			f.env["PATH"] = bin + ":" + f.env["PATH"]
			f.executable(filepath.Join(f.tools, "bun"), "printf '%s\\n' \"$*\" \"$BUN_INSTALL_GLOBAL_DIR\" \"$BUN_INSTALL_BIN\" >> \"$MUTATIONS\"")
			m := f.manager()
			st, _ := m.StatusOf(integrations.Omp)
			if kind == "unproven" {
				if st.Source != SourceBun || st.CanUpdate {
					t.Fatalf("unproven Bun root authorized: %+v", st)
				}
				job, err := m.Update(integrations.Omp)
				if err != nil {
					t.Fatal(err)
				}
				if job.State != StateUnsupported || job.Error != st.Reason || f.mutations() != "" {
					t.Fatalf("unproven Bun mutation: %+v", job)
				}
				return
			}
			if !st.CanUpdate {
				t.Fatalf("documented Bun root refused: %+v", st)
			}
			for _, op := range []string{"update", "reinstall"} {
				var err error
				if op == "update" {
					_, err = m.Update(integrations.Omp)
				} else {
					_, err = m.Install(integrations.Omp, true)
				}
				if err != nil {
					t.Fatal(err)
				}
				requireJob(t, m, integrations.Omp, StateSucceeded)
			}
			want := "install -g @oh-my-pi/pi-coding-agent@latest\n" + root + "\n" + bin + "\ninstall -g @oh-my-pi/pi-coding-agent@latest --force\n" + root + "\n" + bin
			if got := f.mutations(); got != want {
				t.Fatalf("Bun global root not pinned: %q, want %q", got, want)
			}
			if f.env["BUN_INSTALL_GLOBAL_DIR"] != "" && f.env["BUN_INSTALL_GLOBAL_DIR"] != root {
				t.Fatal("caller env changed")
			}
		})
	}
}

func TestMaintenanceHomebrewProvesPrefixAndObservedToken(t *testing.T) {
	for _, kind := range []string{"formula", "foreign-prefix", "unknown-token", "unversioned", "cask", "version-replacement", "reinstall"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "cask" && runtime.GOOS != "darwin" {
				t.Skip("cask registry is macOS-only")
			}
			f := maintenanceSandbox(t)
			prefix := filepath.Join(f.home, "brew-root")
			bin := filepath.Join(prefix, "bin")
			key := "omp"
			token := "omp"
			layout := "Cellar"
			if kind == "cask" {
				key = "codex"
				token = "codex"
				layout = "Caskroom"
			}
			if kind == "unknown-token" {
				token = "unknown"
			}
			target := filepath.Join(prefix, layout, token, "1.2.3", "bin", key)
			if kind == "unversioned" {
				target = filepath.Join(prefix, layout, token, key)
			}
			f.executable(target, "if [ -f \"$MUTATIONS\" ]; then echo 1.2.4; else echo 1.2.3; fi")
			entry := filepath.Join(bin, key)
			f.link(entry, target)
			f.env["PATH"] = bin + ":" + f.env["PATH"]
			response := prefix
			if kind == "foreign-prefix" {
				response = filepath.Join(f.home, "foreign")
			}
			body := "if [ \"$1\" = '--prefix' ]; then printf '%s\\n' " + shellQuote(response) + "; exit 0; fi\nif [ \"$1\" = info ]; then echo invalid; exit 0; fi\nprintf '%s\\n' \"$*\" >> \"$MUTATIONS\""
			if kind == "version-replacement" {
				next := filepath.Join(prefix, layout, token, "1.2.4", "bin", key)
				f.executable(next, "echo 1.2.4")
				body += "\n/bin/rm " + shellQuote(entry) + "; /bin/ln -s " + shellQuote(next) + " " + shellQuote(entry)
			}
			f.executable(filepath.Join(bin, "brew"), body)
			f.executable(filepath.Join(f.tools, "brew"), "if [ \"$1\" = '--prefix' ]; then printf '%s\\n' "+shellQuote(filepath.Join(f.home, "foreign"))+"; exit 0; fi; printf 'wrong brew mutation\\n' >> \"$MUTATIONS\"; exit 1")
			m := f.manager()
			st, _ := m.StatusOf(integrations.ID(key))
			if kind == "foreign-prefix" || kind == "unversioned" {
				if st.CanUpdate || st.Reason == "" {
					t.Fatalf("unproven brew authorized: %+v", st)
				}
				job, err := m.Update(integrations.ID(key))
				if err != nil {
					t.Fatal(err)
				}
				if job.State != StateUnsupported || job.Error != st.Reason {
					t.Fatalf("brew refusal/status disagree: %+v / %+v", job, st)
				}
				if f.mutations() != "" {
					t.Fatal("unproven brew mutated")
				}
				return
			}
			if !st.CanUpdate {
				t.Fatalf("brew owner refused: %+v", st)
			}
			var err error
			if kind == "reinstall" {
				_, err = m.Install(integrations.ID(key), true)
			} else {
				_, err = m.Update(integrations.ID(key))
			}
			if err != nil {
				t.Fatal(err)
			}
			requireJob(t, m, integrations.ID(key), StateSucceeded)
			want := "upgrade --formula " + token
			if kind == "cask" {
				want = "upgrade --cask " + token
			}
			if kind == "reinstall" {
				want = "reinstall --formula omp"
			}
			if got := f.mutations(); got != want {
				t.Fatalf("wrong Homebrew owner: %q, want %q", got, want)
			}
		})
	}
}

func TestMaintenanceUnknownSelfUpdaterIsNotAuthority(t *testing.T) {
	f := maintenanceSandbox(t)
	f.executable(filepath.Join(f.prefix, "bin", "codex"), "printf 'unauthorized\\n' >> \"$MUTATIONS\"; echo 1.2.3")
	m := f.manager()
	st, _ := m.StatusOf(integrations.Codex)
	job, err := m.Update(integrations.Codex)
	if err != nil {
		t.Fatal(err)
	}
	if st.CanUpdate || job.State != StateUnsupported || job.Error != st.Reason || f.mutations() != "" {
		t.Fatalf("arbitrary binary self updater authorized: %+v / %+v", st, job)
	}
}

func TestMaintenanceRunnerPreservesShebangEnvironment(t *testing.T) {
	f := maintenanceSandbox(t)
	runtimePath := filepath.Join(f.tools, "node")
	f.executable(runtimePath, "printf '%s\\n' \"$EXPECTED\"")
	client := filepath.Join(f.tools, "client")
	if err := os.WriteFile(client, []byte("#!/usr/bin/env node\n"), 0755); err != nil {
		t.Fatal(err)
	}
	f.env["EXPECTED"] = "supplied-environment"
	var out strings.Builder
	if err := (ExecRunner{}).Run(context.Background(), f.env, []string{client}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "supplied-environment" {
		t.Fatalf("shebang runtime did not inherit supplied env: %q", out.String())
	}
}
