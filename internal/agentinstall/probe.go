package agentinstall

import (
	"os"
	"path/filepath"
	"strings"

	"prism/internal/integrations"
)

// integrationsEnv is the seam the package probes PATH through; the concrete
// manager passes integrations.Env via envAdapter.
type integrationsEnv interface {
	get(key string) string
}

type envAdapter struct{ env integrations.Env }

func (a envAdapter) get(key string) string { return a.env[key] }

// LookPath resolves name against env's PATH using the injected stat; only
// regular executable files count, in PATH order. Empty string means absent.
func LookPath(env integrationsEnv, name string, stat func(string) (os.FileInfo, error)) string {
	if name == "" {
		return ""
	}
	if strings.ContainsRune(name, '/') {
		if isExecutable(name, stat) {
			return name
		}
		return ""
	}
	path := env.get("PATH")
	if path == "" {
		return ""
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			dir = "."
		}
		if candidate := filepath.Join(dir, name); isExecutable(candidate, stat) {
			return candidate
		}
	}
	return ""
}

func isExecutable(path string, stat func(string) (os.FileInfo, error)) bool {
	info, err := stat(path)
	if err != nil {
		return false
	}
	if info.IsDir() {
		return false
	}
	return info.Mode()&0111 != 0
}

// Source classifies how a resolved binary was installed.
type Source string

const (
	SourceNpm     Source = "npm"
	SourcePnpm    Source = "pnpm"
	SourceBun     Source = "bun"
	SourceBrew    Source = "brew"
	SourceScript  Source = "script"
	SourceUnknown Source = "unknown"
)

// DetectSource classifies a resolved, symlink-followed binary path.
func DetectSource(path string) Source {
	switch {
	case strings.Contains(path, "node_modules") && (strings.Contains(path, ".pnpm") || strings.Contains(path, "pnpm")):
		return SourcePnpm
	case strings.Contains(path, "node_modules"):
		return SourceNpm
	case strings.Contains(path, "/.bun/"):
		return SourceBun
	case strings.Contains(path, "/Cellar/") || strings.Contains(path, "/opt/homebrew/") || strings.Contains(path, "/home/linuxbrew/"):
		return SourceBrew
	case strings.Contains(path, "/.opencode/bin/"),
		strings.Contains(path, "/.local/bin/"),
		strings.Contains(path, "/.grok/downloads/"),
		strings.Contains(path, "/.claude/local"):
		return SourceScript
	default:
		return SourceUnknown
	}
}
