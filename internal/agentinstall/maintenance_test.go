package agentinstall

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/integrations"
)

type maintenanceFixture struct {
	t                        *testing.T
	home, tools, prefix, log string
	env                      integrations.Env
}

func maintenanceSandbox(t *testing.T) *maintenanceFixture {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &maintenanceFixture{t: t, home: home, tools: filepath.Join(home, "tools"), prefix: filepath.Join(home, "global"), log: filepath.Join(home, "mutations")}
	f.env = integrations.Env{"HOME": home, "PATH": f.tools + ":" + filepath.Join(f.prefix, "bin"), "MUTATIONS": f.log}
	f.executable(filepath.Join(f.tools, "node"), "exit 0")
	t.Setenv("PATH", f.env["PATH"])
	return f
}

func (f *maintenanceFixture) executable(path, body string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0755); err != nil {
		f.t.Fatal(err)
	}
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func (f *maintenanceFixture) npm(body string) {
	f.executable(filepath.Join(f.tools, "npm"), "if [ \"$1 $2\" = 'prefix -g' ]; then printf '%s\\n' "+shellQuote(f.prefix)+"; exit 0; fi\nprintf '%s\\n' \"$*\" >> \"$MUTATIONS\"\n"+body)
}
func (f *maintenanceFixture) packageEntry(root, pkg, binary, versionBody string) string {
	f.t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(pkg))
	target := filepath.Join(dir, "bin", binary)
	f.executable(target, versionBody)
	manifest := fmt.Sprintf(`{"name":%q,"version":"9.8.7","bin":{%q:%q}}`, pkg, binary, "bin/"+binary)
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(manifest), 0644); err != nil {
		f.t.Fatal(err)
	}
	return target
}
func (f *maintenanceFixture) link(entry, target string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(entry), 0755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Symlink(target, entry); err != nil {
		f.t.Fatal(err)
	}
}
func (f *maintenanceFixture) global(prefix, pkg, binary, body string) string {
	target := f.packageEntry(filepath.Join(prefix, "lib", "node_modules"), pkg, binary, body)
	entry := filepath.Join(prefix, "bin", binary)
	f.link(entry, target)
	return entry
}
func (f *maintenanceFixture) manager() *Manager {
	f.t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "formula") || strings.Contains(r.URL.Path, "cask") {
			token := strings.TrimSuffix(filepath.Base(r.URL.Path), ".json")
			fmt.Fprintf(w, `{"name":%q,"token":%q,"version":"1.2.4","versions":{"stable":"1.2.4"}}`, token, token)
		} else {
			fmt.Fprint(w, `{"version":"1.2.4"}`)
		}
	}))
	f.t.Cleanup(server.Close)
	client := server.Client()
	transport := client.Transport
	client.Transport = fixtureTransport{transport: transport, server: server.URL}
	stat := func(path string) (os.FileInfo, error) {
		if path != f.home && !inside(path, f.home) {
			return nil, os.ErrNotExist
		}
		return os.Stat(path)
	}
	m := NewManager(f.env, ExecRunner{}, stat, time.Now, func(context.Context, string) (string, error) { return "", errors.New("unexpected script download") }, client)
	f.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		m.Stop(ctx)
	})
	return m
}

type fixtureTransport struct {
	transport http.RoundTripper
	server    string
}

func (t fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	u := *r.URL
	local, _ := http.NewRequest(http.MethodGet, t.server, nil)
	u.Scheme, u.Host = local.URL.Scheme, local.URL.Host
	copy.URL = &u
	return t.transport.RoundTrip(copy)
}

func (f *maintenanceFixture) mutations() string {
	f.t.Helper()
	data, err := os.ReadFile(f.log)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		f.t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}
func requireJob(t *testing.T, m *Manager, id integrations.ID, state JobState) Job {
	t.Helper()
	job := waitTerminal(t, m, string(id), 20*time.Second)
	if job.State != state {
		t.Fatalf("job state %s, want %s; error %q; command %q", job.State, state, job.Error, job.Command)
	}
	return job
}

func TestMaintenanceNpmPreservesObservedDestination(t *testing.T) {
	for _, pkg := range []string{"opencode-ai", "@opencode/cli"} {
		t.Run(pkg, func(t *testing.T) {
			f := maintenanceSandbox(t)
			observed := filepath.Join(f.home, "selected")
			entry := f.global(observed, pkg, "opencode", "if [ \"$1\" = upgrade ]; then printf '%s\\n' \"$NPM_CONFIG_PREFIX $*\" >> \"$MUTATIONS\"; else if [ -f \"$MUTATIONS\" ]; then echo 1.2.4; else echo 1.2.3; fi; fi")
			f.env["OPENCODE_EXECUTABLE"] = entry
			f.global(f.prefix, "@opencode/cli", "opencode", "echo 'other 2.3.4'")
			f.env["PATH"] = filepath.Join(observed, "bin") + ":" + f.env["PATH"]
			f.npm("exit 0")
			m := f.manager()
			st, _ := m.StatusOf(integrations.Opencode)
			if !st.CanUpdate {
				t.Fatalf("valid global owner refused: %+v", st)
			}
			if _, err := m.Update(integrations.Opencode); err != nil {
				t.Fatal(err)
			}
			requireJob(t, m, integrations.Opencode, StateSucceeded)
			want := observed + " upgrade"
			if got := f.mutations(); got != want {
				t.Fatalf("mutated %q, want selected global destination %q", got, want)
			}
		})
	}
}

func TestMaintenanceRefusalsAgreeWithStatus(t *testing.T) {
	for _, kind := range []string{"project", "unknown-package", "wrong-bin", "nested", "opaque", "pnpm", "unresolved"} {
		t.Run(kind, func(t *testing.T) {
			f := maintenanceSandbox(t)
			entry := filepath.Join(f.prefix, "bin", "opencode")
			switch kind {
			case "project":
				f.link(entry, f.packageEntry(filepath.Join(f.home, "project", "node_modules"), "opencode-ai", "opencode", "echo 1.2.3"))
			case "unknown-package":
				f.global(f.prefix, "other-client", "opencode", "echo 1.2.3")
			case "wrong-bin":
				target := f.packageEntry(filepath.Join(f.prefix, "lib", "node_modules"), "opencode-ai", "other", "echo 1.2.3")
				f.link(entry, target)
			case "nested":
				prefix := filepath.Join(f.home, "node_modules", "nested")
				f.global(prefix, "opencode-ai", "opencode", "echo 1.2.3")
				f.env["PATH"] = filepath.Join(prefix, "bin") + ":" + f.env["PATH"]
			case "opaque":
				f.executable(entry, "echo 1.2.3")
			case "pnpm":
				entry = filepath.Join(f.home, "pnpm", "opencode")
				f.executable(entry, "echo 1.2.3")
				f.env["PATH"] = filepath.Dir(entry) + ":" + f.env["PATH"]
			case "native-opencode":
				entry = filepath.Join(f.home, ".opencode", "bin", "opencode")
				f.executable(entry, "echo 1.2.3")
				f.env["PATH"] = filepath.Dir(entry) + ":" + f.env["PATH"]
			case "unresolved":
				f.global(f.prefix, "opencode-ai", "opencode", "echo 1.2.3")
			}
			f.npm("exit 0")
			m := f.manager()
			if kind == "unresolved" {
				m.eval = func(string) (string, error) { return "", errors.New("cannot resolve") }
			}
			st, _ := m.StatusOf(integrations.Opencode)
			if !st.Installed || st.CanUpdate || st.Reason == "" {
				t.Fatalf("unproven owner authorized: %+v", st)
			}
			for _, op := range []string{"update", "reinstall"} {
				var job Job
				var err error
				if op == "update" {
					job, err = m.Update(integrations.Opencode)
				} else {
					job, err = m.Install(integrations.Opencode, true)
				}
				if err != nil {
					t.Fatal(err)
				}
				if job.State != StateUnsupported || job.Error != st.Reason {
					t.Fatalf("%s refusal %+v disagrees with status %+v", op, job, st)
				}
			}
			if f.mutations() != "" {
				t.Fatal("refused action mutated package manager")
			}
		})
	}
}

func TestMaintenanceReinstallKeepsNpmOwner(t *testing.T) {
	f := maintenanceSandbox(t)
	f.global(f.prefix, "@openai/codex", "codex", "echo 1.2.3")
	f.npm("exit 0")
	f.executable(filepath.Join(f.tools, "brew"), "printf 'brew mutation\\n' >> \"$MUTATIONS\"; exit 1")
	m := f.manager()
	if _, err := m.Install(integrations.Codex, true); err != nil {
		t.Fatal(err)
	}
	requireJob(t, m, integrations.Codex, StateSucceeded)
	want := "install -g --prefix " + f.prefix + " @openai/codex@latest --force"
	if got := f.mutations(); got != want {
		t.Fatalf("reinstall switched owner or destination: %q, want %q", got, want)
	}
}

func TestMaintenanceVerificationRejectsChangedOwnerOrInvalidVersion(t *testing.T) {
	for _, kind := range []string{"other-prefix", "other-package", "empty", "missing-version", "missing-entry", "unchanged"} {
		t.Run(kind, func(t *testing.T) {
			f := maintenanceSandbox(t)
			entry := f.global(f.prefix, "opencode-ai", "opencode", "echo 'client 1.2.3'")
			body := "exit 0"
			if kind == "other-prefix" || kind == "other-package" {
				pkg := "opencode-ai"
				prefix := filepath.Join(f.home, "other")
				if kind == "other-package" {
					prefix = f.prefix
					pkg = "@opencode/cli"
				}
				target := f.packageEntry(filepath.Join(prefix, "lib", "node_modules"), pkg, "opencode", "echo 1.2.3")
				body = "/bin/rm " + shellQuote(entry) + "; /bin/ln -s " + shellQuote(target) + " " + shellQuote(entry)
			} else if kind == "missing-entry" {
				body = "/bin/rm " + shellQuote(entry)
			} else if kind == "empty" || kind == "missing-version" {
				target, err := filepath.EvalSymlinks(entry)
				if err != nil {
					t.Fatal(err)
				}
				content := "#!/bin/sh\nexit 0\n"
				if kind == "missing-version" {
					content = "#!/bin/sh\necho ready\n"
				}
				body = "printf %s " + shellQuote(content) + " > " + shellQuote(target)
			}
			f.npm(body)
			m := f.manager()
			if _, err := m.Install(integrations.Opencode, true); err != nil {
				t.Fatal(err)
			}
			want := StateFailed
			if kind == "unchanged" {
				want = StateSucceeded
			}
			requireJob(t, m, integrations.Opencode, want)
		})
	}
}

func TestMaintenanceFreshInstallBoundToPlannedEntry(t *testing.T) {
	for _, kind := range []string{"visible", "not-visible", "other-copy"} {
		t.Run(kind, func(t *testing.T) {
			f := maintenanceSandbox(t)
			target := f.packageEntry(filepath.Join(f.prefix, "lib", "node_modules"), "@opencode/cli", "opencode", "echo 2.3.4")
			entry := filepath.Join(f.prefix, "bin", "opencode")
			body := "/bin/mkdir -p " + shellQuote(filepath.Dir(entry)) + "; /bin/ln -s " + shellQuote(target) + " " + shellQuote(entry)
			if kind == "not-visible" {
				f.env["PATH"] = f.tools
			}
			if kind == "other-copy" {
				other := filepath.Join(f.home, "other")
				f.env["PATH"] = filepath.Join(other, "bin") + ":" + f.env["PATH"]
				otherTarget := f.packageEntry(filepath.Join(other, "lib", "node_modules"), "@opencode/cli", "opencode", "echo 2.3.4")
				body = "/bin/mkdir -p " + shellQuote(filepath.Join(other, "bin")) + "; /bin/ln -s " + shellQuote(otherTarget) + " " + shellQuote(filepath.Join(other, "bin", "opencode"))
			}
			t.Setenv("PATH", f.env["PATH"])
			f.npm(body)
			m := f.manager()
			if _, err := m.Install(integrations.Opencode, false); err != nil {
				t.Fatal(err)
			}
			want := StateFailed
			if kind == "visible" || kind == "not-visible" {
				want = StateSucceeded
			}
			requireJob(t, m, integrations.Opencode, want)
			expected := "install -g --prefix " + f.prefix + " @opencode/cli@latest"
			if got := f.mutations(); got != expected {
				t.Fatalf("fresh mutation %q, want %q", got, expected)
			}
		})
	}
}

func TestMaintenanceRunnerUsesPassedPath(t *testing.T) {
	f := maintenanceSandbox(t)
	wrong := filepath.Join(f.home, "ambient")
	f.executable(filepath.Join(wrong, "npm"), "echo wrong")
	f.executable(filepath.Join(f.tools, "npm"), "echo selected")
	t.Setenv("PATH", wrong)
	var out bytes.Buffer
	if err := (ExecRunner{}).Run(context.Background(), f.env, []string{"npm"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "selected" {
		t.Fatalf("runner executed ambient PATH tool %q", got)
	}
}

func TestMaintenanceRunnerRejectsEmptyCommand(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("empty argv panicked: %v", r)
		}
	}()
	if err := (ExecRunner{}).Run(context.Background(), integrations.Env{}, nil, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("empty argv succeeded")
	}
}
