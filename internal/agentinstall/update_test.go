package agentinstall

import (
	"os"
	"strings"
	"testing"
)

// TestResolveUpdatePlanMatrix walks (agent, source) pairs and asserts either
// the exact update argv or a named refusal.
func TestResolveUpdatePlanMatrix(t *testing.T) {
	cases := []struct {
		name            string
		key             string
		source          Source
		pnpmOnPath      bool
		wantCommand     string
		wantUnsupported string
	}{
		{name: "codex npm", key: "codex", source: SourceNpm, wantCommand: "npm install -g @openai/codex@latest"},
		{name: "codex brew cask", key: "codex", source: SourceBrew, wantCommand: "brew upgrade --cask codex"},
		{name: "codex script", key: "codex", source: SourceScript, wantCommand: "codex update"},
		{name: "codex unknown", key: "codex", source: SourceUnknown, wantUnsupported: "unrecognized"},
		{name: "claude npm", key: "claude", source: SourceNpm, wantCommand: "npm install -g @anthropic-ai/claude-code@latest"},
		{name: "claude brew cask", key: "claude", source: SourceBrew, wantCommand: "brew upgrade --cask claude-code"},
		{name: "claude script", key: "claude", source: SourceScript, wantCommand: "claude update"},
		{name: "grok script", key: "grok", source: SourceScript, wantCommand: "grok update"},
		{name: "grok npm", key: "grok", source: SourceNpm, wantCommand: "npm install -g @xai-official/grok@latest"},
		{name: "omp bun", key: "omp", source: SourceBun, wantCommand: "bun install -g pi-coding-agent"},
		{name: "omp brew", key: "omp", source: SourceBrew, wantCommand: "brew upgrade can1357/tap/omp"},
		{name: "omp script", key: "omp", source: SourceScript, wantCommand: "omp update"},
		{name: "pi npm", key: "pi", source: SourceNpm, wantCommand: "npm install -g @earendil-works/pi-coding-agent@latest"},
		{name: "pi script", key: "pi", source: SourceScript, wantCommand: "pi update"},
		{name: "opencode brew", key: "opencode", source: SourceBrew, wantCommand: "brew upgrade anomalyco/tap/opencode"},
		{name: "opencode npm", key: "opencode", source: SourceNpm, wantCommand: "npm install -g opencode-ai@latest"},
		{name: "opencode script", key: "opencode", source: SourceScript, wantCommand: "opencode upgrade"},
		{name: "hermes npm", key: "hermes", source: SourceNpm, wantCommand: "npm install -g hermes-agent@latest"},
		{name: "hermes script", key: "hermes", source: SourceScript, wantCommand: "hermes update --yes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, stat := updateEnv(tc.pnpmOnPath)
			plan := resolveUpdatePlan(tc.key, tc.source, env, stat)
			if tc.wantCommand != "" {
				if plan.Unsupported != "" {
					t.Fatalf("unexpected refusal: %s", plan.Unsupported)
				}
				if got := strings.Join(plan.Command, " "); got != tc.wantCommand {
					t.Errorf("command = %q, want %q", got, tc.wantCommand)
				}
				return
			}
			if plan.Command != nil {
				t.Fatalf("unexpected command %v, want refusal", plan.Command)
			}
			if !strings.Contains(plan.Unsupported, tc.wantUnsupported) {
				t.Errorf("refusal = %q, want it to mention %q", plan.Unsupported, tc.wantUnsupported)
			}
		})
	}
}

func updateEnv(pnpmOnPath bool) (integrationsEnv, func(string) (os.FileInfo, error)) {
	if pnpmOnPath {
		return envAdapter{env: pathEnv("/bin")}, statWith("/bin/pnpm").stat
	}
	return envAdapter{env: pathEnv("/bin")}, statWith().stat
}

// TestUpdatePnpmRequiresPnpmOnPath: a pnpm-installed agent updates via pnpm
// only when pnpm itself resolves; otherwise a named refusal suggests npm.
func TestUpdatePnpmRequiresPnpmOnPath(t *testing.T) {
	env, stat := updateEnv(false)
	plan := resolveUpdatePlan("opencode2", SourcePnpm, env, stat)
	if plan.Command != nil {
		t.Fatalf("pnpm absent but command returned: %v", plan.Command)
	}
	if !strings.Contains(plan.Unsupported, "pnpm is not on PATH") {
		t.Errorf("refusal = %q, want PATH mention", plan.Unsupported)
	}
	envOK, statOK := updateEnv(true)
	planOK := resolveUpdatePlan("opencode2", SourcePnpm, envOK, statOK)
	if got := strings.Join(planOK.Command, " "); got != "pnpm add -g @opencode-ai/cli@latest" {
		t.Errorf("command = %q, want pnpm add -g @opencode-ai/cli@latest", got)
	}
}

// TestUpdateOpencode2Fallback: opencode2 has no self-update argv (verified
// live 2026-09-09), so script installs rerun the install script and npm
// installs refresh the package.
func TestUpdateOpencode2Fallback(t *testing.T) {
	env, stat := updateEnv(false)
	script := resolveUpdatePlan("opencode2", SourceScript, env, stat)
	if got := strings.Join(script.Command, " "); got != "bash https://opencode.ai/install" {
		t.Errorf("script source: command = %q, want script rerun", got)
	}
	npm := resolveUpdatePlan("opencode2", SourceNpm, env, stat)
	if got := strings.Join(npm.Command, " "); got != "npm install -g @opencode-ai/cli@latest" {
		t.Errorf("npm source: command = %q, want npm refresh", got)
	}
}

// TestUpdateUnknownSourceRefusalIsHonest: unknown sources refuse with a
// reason, never a fabricated command.
func TestUpdateUnknownSourceRefusalIsHonest(t *testing.T) {
	env, stat := updateEnv(false)
	for _, key := range []string{"codex", "claude", "grok", "omp", "pi", "opencode", "opencode2", "hermes"} {
		plan := resolveUpdatePlan(key, SourceUnknown, env, stat)
		if plan.Command != nil {
			t.Errorf("%s: unknown source produced command %v", key, plan.Command)
		}
		if plan.Unsupported == "" {
			t.Errorf("%s: unknown source refusal is empty", key)
		}
	}
}
