package agentinstall

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/deLiseLINO/prism/internal/integrations"
)

func TestWithUserPathFindsBinaryMissingFromGUIPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("login shell probe is covered on unix")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "omp")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	profile := "export PATH='" + strings.ReplaceAll(dir, "'", "'\\''") + "'\n"
	if err := os.WriteFile(filepath.Join(home, ".profile"), []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USER", "probe")
	t.Setenv("LOGNAME", "probe")
	stubPasswdShell(t, "/bin/sh")

	gui := integrations.Env{"PATH": "/usr/bin:/bin", "HOME": home, "USER": "probe", "LOGNAME": "probe"}
	merged := WithUserPath(gui)
	if LookPath(asEnv(merged), "omp", os.Stat) != bin {
		t.Fatalf("PATH = %q, want %s on it", merged["PATH"], dir)
	}
	if LookPath(asEnv(gui), "omp", os.Stat) != "" {
		t.Fatal("unmerged GUI PATH should still miss the binary")
	}
}

func TestWithUserPathKeepsGUIPathWhenProbeFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("login shell probe is covered on unix")
	}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".profile"), []byte("exit 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USER", "probe")
	t.Setenv("LOGNAME", "probe")
	stubPasswdShell(t, "/bin/sh")
	gui := integrations.Env{"PATH": "/usr/bin:/bin", "HOME": home, "USER": "probe", "LOGNAME": "probe"}
	merged := WithUserPath(gui)
	if merged["PATH"] != "/usr/bin:/bin" {
		t.Fatalf("PATH = %q, want the original GUI PATH", merged["PATH"])
	}
}

func TestMergePathUserDirsWinAndDedupes(t *testing.T) {
	sep := string(os.PathListSeparator)
	got := mergePath("/Users/x/.local/bin"+sep+"/usr/bin", "/usr/bin"+sep+"/bin")
	want := strings.Join([]string{"/Users/x/.local/bin", "/usr/bin", "/bin"}, sep)
	if got != want {
		t.Fatalf("mergePath = %q, want %q", got, want)
	}
}

func TestCleanPathOutputIgnoresShellNoise(t *testing.T) {
	raw := "no such file: /Users/x/.cargo/env\n/Users/x/.local/bin:/usr/bin\n"
	got := cleanPathOutput(raw)
	if got != "/Users/x/.local/bin:/usr/bin" {
		t.Fatalf("cleanPathOutput = %q", got)
	}
}

func stubPasswdShell(t *testing.T, shell string) {
	t.Helper()
	prev := lookupPasswdShell
	lookupPasswdShell = func(string) string { return shell }
	t.Cleanup(func() { lookupPasswdShell = prev })
}
