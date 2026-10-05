package agentinstall

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/integrations"
)

type fixtureInterpreterRunner struct {
	root string
}

func (r fixtureInterpreterRunner) Run(ctx context.Context, env integrations.Env, argv []string, stdout, stderr io.Writer) error {
	fallback := func() error { return (ExecRunner{}).Run(ctx, env, argv, stdout, stderr) }
	if len(argv) == 0 || argv[0] == "" {
		return fallback()
	}
	selected := LookPath(asEnv(env), argv[0], os.Stat)
	if selected == "" {
		return fallback()
	}
	selected, err := filepath.Abs(selected)
	if err != nil {
		return fallback()
	}
	real, err := filepath.EvalSymlinks(selected)
	if err != nil || !inside(real, r.root) {
		return fallback()
	}
	script, err := os.Open(selected)
	if err != nil {
		return fallback()
	}
	var header [len("#!/bin/sh\n")]byte
	_, readErr := io.ReadFull(script, header[:])
	closeErr := script.Close()
	if readErr != nil || closeErr != nil || string(header[:]) != "#!/bin/sh\n" {
		return fallback()
	}
	translated := append([]string{"/bin/sh", selected}, argv[1:]...)
	return (ExecRunner{}).Run(ctx, env, translated, stdout, stderr)
}

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
	m := NewManager(f.env, fixtureInterpreterRunner{root: f.home}, os.Stat, time.Now, func(context.Context, string) (string, error) { return "", errors.New("unexpected script download") })
	f.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		m.Stop(ctx)
	})
	return m
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
			f.global(observed, pkg, "opencode", "echo 'client 1.2.3'")
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
			want := "install -g --prefix " + observed + " " + pkg + "@latest"
			if got := f.mutations(); got != want {
				t.Fatalf("mutated %q, want selected global destination %q", got, want)
			}
		})
	}
}

func TestMaintenanceRefusalsAgreeWithStatus(t *testing.T) {
	for _, kind := range []string{"project", "unknown-package", "wrong-bin", "nested", "opaque", "pnpm", "native-opencode", "unresolved"} {
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
			if _, err := m.Update(integrations.Opencode); err != nil {
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
	for _, kind := range []string{"visible", "visible-alias", "prefix-alias", "prefix-alias-missing-suffix", "nested-prefix-alias", "not-visible", "other-copy"} {
		t.Run(kind, func(t *testing.T) {
			f := maintenanceSandbox(t)
			if kind == "nested-prefix-alias" {
				f.prefix = filepath.Join(f.home, "node_modules", "global")
			}
			base := f.prefix
			if kind == "prefix-alias-missing-suffix" {
				if err := os.MkdirAll(base, 0755); err != nil {
					t.Fatal(err)
				}
				f.prefix = filepath.Join(base, "missing", "global")
			}
			physicalPrefix := f.prefix
			packageRoot := filepath.Join(f.prefix, "lib", "node_modules")
			if kind == "prefix-alias-missing-suffix" {
				packageRoot = filepath.Join(f.home, "staging")
			}
			target := f.packageEntry(packageRoot, "@opencode/cli", "opencode", "echo 2.3.4")
			entry := filepath.Join(f.prefix, "bin", "opencode")
			body := "/bin/mkdir -p " + shellQuote(filepath.Dir(entry)) + "; /bin/ln -s " + shellQuote(target) + " " + shellQuote(entry)
			if kind == "prefix-alias-missing-suffix" {
				root := filepath.Join(physicalPrefix, "lib", "node_modules")
				body = "/bin/mkdir -p " + shellQuote(root) + " " + shellQuote(filepath.Dir(entry)) + "; /bin/cp -R " + shellQuote(packageRoot+"/.") + " " + shellQuote(root) + "; /bin/ln -s " + shellQuote(filepath.Join(root, "@opencode", "cli", "bin", "opencode")) + " " + shellQuote(entry)
			}
			if kind == "not-visible" {
				f.env["PATH"] = f.tools
			}
			if kind == "visible-alias" {
				alias := filepath.Join(f.home, "alias")
				f.link(alias, f.prefix)
				f.env["PATH"] = f.tools + ":" + filepath.Join(alias, "bin")
			}
			if kind == "prefix-alias" || kind == "prefix-alias-missing-suffix" || kind == "nested-prefix-alias" {
				alias := filepath.Join(f.home, "prefix-alias")
				f.link(alias, base)
				f.prefix = alias
				if kind == "prefix-alias-missing-suffix" {
					f.prefix = filepath.Join(alias, "missing", "global")
				}
				f.env["PATH"] = f.tools + ":" + filepath.Join(f.prefix, "bin")
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
			if kind == "nested-prefix-alias" {
				job := requireJob(t, m, integrations.Opencode, StateUnsupported)
				if !strings.Contains(job.Error, "npm global prefix could not be proven") || f.mutations() != "" {
					t.Fatalf("nested prefix alias admitted: %+v, mutations %q", job, f.mutations())
				}
				return
			}
			if kind == "not-visible" {
				for attempt := 0; attempt < 2; attempt++ {
					job := requireJob(t, m, integrations.Opencode, StateUnsupported)
					if !strings.Contains(job.Error, "not visible on PATH") || f.mutations() != "" {
						t.Fatalf("invisible destination mutated or wrong refusal: %+v, mutations %q", job, f.mutations())
					}
					if _, err := os.Lstat(entry); !os.IsNotExist(err) {
						t.Fatalf("invisible destination created: %v", err)
					}
					if attempt == 0 {
						if _, err := m.Install(integrations.Opencode, false); err != nil {
							t.Fatal(err)
						}
					}
				}
				return
			}
			want := StateFailed
			if kind == "visible" || kind == "visible-alias" || kind == "prefix-alias" || kind == "prefix-alias-missing-suffix" {
				want = StateSucceeded
			}
			requireJob(t, m, integrations.Opencode, want)
			expected := "install -g --prefix " + physicalPrefix + " @opencode/cli@latest"
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
