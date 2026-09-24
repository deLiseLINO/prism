package integrations

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Env is the injected process environment; a missing key yields "".
type Env map[string]string

func (e Env) get(key string) string {
	v, ok := e[key]
	if !ok {
		return ""
	}
	return v
}

func (e Env) lookup(key string) (string, bool) {
	v, ok := e[key]
	return v, ok
}

// Environ builds an Env from KEY=VALUE pairs (os.Environ shape).
func Environ(pairs []string) Env {
	env := make(Env, len(pairs))
	for _, pair := range pairs {
		k, v, _ := strings.Cut(pair, "=")
		env[k] = v
	}
	return env
}

func CodexHome(env Env, home string) string {
	override := strings.TrimSpace(env.get("CODEX_HOME"))
	if override != "" {
		return override
	}
	return filepath.Join(home, ".codex")
}

func CodexConfigPath(env Env, home string) string {
	return filepath.Join(CodexHome(env, home), "config.toml")
}

func GrokHome(env Env, home string) string {
	override := strings.TrimSpace(env.get("GROK_HOME"))
	if override != "" {
		return override
	}
	return filepath.Join(home, ".grok")
}

func GrokConfigPath(env Env, home string) string {
	return filepath.Join(GrokHome(env, home), "config.toml")
}

var ompProfileRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

func OmpProfile(env Env) (string, error) {
	raw, hasOmp := env.lookup("OMP_PROFILE")
	if !hasOmp {
		raw = env.get("PI_PROFILE")
	}
	profile := strings.TrimSpace(raw)
	if profile == "" || profile == "default" {
		return "", nil
	}
	if profile == "." || profile == ".." || strings.HasSuffix(profile, ".") || !ompProfileRe.MatchString(profile) {
		return "", fmt.Errorf("prism: invalid OMP profile \"%s\"", raw)
	}
	return profile, nil
}

// OmpAgentDir resolves OMP's agent dir with its own env precedence:
// OMP_PROFILE/PI_PROFILE names a profile under `<root>/profiles`,
// PI_CODING_AGENT_DIR is an absolute-or-~ override that a profile shadows, and
// PI_CONFIG_DIR is always a directory name joined to home (even with a leading
// slash), mirroring OMP's own path.join contract.
func OmpAgentDir(env Env, home string) (string, error) {
	profile, err := OmpProfile(env)
	if err != nil {
		return "", err
	}
	if profile == "" {
		codingDir := strings.TrimSpace(env.get("PI_CODING_AGENT_DIR"))
		if codingDir != "" {
			if codingDir == "~" {
				return home, nil
			}
			if strings.HasPrefix(codingDir, "~/") || strings.HasPrefix(codingDir, `~\`) {
				return filepath.Join(home, codingDir[2:]), nil
			}
			if !filepath.IsAbs(codingDir) {
				return "", fmt.Errorf("prism: PI_CODING_AGENT_DIR must be absolute or start with ~")
			}
			return codingDir, nil
		}
	}
	root := ".omp"
	if dir := env.get("PI_CONFIG_DIR"); dir != "" {
		root = dir
	}
	base := filepath.Join(home, root)
	if profile != "" {
		return filepath.Join(base, "profiles", profile, "agent"), nil
	}
	return filepath.Join(base, "agent"), nil
}

// OmpModelsConfigPath prefers models.yml and falls back to models.yaml only when yml is absent.
func OmpModelsConfigPath(agentDir string, exists func(path string) bool) string {
	canonical := filepath.Join(agentDir, "models.yml")
	fallback := filepath.Join(agentDir, "models.yaml")
	if !exists(canonical) && exists(fallback) {
		return fallback
	}
	return canonical
}

func ClaudeConfigDir(env Env, home string) string {
	override := strings.TrimSpace(env.get("CLAUDE_CONFIG_DIR"))
	if override != "" {
		return override
	}
	return filepath.Join(home, ".claude")
}

func ClaudeSettingsPath(env Env, home string) string {
	return filepath.Join(ClaudeConfigDir(env, home), "settings.json")
}

// ClaudeGatewayCachePath is Claude Code's gateway model discovery cache: when
// it exists, the CLI accepts gateway-discovered model ids instead of fataling
// on names missing from its built-in catalog.
func ClaudeGatewayCachePath(env Env, home string) string {
	return filepath.Join(ClaudeConfigDir(env, home), "cache", "gateway-models.json")
}

// PiAgentDir resolves the pi coding agent's directory: PI_CODING_AGENT_DIR
// (absolute or ~-anchored, mirroring the OMP contract pi shares) wins, then
// ~/.pi/agent.
func PiAgentDir(env Env, home string) (string, error) {
	override := strings.TrimSpace(env.get("PI_CODING_AGENT_DIR"))
	if override != "" {
		if override == "~" {
			return home, nil
		}
		if strings.HasPrefix(override, "~/") || strings.HasPrefix(override, `~\`) {
			return filepath.Join(home, override[2:]), nil
		}
		if !filepath.IsAbs(override) {
			return "", fmt.Errorf("prism: PI_CODING_AGENT_DIR must be absolute or start with ~")
		}
		return override, nil
	}
	return filepath.Join(home, ".pi", "agent"), nil
}

func PiConfigPath(env Env, home string) (string, error) {
	agentDir, err := PiAgentDir(env, home)
	if err != nil {
		return "", err
	}
	return filepath.Join(agentDir, "models.json"), nil
}

func XdgConfigHome(env Env, home string) string {
	if dir := strings.TrimSpace(env.get("XDG_CONFIG_HOME")); dir != "" {
		return dir
	}
	return filepath.Join(home, ".config")
}

func OpencodeConfigDir(env Env, home string) string {
	return filepath.Join(XdgConfigHome(env, home), "opencode")
}

func OpencodeConfigPath(env Env, home string) string {
	return filepath.Join(OpencodeConfigDir(env, home), "opencode.json")
}

func HermesHome(env Env, home string) string {
	override := strings.TrimSpace(env.get("HERMES_HOME"))
	if override != "" {
		return override
	}
	return filepath.Join(home, ".hermes")
}

func HermesConfigPath(env Env, home string) string {
	return filepath.Join(HermesHome(env, home), "config.yaml")
}
