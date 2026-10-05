package agentinstall

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/deLiseLINO/prism/internal/integrations"
)

type installTarget struct {
	source Source
	entry  string
	root   string
	name   string
	cask   bool
}

type preparedAction struct {
	target installTarget
	method Method
	argv   []string
	script *Script
	env    integrations.Env
}

type installation struct {
	entry  string
	source Source
	target installTarget
	reason string
}

func absolute(path string) string {
	if path == "" {
		return ""
	}
	value, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	value = filepath.Clean(value)
	parent := filepath.Dir(value)
	var suffix []string
	for {
		if resolved, err := filepath.EvalSymlinks(parent); err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Join(resolved, filepath.Base(value))
		}
		next := filepath.Dir(parent)
		if next == parent {
			return value
		}
		suffix = append(suffix, filepath.Base(parent))
		parent = next
	}
}
func inside(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
func hasNodeModules(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == "node_modules" {
			return true
		}
	}
	return false
}
func packageNames(p Plan) []string { return append([]string{p.Package}, p.Aliases...) }
func packagePlans(def Definition) []Plan {
	var out []Plan
	for _, p := range def.Plans[runtime.GOOS] {
		if p.Method == MethodNpm || p.Method == MethodBun {
			out = append(out, p)
		}
	}
	return out
}

func manifestMatches(root, name, binary, real string) bool {
	manifestPath := filepath.Join(root, "package.json")
	info, err := os.Stat(manifestPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 256*1024 {
		return false
	}
	f, err := os.Open(manifestPath)
	if err != nil {
		return false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 256*1024+1))
	if err != nil || len(data) > 256*1024 {
		return false
	}
	var manifest struct {
		Name string          `json:"name"`
		Bin  json.RawMessage `json:"bin"`
	}
	if json.Unmarshal(data, &manifest) != nil || manifest.Name != name {
		return false
	}
	var bin string
	if json.Unmarshal(manifest.Bin, &bin) != nil {
		var bins map[string]string
		if json.Unmarshal(manifest.Bin, &bins) != nil {
			return false
		}
		bin = bins[binary]
	} else if filepath.Base(name) != binary {
		return false
	}
	if bin == "" || filepath.IsAbs(bin) {
		return false
	}
	path := filepath.Join(root, bin)
	if !inside(path, root) {
		return false
	}
	resolved, err := filepath.EvalSymlinks(path)
	return err == nil && absolute(resolved) == real && inside(real, root) && !hasNodeModules(strings.TrimPrefix(real, root+string(filepath.Separator)))
}

func (m *Manager) observe(def Definition) installation {
	entry := absolute(LookPath(asEnv(m.env), def.Binary, m.stat))
	obs := installation{entry: entry}
	if entry == "" {
		obs.reason = "binary " + def.Binary + " not found on PATH"
		return obs
	}
	real, err := m.eval(entry)
	obs.source = DetectSource(entry)
	if err != nil || real == "" {
		obs.reason = "cannot resolve selected binary; maintenance requires a proven installation"
		return obs
	}
	real = absolute(real)
	if source := DetectSource(real); source != SourceUnknown && !(obs.source == SourceBun && source == SourceNpm) {
		obs.source = source
	}
	root, bin := bunDirectories(m.env)
	if filepath.Dir(entry) == bin && inside(real, root) {
		obs.source = SourceBun
	}
	if obs.source == SourcePnpm {
		obs.reason = "pnpm installation requires manual maintenance; global destination is unproven"
		return obs
	}
	for _, p := range packagePlans(def) {
		for _, name := range packageNames(p) {
			suffix := filepath.Join("lib", "node_modules", filepath.FromSlash(name)) + string(filepath.Separator)
			idx := strings.Index(real, string(filepath.Separator)+suffix)
			if idx >= 0 && obs.source != SourceBun {
				prefix := real[:idx]
				pkgRoot := filepath.Join(prefix, "lib", "node_modules", filepath.FromSlash(name))
				if !hasNodeModules(prefix) && entry == filepath.Join(prefix, "bin", def.Binary) && manifestMatches(pkgRoot, name, def.Binary, real) {
					obs.source = SourceNpm
					obs.target = installTarget{source: SourceNpm, entry: entry, root: prefix, name: name}
					return obs
				}
			}
			pkgRoot := filepath.Join(root, "node_modules", filepath.FromSlash(name))
			if entry == filepath.Join(bin, def.Binary) && inside(real, pkgRoot) && !hasNodeModules(root) && manifestMatches(pkgRoot, name, def.Binary, real) {
				obs.source = SourceBun
				obs.target = installTarget{source: SourceBun, entry: entry, root: root, name: name}
				return obs
			}
		}
	}
	if obs.source == SourceNpm || obs.source == SourceBun {
		obs.reason = "selected package entry is not a proven global installation (root, package, or bin mismatch)"
		return obs
	}
	for _, kind := range []string{"Cellar", "Caskroom"} {
		marker := string(filepath.Separator) + kind + string(filepath.Separator)
		idx := strings.Index(real, marker)
		if idx < 0 {
			continue
		}
		prefix := real[:idx]
		parts := strings.Split(real[idx+len(marker):], string(filepath.Separator))
		if len(parts) < 3 || parts[0] == "" || parts[1] == "" {
			break
		}
		for _, p := range def.Plans[runtime.GOOS] {
			if (p.Method == MethodBrew || p.Method == MethodBrewCask) && filepath.Base(p.Package) == parts[0] && (p.Method == MethodBrewCask) == (kind == "Caskroom") && entry == filepath.Join(prefix, "bin", def.Binary) {
				obs.source = SourceBrew
				obs.target = installTarget{source: SourceBrew, entry: entry, root: prefix, name: parts[0], cask: kind == "Caskroom"}
				return obs
			}
		}
		obs.reason = "selected Homebrew installation has an unrecognized package or receipt layout"
		return obs
	}
	if target, ok := nativeTarget(def, m.env, entry, real); ok {
		obs.source = SourceScript
		obs.target = target
		return obs
	}
	obs.reason = "selected binary has no proven maintenance owner; update and reinstall must be managed manually"
	return obs
}

func valueOr(env integrations.Env, key, fallback string) string {
	if env[key] != "" {
		return env[key]
	}
	return fallback
}
func bunDirectories(env integrations.Env) (string, string) {
	base := filepath.Join(valueOr(env, "XDG_CACHE_HOME", env["HOME"]), ".bun")
	base = valueOr(env, "BUN_INSTALL", base)
	root := valueOr(env, "BUN_INSTALL_GLOBAL_DIR", filepath.Join(base, "install", "global"))
	bin := valueOr(env, "BUN_INSTALL_BIN", filepath.Join(base, "bin"))
	if !filepath.IsAbs(root) || !filepath.IsAbs(bin) {
		return "", ""
	}
	return absolute(root), absolute(bin)
}
func nativeEntry(def Definition, env integrations.Env) string {
	home := env["HOME"]
	switch def.Key {
	case "codex":
		return absolute(filepath.Join(valueOr(env, "CODEX_INSTALL_DIR", filepath.Join(home, ".local", "bin")), def.Binary))
	case "omp":
		return absolute(filepath.Join(valueOr(env, "PI_INSTALL_DIR", filepath.Join(home, ".local", "bin")), def.Binary))
	case "grok":
		bin := absolute(valueOr(env, "GROK_BIN_DIR", filepath.Join(home, ".grok", "bin")))
		local := absolute(filepath.Join(home, ".local", "bin"))
		localVisible := false
		for _, dir := range filepath.SplitList(env["PATH"]) {
			if absolute(dir) == bin {
				return filepath.Join(bin, def.Binary)
			}
			if absolute(dir) == local {
				localVisible = true
			}
		}
		if localVisible {
			return filepath.Join(local, def.Binary)
		}
		return filepath.Join(bin, def.Binary)
	case "opencode":
		return absolute(filepath.Join(home, ".opencode", "bin", def.Binary))
	default:
		return absolute(filepath.Join(home, ".local", "bin", def.Binary))
	}
}
func nativeTarget(def Definition, env integrations.Env, entry, real string) (installTarget, bool) {
	if !filepath.IsAbs(env["HOME"]) {
		return installTarget{}, false
	}
	expected := nativeEntry(def, env)
	home := absolute(env["HOME"])
	root := filepath.Dir(expected)
	valid := entry == expected && real == entry
	switch def.Key {
	case "claude":
		versions := filepath.Join(home, ".local", "share", "claude", "versions")
		valid = entry == expected && (real == entry || inside(real, versions))
		root = versions
	case "codex":
		store := absolute(valueOr(env, "CODEX_HOME", filepath.Join(home, ".codex")))
		valid = entry == expected && (real == entry || inside(real, store))
		root = filepath.Dir(expected)
	case "grok":
		bin := absolute(valueOr(env, "GROK_BIN_DIR", filepath.Join(home, ".grok", "bin")))
		valid = (entry == filepath.Join(bin, def.Binary) || entry == filepath.Join(home, ".local", "bin", def.Binary)) && (real == filepath.Join(bin, def.Binary) || inside(real, filepath.Join(home, ".grok", "downloads")))
		root = bin
	}
	return installTarget{source: SourceScript, entry: entry, root: root, name: def.Key}, valid
}

func missingTools(plan Plan, env integrationsEnv, stat func(string) (os.FileInfo, error)) string {
	tools := []string{plan.Tool}
	if plan.Method == MethodNpm {
		tools = append(tools, "node")
	}
	if plan.Script != nil {
		tools = append(tools, plan.Script.Interpreter)
		tools = append(tools, plan.Script.Requires...)
		if plan.Script.Downloader && LookPath(env, "curl", stat) == "" && LookPath(env, "wget", stat) == "" {
			return "curl or wget is required"
		}
	}
	for _, tool := range tools {
		if tool != "" && LookPath(env, tool, stat) == "" {
			return tool + " is required on PATH"
		}
	}
	return ""
}
func (m *Manager) readCommand(ctx context.Context, env integrations.Env, argv ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out := &boundedOutput{limit: outputTailCap}
	diagnostics := &boundedOutput{limit: outputTailCap}
	if err := m.runner.Run(ctx, env, argv, out, diagnostics); err != nil {
		if detail := strings.TrimSpace(diagnostics.String()); detail != "" {
			return "", fmt.Errorf("%w: %s", err, detail)
		}
		return "", err
	}
	if out.overflow {
		return "", fmt.Errorf("probe output exceeds limit")
	}
	return strings.TrimSpace(out.String()), nil
}
func brewCandidates(prefix string, env integrations.Env, stat func(string) (os.FileInfo, error)) []string {
	var candidates []string
	for _, tool := range []string{filepath.Join(prefix, "bin", "brew"), LookPath(asEnv(env), "brew", stat)} {
		if tool == "" || !isExecutable(tool, stat) {
			continue
		}
		tool = absolute(tool)
		duplicate := false
		for _, candidate := range candidates {
			if candidate == tool {
				duplicate = true
				break
			}
		}
		if !duplicate {
			candidates = append(candidates, tool)
		}
	}
	return candidates
}
func (m *Manager) brewTool(ctx context.Context, prefix string) (string, error) {
	for _, tool := range brewCandidates(prefix, m.env, m.stat) {
		got, err := m.readCommand(ctx, m.env, tool, "--prefix")
		if err == nil && filepath.IsAbs(got) && absolute(got) == prefix {
			return tool, nil
		}
	}
	return "", fmt.Errorf("Homebrew executable must prove selected prefix %s", prefix)
}
func destinationVisible(entry string, env integrations.Env) bool {
	if env["PATH"] == "" {
		return false
	}
	parent := filepath.Dir(absolute(entry))
	for _, dir := range filepath.SplitList(env["PATH"]) {
		if dir == "" {
			dir = "."
		}
		if filepath.Dir(absolute(filepath.Join(dir, filepath.Base(entry)))) == parent {
			return true
		}
	}
	return false
}
func (m *Manager) prepare(ctx context.Context, def Definition, obs installation, op string, force bool) (preparedAction, error) {
	if obs.entry != "" {
		if obs.reason != "" {
			return preparedAction{}, fmt.Errorf("%s", obs.reason)
		}
		return m.prepareTarget(ctx, def, obs.target, op, force)
	}
	if op == "update" {
		return preparedAction{}, ErrNotInstalled
	}
	var reasons []string
	for _, plan := range def.Plans[runtime.GOOS] {
		if reason := missingTools(plan, asEnv(m.env), m.stat); reason != "" {
			reasons = append(reasons, reason)
			continue
		}
		target := installTarget{entry: "", name: plan.Package}
		switch plan.Method {
		case MethodNpm:
			prefix, err := m.readCommand(ctx, m.env, absolute(LookPath(asEnv(m.env), plan.Tool, m.stat)), "prefix", "-g")
			if err != nil || !filepath.IsAbs(prefix) || hasNodeModules(prefix) {
				reasons = append(reasons, "npm global prefix could not be proven")
				continue
			}
			target.source = SourceNpm
			target.root = absolute(prefix)
			target.entry = filepath.Join(target.root, "bin", def.Binary)
		case MethodBun:
			root, bin := bunDirectories(m.env)
			if root == "" || bin == "" || !filepath.IsAbs(m.env["HOME"]) || hasNodeModules(root) {
				reasons = append(reasons, "Bun global directories could not be proven")
				continue
			}
			target = installTarget{source: SourceBun, entry: filepath.Join(bin, def.Binary), root: root, name: plan.Package}
		case MethodBrew, MethodBrewCask:
			prefix, err := m.readCommand(ctx, m.env, absolute(LookPath(asEnv(m.env), plan.Tool, m.stat)), "--prefix")
			if err != nil || !filepath.IsAbs(prefix) {
				reasons = append(reasons, "Homebrew prefix could not be proven")
				continue
			}
			target = installTarget{source: SourceBrew, entry: filepath.Join(absolute(prefix), "bin", def.Binary), root: absolute(prefix), name: filepath.Base(plan.Package), cask: plan.Method == MethodBrewCask}
		case MethodScript:
			if !filepath.IsAbs(m.env["HOME"]) {
				reasons = append(reasons, "absolute HOME is required for script installation")
				continue
			}
			entry := nativeEntry(def, m.env)
			real := entry
			if def.Key == "grok" {
				real = filepath.Join(absolute(valueOr(m.env, "GROK_BIN_DIR", filepath.Join(m.env["HOME"], ".grok", "bin"))), def.Binary)
			}
			target, _ = nativeTarget(def, m.env, entry, real)
		default:
			continue
		}
		if _, err := m.stat(target.entry); err == nil {
			return preparedAction{}, fmt.Errorf("planned destination %s already exists outside selected PATH", target.entry)
		}
		if !destinationVisible(target.entry, m.env) {
			reasons = append(reasons, fmt.Sprintf("planned destination %s is not visible on PATH", target.entry))
			continue
		}
		action, err := m.prepareTarget(ctx, def, target, "install", force)
		if err == nil {
			return action, nil
		}
		reasons = append(reasons, err.Error())
	}
	return preparedAction{}, fmt.Errorf("no install plan is executable: %s", strings.Join(reasons, "; "))
}
func targetCapability(def Definition, target installTarget, env integrations.Env, stat func(string) (os.FileInfo, error), op string) error {
	switch target.source {
	case SourceNpm, SourceBun:
		plan := Plan{Method: MethodNpm, Tool: "npm"}
		if target.source == SourceBun {
			plan = Plan{Method: MethodBun, Tool: "bun"}
		}
		if reason := missingTools(plan, asEnv(env), stat); reason != "" {
			return fmt.Errorf("%s", reason)
		}
	case SourceBrew:
		if !isExecutable(filepath.Join(target.root, "bin", "brew"), stat) {
			return fmt.Errorf("Homebrew executable is required at selected prefix %s", target.root)
		}
	case SourceScript:
		if op == "update" {
			if len(def.SelfUpdate) == 0 {
				return fmt.Errorf("%s native maintenance is manual; installer generation cannot be proven", def.Key)
			}
			return nil
		}
		if def.Key == "opencode" && isExecutable(target.entry, stat) {
			return fmt.Errorf("opencode native maintenance is manual; installer generation cannot be proven")
		}
		if def.Key == "hermes" && isExecutable(target.entry, stat) {
			return fmt.Errorf("hermes reinstall requires manual maintenance; launcher source directory cannot be proven")
		}
		for _, p := range def.Plans[runtime.GOOS] {
			if p.Script != nil {
				if reason := missingTools(p, asEnv(env), stat); reason != "" {
					return fmt.Errorf("%s", reason)
				}
				return nil
			}
		}
		return fmt.Errorf("no validated reinstall script for %s", def.Key)
	default:
		return fmt.Errorf("selected installation requires manual maintenance")
	}
	return nil
}
func (m *Manager) prepareTarget(ctx context.Context, def Definition, target installTarget, op string, force bool) (preparedAction, error) {
	action := preparedAction{target: target, env: m.env}
	if err := targetCapability(def, target, m.env, m.stat, op); err != nil {
		return action, err
	}
	switch target.source {
	case SourceNpm, SourceBun:
		method := MethodNpm
		tool := "npm"
		if target.source == SourceBun {
			method = MethodBun
			tool = "bun"
		}
		action.method = method
		action.argv = []string{absolute(LookPath(asEnv(m.env), tool, m.stat)), "install", "-g"}
		if method == MethodNpm {
			action.argv = append(action.argv, "--prefix", target.root)
		} else {
			action.env = copyEnv(m.env)
			action.env["BUN_INSTALL_GLOBAL_DIR"] = target.root
			action.env["BUN_INSTALL_BIN"] = filepath.Dir(target.entry)
		}
		action.argv = append(action.argv, target.name+"@latest")
		if force && op == "install" {
			action.argv = append(action.argv, "--force")
		}
	case SourceBrew:
		tool, err := m.brewTool(ctx, target.root)
		if err != nil {
			return action, err
		}
		action.method = MethodBrew
		if target.cask {
			action.method = MethodBrewCask
		}
		verb := "upgrade"
		if op == "install" {
			verb = "reinstall"
			if _, err := m.stat(target.entry); os.IsNotExist(err) {
				verb = "install"
			}
		}
		action.argv = []string{tool, verb}
		if target.cask {
			action.argv = append(action.argv, "--cask")
		}
		name := target.name
		if verb == "install" {
			for _, p := range def.Plans[runtime.GOOS] {
				if p.Method == action.method && filepath.Base(p.Package) == name {
					name = p.Package
					break
				}
			}
		}
		action.argv = append(action.argv, name)
	case SourceScript:
		action.method = MethodScript
		if op == "update" {
			action.argv = append([]string{target.entry}, def.SelfUpdate[1:]...)
		} else {
			for _, p := range def.Plans[runtime.GOOS] {
				if p.Script != nil {
					script := *p.Script
					script.Interpreter = absolute(LookPath(asEnv(m.env), script.Interpreter, m.stat))
					action.script = &script
					break
				}
			}
		}
	default:
		return action, fmt.Errorf("selected installation requires manual maintenance")
	}
	return action, nil
}
func copyEnv(env integrations.Env) integrations.Env {
	out := make(integrations.Env, len(env))
	for k, v := range env {
		out[k] = v
	}
	return out
}
