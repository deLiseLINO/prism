package agentinstall

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/deLiseLINO/prism/internal/integrations"
)

const userPathTimeout = 3 * time.Second

func WithUserPath(env integrations.Env) integrations.Env {
	if env == nil {
		env = integrations.Env{}
	}
	discovered, err := userPath(context.Background(), env)
	if err != nil || discovered == "" {
		return env
	}
	current := env["PATH"]
	merged := mergePath(discovered, current)
	if merged == current {
		return env
	}
	out := make(integrations.Env, len(env)+1)
	for k, v := range env {
		out[k] = v
	}
	out["PATH"] = merged
	return out
}

func userPath(ctx context.Context, env integrations.Env) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, userPathTimeout)
	defer cancel()
	name, args, err := loginPathCommand(env)
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = loginProbeEnv(env)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return cleanPathOutput(stdout.String()), nil
}

func loginPathCommand(env integrations.Env) (string, []string, error) {
	if runtime.GOOS == "windows" {
		return "powershell.exe", []string{
			"-NoProfile",
			"-NonInteractive",
			"-Command",
			`$expand = { param($s) if (-not $s) { '' } else { [Environment]::ExpandEnvironmentVariables($s) } }; $u = & $expand ([Environment]::GetEnvironmentVariable('Path','User')); $m = & $expand ([Environment]::GetEnvironmentVariable('Path','Machine')); if ($u -and $m) { "$u;$m" } elseif ($u) { $u } else { $m }`,
		}, nil
	}
	shell := userShell(env)
	if shell == "" {
		return "", nil, os.ErrNotExist
	}
	return shell, loginArgs(shell), nil
}

func userShell(env integrations.Env) string {
	if shell := recordedShell(env); isExecutableFile(shell) {
		return shell
	}
	if shell := strings.TrimSpace(envValue(env, "SHELL")); isExecutableFile(shell) {
		return shell
	}
	if runtime.GOOS == "darwin" && isExecutableFile("/bin/zsh") {
		return "/bin/zsh"
	}
	if isExecutableFile("/bin/sh") {
		return "/bin/sh"
	}
	return ""
}

func recordedShell(env integrations.Env) string {
	name := strings.TrimSpace(envValue(env, "USER"))
	if name == "" {
		name = strings.TrimSpace(envValue(env, "LOGNAME"))
	}
	if name == "" {
		current, err := user.Current()
		if err != nil {
			return ""
		}
		name = current.Username
	}
	return lookupPasswdShell(name)
}

func loginArgs(shell string) []string {
	if strings.ToLower(filepath.Base(shell)) == "fish" {
		return []string{"-l", "-c", "string join : $PATH"}
	}
	return []string{"-ilc", `printf %s "$PATH"`}
}

func loginProbeEnv(env integrations.Env) []string {
	out := make([]string, 0, 4)
	for _, key := range []string{"HOME", "USER", "LOGNAME"} {
		if value := envValue(env, key); value != "" {
			out = append(out, key+"="+value)
		}
	}
	if !hasKey(out, "HOME") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			out = append(out, "HOME="+home)
		}
	}
	out = append(out, "TERM=dumb")
	return out
}

func envValue(env integrations.Env, key string) string {
	if value := strings.TrimSpace(env[key]); value != "" {
		return value
	}
	return strings.TrimSpace(os.Getenv(key))
}

func hasKey(pairs []string, key string) bool {
	prefix := key + "="
	for _, pair := range pairs {
		if strings.HasPrefix(pair, prefix) {
			return true
		}
	}
	return false
}

func mergePath(userPath, current string) string {
	sep := string(os.PathListSeparator)
	seen := map[string]bool{}
	var dirs []string
	add := func(list string) {
		for _, dir := range filepath.SplitList(list) {
			dir = strings.TrimSpace(dir)
			if dir == "" || seen[dir] {
				continue
			}
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}
	add(userPath)
	add(current)
	return strings.Join(dirs, sep)
}

func cleanPathOutput(raw string) string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	lines := strings.Split(raw, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if strings.Contains(line, string(os.PathListSeparator)) || looksLikeDir(line) {
			return line
		}
	}
	return ""
}

func looksLikeDir(value string) bool {
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, "~") {
		return true
	}
	return len(value) >= 3 && value[1] == ':' && (value[2] == '\\' || value[2] == '/')
}

func isExecutableFile(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode()&0111 != 0
}
