package integrations

import (
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// sshCommandRunner runs one ssh invocation with stdin; exec.CommandContext is
// the production implementation, tests inject a fake.
type sshCommandRunner func(ctx context.Context, address string, stdin string, script string) (stdout string, err error)

// SshIO is the remote FileIO: every operation is one ssh command against
// hostconfig's address, honoring the user's ~/.ssh/config for keys and
// aliases. Content travels base64-encoded so quoting and binary bytes never
// meet shell escaping. The staged atomic-write contract matches LocalIO:
// stage writes a sibling temp and commit renames it over the target.
type SshIO struct {
	address string
	run     sshCommandRunner
	timeout time.Duration
}

func NewSshIO(address string) *SshIO {
	return &SshIO{address: address, run: runSshCommand, timeout: 15 * time.Second}
}

func (s *SshIO) runScript(ctx context.Context, script string) (string, error) {
	if s.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.timeout)
		defer cancel()
	}
	return s.run(ctx, s.address, "", script)
}

func (s *SshIO) runWithStdin(ctx context.Context, stdin string, script string) (string, error) {
	if s.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.timeout)
		defer cancel()
	}
	return s.run(ctx, s.address, stdin, script)
}

func (s *SshIO) ReadText(path string) (string, bool, error) {
	out, err := s.runScript(context.Background(), readScript(path))
	if err != nil {
		return "", false, err
	}
	if out == "absent\n" {
		return "", false, nil
	}
	if !strings.HasPrefix(out, "present\n") || !strings.HasSuffix(out, "\n") {
		return "", false, fmt.Errorf("ssh read %s: invalid file response", path)
	}
	return strings.TrimSuffix(strings.TrimPrefix(out, "present\n"), "\n"), true, nil
}

func (s *SshIO) FileExists(path string) bool {
	_, err := s.runScript(context.Background(), fmt.Sprintf("test -e %s", shq(path)))
	return err == nil
}

func (s *SshIO) StageWrite(path string, content string) error {
	staged := StagedPath(path)
	script := stageScript(path, staged)
	_, err := s.runWithStdin(context.Background(), base64.StdEncoding.EncodeToString([]byte(content)), script)
	return err
}

func (s *SshIO) CommitStaged(path string) error {
	_, err := s.runScript(context.Background(), fmt.Sprintf("mv %s %s && sync -f %s && sync -f %s", shq(StagedPath(path)), shq(path), shq(path), shq(shDir(path))))
	return err
}

func (s *SshIO) RecoverStaged(path string) bool {
	_, err := s.runScript(context.Background(), fmt.Sprintf("rm -f %s", shq(StagedPath(path))))
	return err == nil
}

func (s *SshIO) Remove(path string) error {
	_, err := s.runScript(context.Background(), fmt.Sprintf("rm -f %s", shq(path)))
	return err
}

func (s *SshIO) RemoveDurable(path string) error {
	_, err := s.runScript(context.Background(), fmt.Sprintf("rm -f %s && sync -f %s", shq(path), shq(shDir(path))))
	return err
}

// SshHome asks the remote shell for its home directory; the daemon needs it to
// resolve client config paths on the host.
func SshHome(ctx context.Context, address string) (string, error) {
	s := &SshIO{address: address, run: runSshCommand, timeout: 15 * time.Second}
	out, err := s.runScript(ctx, `printf %s "$HOME"`)
	if err != nil {
		return "", err
	}
	home := strings.TrimSpace(out)
	if home == "" {
		return "", fmt.Errorf("prism: host %s reported an empty HOME", address)
	}
	return home, nil
}

func runSshCommand(ctx context.Context, address string, stdin string, script string) (string, error) {
	cmd := exec.CommandContext(ctx, "ssh", address, "sh -c "+shq(script))
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("ssh %s: %w: %s", address, err, strings.TrimSpace(string(exit.Stderr)))
		}
		return "", fmt.Errorf("ssh %s: %w", address, err)
	}
	return string(out), nil
}

// SshReachable reports whether one trivial command succeeds against the host.
func SshReachable(ctx context.Context, address string) bool {
	s := &SshIO{address: address, run: runSshCommand, timeout: 10 * time.Second}
	_, err := s.runScript(ctx, "true")
	return err == nil
}

func readScript(path string) string {
	return fmt.Sprintf("p=%s; if detail=$(LC_ALL=C stat -- \"$p\" 2>&1); then printf 'present\\n'; cat -- \"$p\" && printf '\\n'; else case \"$detail\" in *'No such file or directory'*) printf 'absent\\n';; *) printf 'read %%s: %%s\\n' \"$p\" \"$detail\" >&2; exit 1;; esac; fi", shq(path))
}

func stageScript(path string, staged string) string {
	return fmt.Sprintf("umask 077; mkdir -p %s && base64 -d > %s && sync -f %s || exit 1; d=%s; while test -n \"$d\"; do sync -f \"$d\" || exit 1; test \"$d\" = / && break; next=$(dirname -- \"$d\"); test \"$next\" = \"$d\" && break; d=$next; done", shq(shDir(path)), shq(staged), shq(staged), shq(shDir(path)))
}

func shDir(path string) string {
	idx := strings.LastIndex(path, "/")
	if idx <= 0 {
		return "."
	}
	return path[:idx]
}

// shq single-quotes a path for a POSIX shell.
func shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
