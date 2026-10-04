package agentinstall

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/integrations"
)

func fetchOK(context.Context, string) (string, error) {
	return "", errors.New("unexpected script download")
}

func TestInstallUnsupportedWhenNoTool(t *testing.T) {
	f := maintenanceSandbox(t)
	m := f.manager()
	job, err := m.Install(integrations.Pi, false)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != StateUnsupported || !strings.Contains(job.Error, "npm") {
		t.Fatalf("missing prerequisite not reported: %+v", job)
	}
	if m.JobOf(integrations.Grok).State != StateIdle {
		t.Fatal("untouched job is not idle")
	}
	if _, ok := m.StatusOf(integrations.ID("nope")); ok {
		t.Fatal("unknown id resolved")
	}
	if _, err := m.Update(integrations.Grok); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("missing client update: %v", err)
	}
}

func TestInstallLifecycleRunFailureAndBoundedOutput(t *testing.T) {
	f := maintenanceSandbox(t)
	f.global(f.prefix, "@earendil-works/pi-coding-agent", "pi", "echo 1.2.3")
	f.npm("i=0; while [ $i -lt 2000 ]; do printf 'xxxxxxxxxx'; i=$((i+1)); done; printf 'network failure'; exit 1")
	m := f.manager()
	if _, err := m.Install(integrations.Pi, false); err != nil {
		t.Fatal(err)
	}
	job := requireJob(t, m, integrations.Pi, StateFailed)
	if len(job.Output) != outputTailCap || !strings.HasSuffix(job.Output, "network failure") || job.Error != "exit status 1" {
		t.Fatalf("failed transcript: %+v", job)
	}
}

func TestMaintenanceNativeUpdatesUseSelectedAbsoluteEntry(t *testing.T) {
	for _, key := range []string{"codex", "claude", "grok", "omp", "pi", "hermes"} {
		t.Run(key, func(t *testing.T) {
			f := maintenanceSandbox(t)
			entry := filepath.Join(f.home, ".local", "bin", key)
			body := "if [ \"$1\" = '--version' ]; then echo 'client 1.2.3'; else printf '%s\\n' \"$*\" >> \"$MUTATIONS\"; fi"
			if key == "grok" {
				target := filepath.Join(f.home, ".grok", "bin", key)
				f.executable(target, body)
				f.link(entry, target)
			} else {
				f.executable(entry, body)
			}
			f.env["PATH"] = filepath.Dir(entry) + ":" + f.env["PATH"]
			f.executable(filepath.Join(f.tools, key), "echo 'ambient 0.0.0'; exit 1")
			m := f.manager()
			st, _ := m.StatusOf(integrations.ID(key))
			if !st.CanUpdate {
				t.Fatalf("documented native refused: %+v", st)
			}
			job, err := m.Update(integrations.ID(key))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(job.Command, entry+" ") {
				t.Fatalf("not selected absolute entry: %q", job.Command)
			}
			requireJob(t, m, integrations.ID(key), StateSucceeded)
			want := "update"
			if key == "hermes" {
				want += " --yes"
			}
			if key == "pi" {
				want += " --self"
			}
			if got := f.mutations(); got != want {
				t.Fatalf("update mutation %q, want %q", got, want)
			}
		})
	}
}

func TestMaintenanceScriptFetchAndBoundVerification(t *testing.T) {
	for _, kind := range []string{"success", "fetch-failure", "no-client", "hermes-args"} {
		t.Run(kind, func(t *testing.T) {
			f := maintenanceSandbox(t)
			f.link(filepath.Join(f.tools, "bash"), "/bin/bash")
			f.executable(filepath.Join(f.tools, "git"), "exit 0")
			f.executable(filepath.Join(f.tools, "curl"), "exit 0")
			id := integrations.Grok
			entry := filepath.Join(f.home, ".grok", "bin", "grok")
			f.env["PATH"] = filepath.Dir(entry) + ":" + f.env["PATH"]
			if kind == "hermes-args" {
				id = integrations.Hermes
				entry = filepath.Join(f.home, ".local", "bin", "hermes")
				f.env["PATH"] = filepath.Dir(entry) + ":" + f.env["PATH"]
			}
			m := f.manager()
			fetched := filepath.Join(f.home, "downloaded.sh")
			m.fetchScript = func(ctx context.Context, url string) (string, error) {
				if kind == "fetch-failure" {
					return "", errors.New("unavailable")
				}
				if kind == "hermes-args" && url != "https://hermes-agent.nousresearch.com/install.sh" {
					t.Errorf("wrong official installer: %s", url)
				}
				body := "/bin/mkdir -p " + shellQuote(filepath.Dir(entry)) + "\nprintf %s " + shellQuote("#!/bin/sh\necho 1.2.3\n") + " > " + shellQuote(entry) + "\n/bin/chmod 755 " + shellQuote(entry)
				if kind == "no-client" {
					body = "exit 0"
				}
				if kind == "hermes-args" {
					body = "[ \"$1\" = '--non-interactive' ] || exit 7\n" + body
				}
				f.executable(fetched, body)
				return fetched, nil
			}
			if _, err := m.Install(id, false); err != nil {
				t.Fatal(err)
			}
			want := StateSucceeded
			if kind == "fetch-failure" || kind == "no-client" {
				want = StateFailed
			}
			job := requireJob(t, m, id, want)
			if kind == "fetch-failure" && !strings.HasPrefix(job.Error, "fetch script: ") {
				t.Fatalf("download failure: %+v", job)
			}
			if kind != "fetch-failure" {
				if _, err := os.Stat(fetched); !os.IsNotExist(err) {
					t.Fatalf("download not removed: %v", err)
				}
			}
		})
	}
}

func TestMaintenanceActiveRootAndShutdownAdmission(t *testing.T) {
	f := maintenanceSandbox(t)
	f.global(f.prefix, "@openai/codex", "codex", "echo 1.2.3")
	f.global(f.prefix, "@earendil-works/pi-coding-agent", "pi", "echo 1.2.3")
	f.npm("printf 'started\\n' > " + shellQuote(filepath.Join(f.home, "started")) + "; while :; do :; done")
	m := f.manager()
	first, err := m.Update(integrations.Codex)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []integrations.ID{integrations.Codex, integrations.Pi} {
		if _, err := m.Install(id, true); !errors.Is(err, ErrInstallActive) {
			t.Fatalf("shared-root job admitted for %s: %v", id, err)
		}
	}
	if job := m.JobOf(integrations.Codex); job.StartedAt != first.StartedAt || job.State == StateUnsupported {
		t.Fatalf("live job overwritten: %+v", job)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	m.Stop(ctx)
	requireJob(t, m, integrations.Codex, StateInterrupted)
	if _, err := m.Install(integrations.Pi, false); err == nil {
		t.Fatal("stopped manager admitted work")
	}
}

func TestMaintenanceJobDeadlineIncludesVerification(t *testing.T) {
	f := maintenanceSandbox(t)
	f.global(f.prefix, "opencode-ai", "opencode", "while :; do :; done")
	f.npm("exit 0")
	m := f.manager()
	m.jobTimeout = 650 * time.Millisecond
	m.verifyTimeout = 2 * time.Second
	m.verifyRetry = 3 * time.Second
	if _, err := m.Update(integrations.Opencode); err != nil {
		t.Fatal(err)
	}
	job := requireJob(t, m, integrations.Opencode, StateFailed)
	if !strings.Contains(job.Error, "timed out") {
		t.Fatalf("whole deadline not applied: %+v", job)
	}
}

func TestMaintenanceTimeoutOnlyVerificationRetry(t *testing.T) {
	for _, retry := range []bool{true, false} {
		t.Run(fmt.Sprint(retry), func(t *testing.T) {
			f := maintenanceSandbox(t)
			marker := filepath.Join(f.home, "probed")
			body := "if [ ! -f " + shellQuote(marker) + " ]; then printf 'first\\n' > " + shellQuote(marker) + "; while :; do :; done; fi\necho 1.2.3"
			if !retry {
				body = "printf 'attempt\\n' >> " + shellQuote(marker) + "; echo ready; exit 1"
			}
			f.global(f.prefix, "opencode-ai", "opencode", body)
			f.npm("exit 0")
			m := f.manager()
			m.verifyTimeout = 500 * time.Millisecond
			m.verifyRetry = time.Second
			if _, err := m.Update(integrations.Opencode); err != nil {
				t.Fatal(err)
			}
			want := StateSucceeded
			if !retry {
				want = StateFailed
			}
			requireJob(t, m, integrations.Opencode, want)
			if !retry {
				data, err := os.ReadFile(marker)
				if err != nil {
					t.Fatal(err)
				}
				if string(data) != "attempt\n" {
					t.Fatal("non-timeout retried")
				}
			}
		})
	}
}
