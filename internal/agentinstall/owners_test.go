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
	for _, kind := range []string{"default", "xdg", "install-over-xdg", "explicit", "global-only", "bin-only", "directory-alias", "unproven"} {
		t.Run(kind, func(t *testing.T) {
			f := maintenanceSandbox(t)
			root := filepath.Join(f.home, ".bun", "install", "global")
			bin := filepath.Join(f.home, ".bun", "bin")
			if kind == "xdg" || kind == "install-over-xdg" || kind == "global-only" || kind == "bin-only" {
				f.env["XDG_CACHE_HOME"] = filepath.Join(f.home, "cache")
				base := filepath.Join(f.env["XDG_CACHE_HOME"], ".bun")
				if kind == "install-over-xdg" {
					base = filepath.Join(f.home, "custom-install")
					f.env["BUN_INSTALL"] = base
				}
				root, bin = filepath.Join(base, "install", "global"), filepath.Join(base, "bin")
			}
			if kind == "explicit" || kind == "global-only" {
				root = filepath.Join(f.home, "custom-root")
				f.env["BUN_INSTALL_GLOBAL_DIR"] = root
			}
			if kind == "explicit" || kind == "bin-only" {
				bin = filepath.Join(f.home, "custom-bin")
				f.env["BUN_INSTALL_BIN"] = bin
			}
			if kind == "directory-alias" {
				root = filepath.Join(f.home, "physical-global")
				bin = filepath.Join(f.home, "physical-bin")
				if err := os.MkdirAll(root, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(bin, 0755); err != nil {
					t.Fatal(err)
				}
				f.env["BUN_INSTALL_GLOBAL_DIR"] = filepath.Join(f.home, "global-alias")
				f.env["BUN_INSTALL_BIN"] = filepath.Join(f.home, "bin-alias")
				f.link(f.env["BUN_INSTALL_GLOBAL_DIR"], root)
				f.link(f.env["BUN_INSTALL_BIN"], bin)
			}
			original := copyEnv(f.env)
			packageRoot := filepath.Join(root, "node_modules")
			if kind == "unproven" {
				packageRoot = filepath.Join(f.home, "node_modules")
			}
			target := f.packageEntry(packageRoot, "@oh-my-pi/pi-coding-agent", "omp", "echo 18.6.1")
			f.link(filepath.Join(bin, "omp"), target)
			visibleBin := bin
			if kind == "directory-alias" {
				visibleBin = f.env["BUN_INSTALL_BIN"]
			}
			f.env["PATH"] = visibleBin + ":" + f.env["PATH"]
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
			for _, key := range []string{"BUN_INSTALL", "XDG_CACHE_HOME", "BUN_INSTALL_GLOBAL_DIR", "BUN_INSTALL_BIN"} {
				if f.env[key] != original[key] || m.env[key] != original[key] {
					t.Fatalf("shared env changed at %s", key)
				}
			}
		})
	}
}

func TestMaintenanceHomebrewProvesPrefixAndObservedToken(t *testing.T) {
	for _, kind := range []string{"formula", "prefix-alias", "fresh-prefix-alias", "foreign-prefix", "unknown-token", "unversioned", "cask", "version-replacement", "reinstall"} {
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
			f.executable(target, "echo 1.2.3")
			entry := filepath.Join(bin, key)
			if kind != "fresh-prefix-alias" {
				f.link(entry, target)
			}
			f.env["PATH"] = bin + ":" + f.env["PATH"]
			response := prefix
			if kind == "prefix-alias" || kind == "fresh-prefix-alias" {
				response = filepath.Join(f.home, "brew-alias")
				f.link(response, prefix)
				f.env["PATH"] = filepath.Join(response, "bin") + ":" + f.env["PATH"]
			}
			if kind == "foreign-prefix" {
				response = filepath.Join(f.home, "foreign")
			}
			body := "if [ \"$1\" = '--prefix' ]; then printf '%s\\n' " + shellQuote(response) + "; exit 0; fi\nprintf '%s\\n' \"$*\" >> \"$MUTATIONS\""
			if kind == "fresh-prefix-alias" {
				body += "\n/bin/ln -s " + shellQuote(target) + " " + shellQuote(entry)
			}
			if kind == "version-replacement" {
				next := filepath.Join(prefix, layout, token, "1.2.4", "bin", key)
				f.executable(next, "echo 1.2.4")
				body += "\n/bin/rm " + shellQuote(entry) + "; /bin/ln -s " + shellQuote(next) + " " + shellQuote(entry)
			}
			f.executable(filepath.Join(bin, "brew"), body)
			f.executable(filepath.Join(f.tools, "brew"), "if [ \"$1\" = '--prefix' ]; then printf '%s\\n' "+shellQuote(filepath.Join(f.home, "foreign"))+"; exit 0; fi; printf 'wrong brew mutation\\n' >> \"$MUTATIONS\"; exit 1")
			m := f.manager()
			st, _ := m.StatusOf(integrations.ID(key))
			if kind == "foreign-prefix" {
				if !st.CanUpdate || st.Reason != "" {
					t.Fatalf("local brew capability refused before attestation: %+v", st)
				}
				for _, op := range []string{"update", "reinstall"} {
					var job Job
					var err error
					if op == "update" {
						job, err = m.Update(integrations.ID(key))
					} else {
						job, err = m.Install(integrations.ID(key), true)
					}
					if err != nil || job.State != StateUnsupported || !strings.Contains(job.Error, "must prove selected prefix") || f.mutations() != "" {
						t.Fatalf("foreign brew %s authorized: %+v, error %v", op, job, err)
					}
				}
				return
			}
			if kind == "unknown-token" || kind == "unversioned" {
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
			if kind != "fresh-prefix-alias" && !st.CanUpdate {
				t.Fatalf("brew owner refused: %+v", st)
			}
			var err error
			if kind == "reinstall" || kind == "fresh-prefix-alias" {
				_, err = m.Install(integrations.ID(key), true)
			} else {
				_, err = m.Update(integrations.ID(key))
			}
			if err != nil {
				t.Fatal(err)
			}
			requireJob(t, m, integrations.ID(key), StateSucceeded)
			want := "upgrade " + token
			if kind == "cask" {
				want = "upgrade --cask " + token
			}
			if kind == "reinstall" {
				want = "reinstall omp"
			} else if kind == "fresh-prefix-alias" {
				got := f.mutations()
				if !strings.HasPrefix(got, "install ") || filepath.Base(strings.TrimPrefix(got, "install ")) != token {
					t.Fatalf("wrong fresh Homebrew owner: %q", got)
				}
				return
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
