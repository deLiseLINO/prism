package agentinstall

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deLiseLINO/prism/internal/integrations"
)

func TestMaintenanceDiscoverySelectsNewestAndExplicitOverride(t *testing.T) {
	for _, kind := range []string{"newest", "tie", "override", "known"} {
		t.Run(kind, func(t *testing.T) {
			f := maintenanceSandbox(t)
			first := filepath.Join(f.tools, "opencode")
			second := filepath.Join(f.home, ".opencode", "bin", "opencode")
			f.executable(first, "echo 1.0.0")
			version := "2.0.0"
			if kind == "tie" {
				version = "1.0.0"
			}
			f.executable(second, "echo "+version)
			if kind == "known" {
				f.env["PATH"] = filepath.Join(f.home, "empty")
			}
			if kind == "override" {
				f.env["OPENCODE_EXECUTABLE"] = second
			}
			m := f.manager()
			st, _ := m.StatusOf(integrations.Opencode)
			want := second
			if kind == "tie" {
				want = first
			}
			if !st.Installed || st.Path != want {
				t.Fatalf("selected %q, want %q: %+v", st.Path, want, st)
			}
		})
	}
}

func TestMaintenanceToolchainDiscoveryIncludesNewestBeyondBound(t *testing.T) {
	for _, rootName := range []string{".nvm/versions/node", ".fnm/node-versions"} {
		t.Run(rootName, func(t *testing.T) {
			f := maintenanceSandbox(t)
			root := filepath.Join(f.home, filepath.FromSlash(rootName))
			for i := 1; i <= 140; i++ {
				if err := os.MkdirAll(filepath.Join(root, fmt.Sprintf("v9.%d.0", i)), 0755); err != nil {
					t.Fatal(err)
				}
			}
			bin := filepath.Join(root, "v10.0.0", "bin")
			if strings.HasPrefix(rootName, ".fnm") {
				bin = filepath.Join(root, "v10.0.0", "installation", "bin")
			}
			f.global(filepath.Dir(bin), "@opencode/cli", "opencode", "echo 2.0.0")
			f.executable(filepath.Join(bin, "npm"), "if [ \"$1 $2\" = 'prefix -g' ]; then echo "+shellQuote(f.prefix)+"; else printf installed > \"$MUTATIONS\"; fi")
			f.executable(filepath.Join(bin, "node"), "exit 0")
			if err := os.Remove(filepath.Join(f.tools, "node")); err != nil {
				t.Fatal(err)
			}
			m := f.manager()
			st, _ := m.StatusOf(integrations.Opencode)
			if !st.Installed || st.Path != filepath.Join(bin, "opencode") || !st.CanUpdate {
				t.Fatalf("newest managed client/runtime skipped: %+v", st)
			}
			if _, err := m.Install(integrations.Opencode, false); err != nil {
				t.Fatal(err)
			}
			requireJob(t, m, integrations.Opencode, StateSucceeded)
			if f.mutations() != "installed" {
				t.Fatal("managed installer tool did not run")
			}
		})
	}
}

type fixedDiscoveryRunner struct{ t *testing.T }

func (r fixedDiscoveryRunner) Run(_ context.Context, _ integrations.Env, argv []string, stdout, _ io.Writer) error {
	if len(argv) != 2 || argv[1] != "--version" {
		r.t.Fatalf("unexpected command at fixed filesystem boundary: %v", argv)
	}
	version := "1.0.0"
	if argv[0] == "/usr/local/bin/opencode" {
		version = "2.0.0"
	}
	_, err := fmt.Fprintln(stdout, version)
	return err
}

func TestMaintenanceFixedBinsAbsentCapturedPath(t *testing.T) {
	f := maintenanceSandbox(t)
	fixture := filepath.Join(f.home, "fixed-executable")
	f.executable(fixture, "exit 9")
	info, err := os.Stat(fixture)
	if err != nil {
		t.Fatal(err)
	}
	m := f.manager()
	m.stat = func(path string) (os.FileInfo, error) {
		if path == "/opt/homebrew/bin/opencode" || path == "/usr/local/bin/opencode" {
			return info, nil
		}
		return nil, os.ErrNotExist
	}
	m.eval = func(path string) (string, error) { return path, nil }
	m.runner = fixedDiscoveryRunner{t: t}
	st, _ := m.StatusOf(integrations.Opencode)
	if !st.Installed || st.Path != "/usr/local/bin/opencode" || st.CanUpdate {
		t.Fatalf("fixed-bin discovery or unknown-owner refusal: %+v", st)
	}
}
