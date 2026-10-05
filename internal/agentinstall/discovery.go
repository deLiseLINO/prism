package agentinstall

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/deLiseLINO/prism/internal/integrations"
)

func installerDirs(env integrations.Env) []string {
	dirs := []string{"/opt/homebrew/bin", "/usr/local/bin"}
	if prefix := env["HOMEBREW_PREFIX"]; filepath.IsAbs(prefix) {
		dirs = append(dirs, filepath.Join(prefix, "bin"))
	}
	home := env["HOME"]
	if !filepath.IsAbs(home) {
		return dirs
	}
	_, bunBin := bunDirectories(env)
	dirs = append(dirs, bunBin, filepath.Join(valueOr(env, "VOLTA_HOME", filepath.Join(home, ".volta")), "bin"), valueOr(env, "PNPM_HOME", filepath.Join(home, "Library", "pnpm")), filepath.Join(home, ".local", "share", "pnpm"), filepath.Join(home, ".local", "share", "pnpm", "bin"), env["NVM_SYMLINK"])
	for _, root := range []string{env["FNM_DIR"], filepath.Join(home, ".fnm"), filepath.Join(home, ".local", "share", "fnm"), filepath.Join(home, "Library", "Application Support", "fnm")} {
		if !filepath.IsAbs(root) {
			continue
		}
		dirs = append(dirs, filepath.Join(root, "aliases", "default", "bin"))
		dirs = append(dirs, versionedBins(filepath.Join(root, "node-versions"))...)
	}
	dirs = append(dirs, versionedBins(filepath.Join(valueOr(env, "NVM_DIR", filepath.Join(home, ".nvm")), "versions", "node"))...)
	return dirs
}

func versionedBins(root string) []string {
	entries, _ := os.ReadDir(root)
	sort.Slice(entries, func(i, j int) bool {
		a, b := normalizedVersion(entries[i].Name()), normalizedVersion(entries[j].Name())
		if a != "" && b != "" && compareVersions(a, b) != 0 {
			return compareVersions(a, b) > 0
		}
		if (a != "") != (b != "") {
			return a != ""
		}
		return entries[i].Name() > entries[j].Name()
	})
	var dirs []string
	for i, entry := range entries {
		if i >= 128 {
			break
		}
		dirs = append(dirs, filepath.Join(root, entry.Name(), "bin"), filepath.Join(root, entry.Name(), "installation", "bin"))
	}
	return dirs
}

func (m *Manager) installerEnv(env integrations.Env) integrations.Env {
	out := copyEnv(env)
	dirs := filepath.SplitList(env["PATH"])
	dirs = append(dirs, installerDirs(env)...)
	out["PATH"] = strings.Join(dirs, string(os.PathListSeparator))
	return out
}

type versionObservation struct {
	real, version string
	mtime         time.Time
	size          int64
}

func (m *Manager) candidateDirs(def Definition) []string {
	dirs := filepath.SplitList(m.env["PATH"])
	home := m.env["HOME"]
	if filepath.IsAbs(home) {
		dirs = append(dirs, filepath.Dir(nativeEntry(def, m.env)), filepath.Join(home, ".local", "bin"), filepath.Join(home, ".npm-global", "bin"))
		switch def.Key {
		case "claude":
			dirs = append(dirs, filepath.Join(home, ".claude", "local"))
		case "codex":
			dirs = append(dirs, filepath.Join(home, ".codex", "bin"))
		case "opencode":
			dirs = append(dirs, filepath.Join(home, ".opencode", "bin"))
		case "grok":
			dirs = append(dirs, filepath.Join(home, ".grok", "bin"))
		case "hermes":
			dirs = append(dirs, filepath.Join(valueOr(m.env, "HERMES_HOME", filepath.Join(home, ".hermes")), "bin"))
		case "pi":
			dirs = append(dirs, filepath.Join(home, ".pi", "agent", "bin"))
		}
		if def.Key == "pi" {
			dirs = append(dirs, piManagedDirs(m.env)...)
		}
	}
	dirs = append(dirs, installerDirs(m.env)...)
	m.mu.Lock()
	dirs = append(dirs, m.destinations[def.Key]...)
	m.mu.Unlock()
	return dirs
}

func executableOverrideKey(def Definition) string {
	if def.Key == "claude" {
		return "CLAUDE_CODE_EXECUTABLE"
	}
	return strings.ToUpper(def.Key) + "_EXECUTABLE"
}

func (m *Manager) discover(def Definition) string {
	if override := m.env[executableOverrideKey(def)]; override != "" {
		return absolute(override)
	}
	var entries []string
	seen := make(map[string]bool)
	for _, dir := range m.candidateDirs(def) {
		if dir == "" {
			continue
		}
		entry := absolute(filepath.Join(dir, def.Binary))
		if !isExecutable(entry, m.stat) {
			continue
		}
		real, err := m.eval(entry)
		if err != nil {
			real = entry
		}
		if seen[real] {
			continue
		}
		seen[real] = true
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		return ""
	}
	best, version := entries[0], ""
	if len(entries) == 1 {
		return best
	}
	for _, entry := range entries {
		got := m.discoveryVersion(def, entry)
		if got != "" && (version == "" || compareVersions(got, version) > 0) {
			best, version = entry, got
		}
	}
	return best
}
func (m *Manager) discoveryVersion(def Definition, entry string) string {
	info, err := m.stat(entry)
	if err != nil {
		return ""
	}
	real, _ := m.eval(entry)
	m.mu.Lock()
	cached, ok := m.versions[entry]
	busy := len(m.active) != 0
	m.mu.Unlock()
	if busy {
		return cached.version
	}
	if ok && cached.real == real && cached.mtime == info.ModTime() && cached.size == info.Size() {
		return cached.version
	}
	out, err := m.readCommand(m.lifetime, m.env, entry, def.VerifyArg)
	if err != nil {
		return ""
	}
	version := parsedVersion(out, false)
	m.mu.Lock()
	m.versions[entry] = versionObservation{real: real, version: version, mtime: info.ModTime(), size: info.Size()}
	m.mu.Unlock()
	return version
}
