package agentinstall

import (
	"fmt"
	"os"
	"runtime"
)

type UpdatePlan struct {
	Command []string
	// Unsupported carries the refusal reason when no update command applies.
	Unsupported string
}

// resolveUpdatePlan derives the update argv from the (agent, source) matrix.
// Refusals are named, never silent: package-manager sources refresh through
// their own manager, script sources use the binary's self-update, and unknown
// sources honestly refuse (the install is managed outside Prism's knowledge).
func resolveUpdatePlan(key string, source Source, env integrationsEnv, stat func(string) (os.FileInfo, error)) UpdatePlan {
	def, ok := definition(key)
	if !ok {
		return UpdatePlan{Unsupported: "unknown agent " + key}
	}
	switch source {
	case SourceNpm:
		return UpdatePlan{Command: []string{"npm", "install", "-g", npmPackage(def)}}
	case SourceBun:
		return UpdatePlan{Command: []string{"bun", "install", "-g", bunPackage(def)}}
	case SourcePnpm:
		if LookPath(env, "pnpm", stat) == "" {
			return UpdatePlan{Unsupported: "pnpm-installed but pnpm is not on PATH; reinstall via npm or add pnpm"}
		}
		return UpdatePlan{Command: []string{"pnpm", "add", "-g", npmPackage(def)}}
	case SourceBrew:
		if isCask(def) {
			return UpdatePlan{Command: []string{"brew", "upgrade", "--cask", brewName(def)}}
		}
		return UpdatePlan{Command: []string{"brew", "upgrade", brewName(def)}}
	case SourceScript:
		if len(def.SelfUpdate) == 0 {
			return UpdatePlan{Command: scriptRerun(def)}
		}
		return UpdatePlan{Command: append([]string{}, def.SelfUpdate...)}
	default:
		return UpdatePlan{Unsupported: fmt.Sprintf("installed from an unrecognized location; update is managed outside Prism (see %s)", def.DocsURL)}
	}
}

// npmPackage finds the plan package for the agent's npm method; @latest pins
// the refresh semantics.
func npmPackage(def Definition) string {
	for _, plan := range def.Plans[runtime.GOOS] {
		if plan.Method == MethodNpm {
			return plan.Package + "@latest"
		}
	}
	return ""
}

func bunPackage(def Definition) string {
	for _, plan := range def.Plans[runtime.GOOS] {
		if plan.Method == MethodBun {
			return plan.Package
		}
	}
	return ""
}

func brewName(def Definition) string {
	for _, plan := range def.Plans[runtime.GOOS] {
		if plan.Method == MethodBrew || plan.Method == MethodBrewCask {
			return plan.Package
		}
	}
	return ""
}

func isCask(def Definition) bool {
	for _, plan := range def.Plans[runtime.GOOS] {
		if plan.Method == MethodBrewCask {
			return true
		}
	}
	return false
}

// scriptRerun re-resolves the install script so a rerun updates in place;
// opencode2 (no SelfUpdate) lands here for script installs.
func scriptRerun(def Definition) []string {
	for _, plan := range def.Plans[runtime.GOOS] {
		if plan.Script != nil {
			return append([]string{plan.Script.Interpreter}, plan.Script.URL)
		}
	}
	return nil
}
