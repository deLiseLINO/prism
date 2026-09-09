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

// DetectSource classifies how a resolved binary was installed. Callers pass
// the symlink-followed path: npm/pnpm link prefix/bin/<name> at
// ../lib/node_modules/<pkg>/..., so classification on the link itself would
// miss the package manager entirely.
func DetectSource(path string) Source {
	switch {
	case strings.Contains(path, "node_modules") && (strings.Contains(path, ".pnpm") || strings.Contains(path, "pnpm")):
		return SourcePnpm
	case strings.Contains(path, "/.bun/"):
		// bun's global layout is ~/.bun/install/global/node_modules, so it
		// must beat the plain node_modules (npm) rule right below.
		return SourceBun
	case strings.Contains(path, "node_modules"):
		return SourceNpm
	case strings.Contains(path, "/Cellar/") || strings.Contains(path, "/opt/homebrew/") || strings.Contains(path, "/home/linuxbrew/"):
		return SourceBrew
	case strings.Contains(path, "/.opencode/bin/"),
		strings.Contains(path, "/.local/bin/"),
		strings.Contains(path, "/.grok/downloads/"),
		strings.Contains(path, "/.claude/local"):
		return SourceScript
	case pnpmBinDir(path):
		// pnpm 12+ writes global bins as shim scripts (regular files) into
		// .../pnpm or .../pnpm/bin, so no symlink ever reaches the
		// node_modules layout above.
		return SourcePnpm
	default:
		return SourceUnknown
	}
}

// pnpmBinDir reports whether path sits directly inside a pnpm global bin
// directory (.../pnpm or .../pnpm/bin).
func pnpmBinDir(path string) bool {
	dir := filepath.Clean(filepath.Dir(path))
	return strings.HasSuffix(dir, "/pnpm") || strings.HasSuffix(dir, "/pnpm/bin")
}

// resolveSymlinks follows a binary path to its final target with the injected
// resolver; an unresolvable path is classified as-is rather than guessed.
func resolveSymlinks(path string, eval func(string) (string, error)) string {
	if resolved, err := eval(path); err == nil && resolved != "" {
		return resolved
	}
	return path
}
