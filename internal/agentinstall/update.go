package agentinstall

import (
	"fmt"
	"os"
)

type UpdatePlan struct {
	Command []string
	// Script, when set, reruns the agent's install script; the manager
	// fetches it to a temp file first because interpreters cannot open URLs.
	Script *Script
	// Unsupported carries the refusal reason when no update command applies.
	Unsupported string
}

// resolveUpdatePlan derives the update argv from the (agent, source) matrix
// for the given OS. Refusals are named, never silent: package-manager
// sources refresh through their own manager, script sources use the binary's
// self-update, and unknown or plan-less sources honestly refuse instead of
// emitting an empty package argv (found live 2026-09-09: bun-installed omp
// refreshed via a literal "npm install -g ").
func resolveUpdatePlan(key string, source Source, goos string, env integrationsEnv, stat func(string) (os.FileInfo, error)) UpdatePlan {
	def, ok := definition(key)
	if !ok {
		return UpdatePlan{Unsupported: "unknown agent " + key}
	}
	switch source {
	case SourceNpm:
		pkg := npmPackage(def, goos)
		if pkg == "" {
			return UpdatePlan{Unsupported: fmt.Sprintf("%s has no npm package; this install is not managed by Prism (see %s)", def.Key, def.DocsURL)}
		}
		return UpdatePlan{Command: []string{"npm", "install", "-g", pkg}}
	case SourceBun:
		pkg := bunPackage(def, goos)
		if pkg == "" {
			return UpdatePlan{Unsupported: fmt.Sprintf("%s has no bun package; this install is not managed by Prism (see %s)", def.Key, def.DocsURL)}
		}
		return UpdatePlan{Command: []string{"bun", "install", "-g", pkg}}
	case SourcePnpm:
		if LookPath(env, "pnpm", stat) == "" {
			return UpdatePlan{Unsupported: "pnpm-installed but pnpm is not on PATH; reinstall via npm or add pnpm"}
		}
		pkg := npmPackage(def, goos)
		if pkg == "" {
			return UpdatePlan{Unsupported: fmt.Sprintf("%s has no npm-family package; this install is not managed by Prism (see %s)", def.Key, def.DocsURL)}
		}
		return UpdatePlan{Command: []string{"pnpm", "add", "-g", pkg}}
	case SourceBrew:
		name := brewName(def, goos)
		if name == "" {
			return UpdatePlan{Unsupported: fmt.Sprintf("%s has no brew formula or cask for %s; this install is not managed by Prism (see %s)", def.Key, goos, def.DocsURL)}
		}
		if isCask(def, goos) {
			return UpdatePlan{Command: []string{"brew", "upgrade", "--cask", name}}
		}
		return UpdatePlan{Command: []string{"brew", "upgrade", name}}
	case SourceScript:
		if len(def.SelfUpdate) == 0 {
			if script := scriptPlan(def, goos); script != nil {
				return UpdatePlan{Script: script}
			}
			return UpdatePlan{Unsupported: fmt.Sprintf("no install script to rerun for %s (see %s)", def.Key, def.DocsURL)}
		}
		return UpdatePlan{Command: append([]string{}, def.SelfUpdate...)}
	default:
		return UpdatePlan{Unsupported: fmt.Sprintf("installed from an unrecognized location; update is managed outside Prism (see %s)", def.DocsURL)}
	}
}

// npmPackage finds the plan package for the agent's npm method; @latest pins
// the refresh semantics.
func npmPackage(def Definition, goos string) string {
	for _, plan := range def.Plans[goos] {
		if plan.Method == MethodNpm {
			return plan.Package + "@latest"
		}
	}
	return ""
}

func bunPackage(def Definition, goos string) string {
	for _, plan := range def.Plans[goos] {
		if plan.Method == MethodBun {
			return plan.Package
		}
	}
	return ""
}

func brewName(def Definition, goos string) string {
	for _, plan := range def.Plans[goos] {
		if plan.Method == MethodBrew || plan.Method == MethodBrewCask {
			return plan.Package
		}
	}
	return ""
}

func isCask(def Definition, goos string) bool {
	for _, plan := range def.Plans[goos] {
		if plan.Method == MethodBrewCask {
			return true
		}
	}
	return false
}

// scriptPlan finds the install script whose rerun updates in place;
func scriptPlan(def Definition, goos string) *Script {
	for _, plan := range def.Plans[goos] {
		if plan.Script != nil {
			return plan.Script
		}
	}
	return nil
}
