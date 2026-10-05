package agentinstall

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deLiseLINO/prism/internal/integrations"
)

func TestMaintenancePrefixUsesOnlyBoundedStdout(t *testing.T) {
	for _, kind := range []string{"warning", "large-warning", "stderr-only", "stdout-overflow"} {
		t.Run(kind, func(t *testing.T) {
			f := maintenanceSandbox(t)
			receipt := filepath.Join(f.home, "prefix-receipts")
			f.env["PREFIX_RECEIPTS"] = receipt
			entry := filepath.Join(f.prefix, "bin", "opencode")
			target := f.packageEntry(filepath.Join(f.prefix, "lib", "node_modules"), "@opencode/cli", "opencode", "echo 2.3.4 >&2")
			response := "printf '%s\\n' " + shellQuote(f.prefix)
			switch kind {
			case "warning":
				response += "; printf 'configuration warning\\n' >&2"
			case "large-warning":
				response += "; i=0; while [ $i -lt 1000 ]; do printf 'warning text' >&2; i=$((i+1)); done"
			case "stderr-only":
				response += " >&2"
			case "stdout-overflow":
				response = "i=0; while [ $i -lt 5000 ]; do printf ' '; i=$((i+1)); done; " + response
			}
			f.executable(filepath.Join(f.tools, "npm"), "if [ \"$1 $2\" = 'prefix -g' ]; then printf 'prefix\\n' >> \"$PREFIX_RECEIPTS\"; "+response+"; printf 'prefix-complete\\n' >> \"$PREFIX_RECEIPTS\"; exit 0; fi\nprintf '%s\\n' \"$*\" >> \"$MUTATIONS\"\nprintf 'mutation diagnostic' >&2\n/bin/mkdir -p "+shellQuote(filepath.Dir(entry))+"; /bin/ln -s "+shellQuote(target)+" "+shellQuote(entry))
			m := f.manager()
			if _, err := m.Install(integrations.Opencode, false); err != nil {
				t.Fatal(err)
			}
			want := StateSucceeded
			if kind == "stderr-only" || kind == "stdout-overflow" {
				want = StateUnsupported
			}
			job := requireJob(t, m, integrations.Opencode, want)
			if data, err := os.ReadFile(receipt); err != nil || string(data) != "prefix\nprefix-complete\n" {
				t.Fatalf("prefix response did not complete: receipt %q, error %v; job %+v", data, err, job)
			}
			if want == StateUnsupported {
				if f.mutations() != "" {
					t.Fatal("invalid prefix mutated")
				}
				if _, err := os.Lstat(entry); !os.IsNotExist(err) {
					t.Fatalf("invalid prefix created an entry: %v", err)
				}
				return
			}
			mutation := "install -g --prefix " + f.prefix + " @opencode/cli@latest"
			if f.mutations() != mutation || job.Output != "mutation diagnostic" {
				t.Fatalf("unexpected mutation transcript: %+v, mutations %q", job, f.mutations())
			}
			if job.Command != filepath.Join(f.tools, "npm")+" "+mutation || job.Method != string(MethodNpm) {
				t.Fatalf("mutation metadata changed: %+v", job)
			}
			if got, err := os.Readlink(entry); err != nil || got != target {
				t.Fatalf("installed entry target %q, want %q; error %v", got, target, err)
			}
		})
	}
}

func TestMaintenanceDirectoryAliasSharesActiveRoot(t *testing.T) {
	f := maintenanceSandbox(t)
	f.global(f.prefix, "@opencode/cli", "opencode", "echo 2.3.4")
	physicalPrefix := f.prefix
	alias := filepath.Join(f.home, "alias")
	f.link(alias, physicalPrefix)
	f.prefix = alias
	f.env["PATH"] = f.tools + ":" + filepath.Join(alias, "bin")
	release := filepath.Join(f.home, "release")
	f.npm("while [ ! -f " + shellQuote(release) + " ]; do /bin/sleep 0.01; done")
	m := f.manager()
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0644) })
	first, err := m.Install(integrations.Codex, false)
	if err != nil || first.State != StateInstalling {
		t.Fatalf("fresh alias install not admitted: %+v, error %v", first, err)
	}
	st, _ := m.StatusOf(integrations.Opencode)
	if !st.CanUpdate || st.Path != filepath.Join(physicalPrefix, "bin", "opencode") {
		t.Fatalf("selected physical owner not proven: %+v", st)
	}
	if job, err := m.Update(integrations.Opencode); !errors.Is(err, ErrInstallActive) {
		t.Fatalf("same physical root admitted a second mutation: %+v, error %v", job, err)
	}
	if err := os.WriteFile(release, nil, 0644); err != nil {
		t.Fatal(err)
	}
	requireJob(t, m, integrations.Codex, StateFailed)
	if _, err := m.Update(integrations.Opencode); err != nil {
		t.Fatal(err)
	}
	requireJob(t, m, integrations.Opencode, StateSucceeded)
	if got := f.mutations(); got != "install -g --prefix "+physicalPrefix+" @openai/codex@latest\ninstall -g --prefix "+physicalPrefix+" @opencode/cli@latest" {
		t.Fatalf("unexpected root mutation transcript %q", got)
	}
}

func TestMaintenanceStatusNeverRunsHomebrew(t *testing.T) {
	f := maintenanceSandbox(t)
	prefix := filepath.Join(f.home, "brew-root")
	bin := filepath.Join(prefix, "bin")
	for _, key := range []string{"omp", "codex", "claude"} {
		layout, token := "Caskroom", key
		if key == "omp" {
			layout = "Cellar"
		} else if key == "claude" {
			token = "claude-code"
		}
		target := filepath.Join(prefix, layout, token, "1.2.3", "bin", key)
		f.executable(target, "echo 1.2.3")
		f.link(filepath.Join(bin, key), target)
	}
	f.env["PATH"] = bin + ":" + f.env["PATH"]
	probeLog := filepath.Join(f.home, "prefix-probes")
	f.executable(filepath.Join(bin, "brew"), "printf 'probe\\n' >> "+shellQuote(probeLog)+"; exec /bin/sleep 1")
	m := f.manager()
	m.StatusAll()
	st, _ := m.StatusOf(integrations.Omp)
	if !st.CanUpdate {
		t.Errorf("local brew capability refused: %+v", st)
	}
	if data, err := os.ReadFile(probeLog); !os.IsNotExist(err) {
		t.Errorf("status executed brew: %q, error %v", data, err)
	}
}

func TestMaintenanceHomebrewDeduplicatesPrefixCandidates(t *testing.T) {
	f := maintenanceSandbox(t)
	prefix := filepath.Join(f.home, "brew-root")
	bin := filepath.Join(prefix, "bin")
	target := filepath.Join(prefix, "Cellar", "omp", "1.2.3", "bin", "omp")
	f.executable(target, "echo 1.2.3")
	f.link(filepath.Join(bin, "omp"), target)
	f.env["PATH"] = bin + ":" + f.env["PATH"]
	f.executable(filepath.Join(bin, "brew"), "if [ \"$1\" = '--prefix' ]; then printf 'probe\\n' >> \"$MUTATIONS\"; exit 1; fi; printf 'mutation\\n' >> \"$MUTATIONS\"")
	m := f.manager()
	job, err := m.Update(integrations.Omp)
	if err != nil || job.State != StateUnsupported || f.mutations() != "probe" {
		t.Fatalf("duplicate prefix probes or mutation: %+v, error %v, calls %q", job, err, f.mutations())
	}
}

func TestMaintenanceBunFreshInstallUsesXDG(t *testing.T) {
	f := maintenanceSandbox(t)
	f.env["XDG_CACHE_HOME"] = filepath.Join(f.home, "cache")
	root := filepath.Join(f.env["XDG_CACHE_HOME"], ".bun", "install", "global")
	bin := filepath.Join(f.env["XDG_CACHE_HOME"], ".bun", "bin")
	f.env["PATH"] = bin + ":" + f.env["PATH"]
	target := f.packageEntry(filepath.Join(root, "node_modules"), "@oh-my-pi/pi-coding-agent", "omp", "echo 18.6.1")
	f.executable(filepath.Join(f.tools, "bun"), "printf '%s\\n' \"$BUN_INSTALL_GLOBAL_DIR\" \"$BUN_INSTALL_BIN\" >> \"$MUTATIONS\"\n/bin/mkdir -p \"$BUN_INSTALL_BIN\"\n/bin/ln -s "+shellQuote(target)+" \"$BUN_INSTALL_BIN/omp\"")
	m := f.manager()
	if _, err := m.Install(integrations.Omp, false); err != nil {
		t.Fatal(err)
	}
	requireJob(t, m, integrations.Omp, StateSucceeded)
	st, _ := m.StatusOf(integrations.Omp)
	if !st.CanUpdate || st.Source != SourceBun || st.Path != filepath.Join(bin, "omp") || f.mutations() != root+"\n"+bin {
		t.Fatalf("XDG destination/owner mismatch: %+v, mutation %q", st, f.mutations())
	}
	if m.env["BUN_INSTALL_GLOBAL_DIR"] != "" || m.env["BUN_INSTALL_BIN"] != "" || f.env["BUN_INSTALL_BIN"] != "" {
		t.Fatal("action mutated shared environment")
	}
}

func TestMaintenanceFreshInvisibleScriptDoesNotFetch(t *testing.T) {
	f := maintenanceSandbox(t)
	f.link(filepath.Join(f.tools, "bash"), "/bin/bash")
	f.executable(filepath.Join(f.tools, "curl"), "exit 0")
	m := f.manager()
	fetched := false
	m.fetchScript = func(context.Context, string) (string, error) {
		fetched = true
		path := filepath.Join(f.home, "installer")
		f.executable(path, "printf 'mutation\\n' >> \"$MUTATIONS\"")
		return path, nil
	}
	if _, err := m.Install(integrations.Grok, false); err != nil {
		t.Fatal(err)
	}
	job := requireJob(t, m, integrations.Grok, StateUnsupported)
	if fetched || f.mutations() != "" || !strings.Contains(job.Error, "not visible on PATH") {
		t.Fatalf("invisible script fetched/mutated: %+v, fetched %v", job, fetched)
	}
}
