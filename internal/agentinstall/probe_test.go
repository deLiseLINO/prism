package agentinstall

import (
	"os"
	"testing"

	"prism/internal/integrations"
)

func TestLookPathMissing(t *testing.T) {
	env := envAdapter{env: pathEnv("/bin", "/usr/bin")}
	if got := LookPath(env, "absent", statWith().stat); got != "" {
		t.Errorf("LookPath(absent) = %q, want empty", got)
	}
}

// TestLookPathNonExecutableIgnored: a file present without exec bits never
// resolves, and neither does a directory that shadows the name.
func TestLookPathNonExecutableIgnored(t *testing.T) {
	stat := fakeStat{entries: map[string]os.FileMode{
		"/bin/tool":    0644,
		"/usr/bin/dir": os.ModeDir | 0755,
	}}
	env := envAdapter{env: pathEnv("/bin", "/usr/bin")}
	if got := LookPath(env, "tool", stat.stat); got != "" {
		t.Errorf("non-executable file resolved: %q", got)
	}
	if got := LookPath(env, "dir", stat.stat); got != "" {
		t.Errorf("directory resolved: %q", got)
	}
}

func TestLookPathPathOrder(t *testing.T) {
	env := envAdapter{env: pathEnv("/a", "/b")}
	stat := statWith("/a/tool", "/b/tool")
	if got := LookPath(env, "tool", stat.stat); got != "/a/tool" {
		t.Errorf("LookPath = %q, want /a/tool (first PATH dir wins)", got)
	}
	stat2 := statWith("/b/tool")
	if got := LookPath(env, "tool", stat2.stat); got != "/b/tool" {
		t.Errorf("LookPath = %q, want /b/tool", got)
	}
}

func TestLookPathEmptyPathEnv(t *testing.T) {
	env := envAdapter{env: integrations.Env{}}
	if got := LookPath(env, "tool", statWith("/bin/tool").stat); got != "" {
		t.Errorf("LookPath with no PATH = %q, want empty", got)
	}
}

func TestLookPathExplicitSlashPath(t *testing.T) {
	env := envAdapter{env: pathEnv("/bin")}
	if got := LookPath(env, "/opt/x/bin/thing", statWith("/opt/x/bin/thing").stat); got != "/opt/x/bin/thing" {
		t.Errorf("explicit path = %q, want /opt/x/bin/thing", got)
	}
	if got := LookPath(env, "/opt/x/bin/nope", statWith().stat); got != "" {
		t.Errorf("explicit missing path = %q, want empty", got)
	}
}

// TestDetectSourceMatrix walks the substring rules, including the real
// ground-truth paths observed on this machine 2026-09-09.
func TestDetectSourceMatrix(t *testing.T) {
	cases := []struct {
		path string
		want Source
	}{
		// ground truth from the live machine
		{"/Users/x/.local/lib/node_modules/@scope/cli.js", SourceNpm},
		{"/Users/x/node_modules/.pnpm/@opencode-ai+cli@1.0.0/node_modules/.bin/opencode2", SourcePnpm},
		{"/Users/x/.grok/downloads/grok-1.0.24-macos-aarch64", SourceScript},
		{"/Users/x/.local/share/claude/versions/1.0.32/claude", SourceUnknown},
		{"/Users/x/.local/bin/codex", SourceScript},
		{"/Users/x/.local/bin/omp", SourceScript},
		{"/Users/x/.local/bin/hermes", SourceScript},
		{"/Users/x/.opencode/bin/opencode", SourceScript},
		// rule matrix
		{"/opt/homebrew/bin/codex", SourceBrew},
		{"/usr/local/Cellar/codex/1.0/bin/codex", SourceBrew},
		{"/home/linuxbrew/linuxbrew/bin/pi", SourceBrew},
		{"/Users/x/.bun/bin/omp", SourceBun},
		// bun's global install resolves into ~/.bun/install/global/node_modules,
		// which must stay bun, not npm (live evidence 2026-09-09)
		{"/root/.bun/install/global/node_modules/pi-coding-agent/bin/omp", SourceBun},
		// pnpm 12+ global bins are shims in .../pnpm/bin, not node_modules symlinks
		{"/root/.local/share/pnpm/bin/codex", SourcePnpm},
		{"/Users/x/Library/pnpm/codex", SourcePnpm},
		// brew beats the pnpm-dir heuristic for the pnpm formula itself
		{"/home/linuxbrew/.linuxbrew/Cellar/pnpm/10.2.0/bin/pnpm", SourceBrew},
		{"/Users/x/.claude/local/claude", SourceScript},
		{"/usr/local/bin/codex", SourceUnknown},
		{"/nix/store/abc-codex/bin/codex", SourceUnknown},
		// node_modules without pnpm is npm; node_modules with pnpm is pnpm
		{"/usr/lib/node_modules/@openai/codex/bin/codex", SourceNpm},
		{"/usr/lib/pnpm/node_modules/@openai/codex/bin/codex", SourcePnpm},
	}
	for _, tc := range cases {
		if got := DetectSource(tc.path); got != tc.want {
			t.Errorf("DetectSource(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestDetectSourceFollowsSymlinksThroughManager(t *testing.T) {
	link := "/tmp/prefix/bin/codex"
	target := "/tmp/prefix/lib/node_modules/@openai/codex/bin/codex.js"
	stat := statWith(link)
	env := pathEnv("/tmp/prefix/bin")
	m := NewManager(env, &fakeRunner{}, stat.stat, testNow, fetchOK)
	m.eval = func(p string) (string, error) {
		if p != link {
			return "", os.ErrNotExist
		}
		return target, nil
	}
	st, ok := m.StatusOf(integrations.Codex)
	if !ok || !st.Installed {
		t.Fatalf("status = %+v, want installed", st)
	}
	if st.Source != SourceNpm {
		t.Fatalf("source = %q, want npm: npm links prefix/bin at the package dir, so classification must follow the symlink", st.Source)
	}
	if st.Path != link {
		t.Fatalf("path = %q, want the PATH link itself", st.Path)
	}
}
