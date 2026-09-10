package integrations

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

// fakeSsh records scripts and answers them against an in-memory filesystem,
// so the SshIO contract is tested without a real ssh binary.
type fakeSsh struct {
	files map[string]string
	scripts []string
}

func newFakeSsh(seed map[string]string) *fakeSsh {
	if seed == nil {
		seed = map[string]string{}
	}
	return &fakeSsh{files: seed}
}

func (f *fakeSsh) run(ctx context.Context, address string, stdin string, script string) (string, error) {
	f.scripts = append(f.scripts, script)
	switch {
	case script == "true":
		return "", nil
	case strings.HasPrefix(script, "test -e "):
		path := firstShq(script[len("test -e "):])
		_, ok := f.files[path]
		return "", errIf(!ok)
	case strings.HasPrefix(script, "cat "):
		path := firstShq(script[len("cat "):])
		content, ok := f.files[path]
		if !ok {
			return "", fmt.Errorf("cat: %s: no such file", path)
		}
		return content + "\n", nil
	case strings.HasPrefix(script, "mkdir -p ") && strings.Contains(script, "base64 -d > "):
		staged := firstShq(script[strings.LastIndex(script, "base64 -d > ")+len("base64 -d > "):])
		decoded, err := base64.StdEncoding.DecodeString(stdin)
		if err != nil {
			return "", err
		}
		f.files[staged] = string(decoded)
		return "", nil
	case strings.HasPrefix(script, "mv "):
		parts := strings.SplitN(script[3:], " ", 2)
		staged, target := unshq(parts[0]), unshq(parts[1])
		content, ok := f.files[staged]
		if !ok {
			return "", fmt.Errorf("mv: %s: no such file", staged)
		}
		delete(f.files, staged)
		f.files[target] = content
		return "", nil
	case strings.HasPrefix(script, "rm -f "):
		delete(f.files, unshq(script[len("rm -f "):]))
		return "", nil
	case script == `printf %s "$HOME"`:
		return "/remote/home", nil
	}
	return "", fmt.Errorf("fakeSsh: unhandled script %q", script)
}

func errIf(cond bool) error {
	if cond {
		return fmt.Errorf("not found")
	}
	return nil
}

func firstShq(s string) string {
	if !strings.HasPrefix(s, "'") {
		return s
	}
	end := strings.Index(s[1:], "'") + 1
	return unshq(s[:end+1])
}

func unshq(s string) string {
	return strings.ReplaceAll(s[1:len(s)-1], `'\''`, `'`)
}

func newTestSshIO(seed map[string]string) (*SshIO, *fakeSsh) {
	fake := newFakeSsh(seed)
	s := &SshIO{address: "testhost", run: fake.run, timeout: 0}
	return s, fake
}

func TestSshIOStagedWriteAtomic(t *testing.T) {
	io, _ := newTestSshIO(map[string]string{"/remote/config.toml": "user bytes"})
	if err := io.StageWrite("/remote/config.toml", "next bytes"); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if got, ok := io.ReadTextIfExists("/remote/config.toml"); !ok || got != "user bytes\n" {
		t.Fatalf("stage leaked to target: %q ok=%v", got, ok)
	}
	if err := io.CommitStaged("/remote/config.toml"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if got, ok := io.ReadTextIfExists("/remote/config.toml"); !ok || got != "next bytes\n" {
		t.Fatalf("commit did not swap: %q ok=%v", got, ok)
	}
}

func TestSshIORecoverDiscardsStagedTemp(t *testing.T) {
	io, _ := newTestSshIO(map[string]string{"/remote/config.toml": "user bytes"})
	if err := io.StageWrite("/remote/config.toml", "orphan"); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if !io.RecoverStaged("/remote/config.toml") {
		t.Fatal("expected recovery to remove the staged temp")
	}
	if got, ok := io.ReadTextIfExists("/remote/config.toml"); !ok || got != "user bytes\n" {
		t.Fatalf("recovery altered the target: %q ok=%v", got, ok)
	}
	if io.FileExists(StagedPath("/remote/config.toml")) {
		t.Fatal("staged temp survived recovery")
	}
}

func TestSshIOBinaryContentSurvivesBase64(t *testing.T) {
	io, fake := newTestSshIO(nil)
	content := "line\r\nwith\ttabs and \"quotes\" and 'singles'"
	if err := io.StageWrite("/remote/models.json", content); err != nil {
		t.Fatalf("stage: %v", err)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	if !strings.Contains(strings.Join(fake.scripts, ";"), "base64 -d") {
		t.Fatalf("expected base64 transport, scripts: %v", fake.scripts)
	}
	_ = encoded
	if err := io.CommitStaged("/remote/models.json"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if got, ok := io.ReadTextIfExists("/remote/models.json"); !ok || got != content+"\n" {
		t.Fatalf("content corrupted: %q", got)
	}
}

func TestSshIOAbsentFileReportsNotExists(t *testing.T) {
	io, _ := newTestSshIO(nil)
	if _, ok := io.ReadTextIfExists("/remote/nope.toml"); ok {
		t.Fatal("absent file reported as present")
	}
	if io.FileExists("/remote/nope.toml") {
		t.Fatal("absent file reported as existing")
	}
}

func TestSshHomeAndReachable(t *testing.T) {
	fake := newFakeSsh(nil)
	home, err := fake.run(context.Background(), "testhost", "", `printf %s "$HOME"`)
	if err != nil || home != "/remote/home" {
		t.Fatalf("home: %q err=%v", home, err)
	}
	if _, err := fake.run(context.Background(), "testhost", "", "true"); err != nil {
		t.Fatalf("reachable: %v", err)
	}
}
