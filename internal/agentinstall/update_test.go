package agentinstall

import (
	"os"
	"strings"
	"testing"
)

// TestResolveUpdatePlanMatrix walks (agent, source, goos) pairs and asserts
// either the exact update argv or a named refusal. The OS is explicit so the
// darwin-only cask plans assert identically on every host.
func TestResolveUpdatePlanMatrix(t *testing.T) {
	cases := []struct {
		name            string
		key             string
		source          Source
		goos            string
		pnpmOnPath      bool
		wantCommand     string
		wantUnsupported string
	}{
		{name: "codex npm", key: "codex", source: SourceNpm, goos: "linux", wantCommand: "npm install -g @openai/codex@latest"},
		{name: "codex brew cask", key: "codex", source: SourceBrew, goos: "darwin", wantCommand: "brew upgrade --cask codex"},
		{name: "codex brew on linux refuses", key: "codex", source: SourceBrew, goos: "linux", wantUnsupported: "no brew formula or cask"},
		{name: "codex script", key: "codex", source: SourceScript, goos: "linux", wantCommand: "codex update"},
		{name: "codex unknown", key: "codex", source: SourceUnknown, goos: "linux", wantUnsupported: "unrecognized"},
		{name: "claude npm", key: "claude", source: SourceNpm, goos: "linux", wantCommand: "npm install -g @anthropic-ai/claude-code@latest"},
		{name: "claude brew cask", key: "claude", source: SourceBrew, goos: "darwin", wantCommand: "brew upgrade --cask claude-code"},
		{name: "claude script", key: "claude", source: SourceScript, goos: "linux", wantCommand: "claude update"},
		{name: "grok script", key: "grok", source: SourceScript, goos: "linux", wantCommand: "grok update"},
		{name: "grok npm", key: "grok", source: SourceNpm, goos: "linux", wantCommand: "npm install -g @xai-official/grok@latest"},
		{name: "omp bun", key: "omp", source: SourceBun, goos: "linux", wantCommand: "bun install -g pi-coding-agent"},
		{name: "omp npm misclassified refuses", key: "omp", source: SourceNpm, goos: "linux", wantUnsupported: "no npm package"},
		{name: "omp brew", key: "omp", source: SourceBrew, goos: "linux", wantCommand: "brew upgrade can1357/tap/omp"},
		{name: "omp script", key: "omp", source: SourceScript, goos: "linux", wantCommand: "omp update"},
		{name: "pi npm", key: "pi", source: SourceNpm, goos: "linux", wantCommand: "npm install -g @earendil-works/pi-coding-agent@latest"},
		{name: "pi script", key: "pi", source: SourceScript, goos: "linux", wantCommand: "pi update"},
		{name: "opencode npm", key: "opencode", source: SourceNpm, goos: "linux", wantCommand: "npm install -g @opencode-ai/cli@latest"},
		{name: "hermes npm", key: "hermes", source: SourceNpm, goos: "linux", wantCommand: "npm install -g hermes-agent@latest"},
		{name: "hermes script", key: "hermes", source: SourceScript, goos: "linux", wantCommand: "hermes update --yes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, stat := updateEnv(tc.pnpmOnPath)
			plan := resolveUpdatePlan(tc.key, tc.source, tc.goos, env, stat)
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
	plan := resolveUpdatePlan("opencode", SourcePnpm, "linux", env, stat)
	if plan.Command != nil {
		t.Fatalf("pnpm absent but command returned: %v", plan.Command)
	}
	if !strings.Contains(plan.Unsupported, "pnpm is not on PATH") {
		t.Errorf("refusal = %q, want PATH mention", plan.Unsupported)
	}
	envOK, statOK := updateEnv(true)
	planOK := resolveUpdatePlan("opencode", SourcePnpm, "linux", envOK, statOK)
	if got := strings.Join(planOK.Command, " "); got != "pnpm add -g @opencode-ai/cli@latest" {
		t.Errorf("command = %q, want pnpm add -g @opencode-ai/cli@latest", got)
	}
}

func TestUpdateOpencodeFallback(t *testing.T) {
	env, stat := updateEnv(false)
	script := resolveUpdatePlan("opencode", SourceScript, "linux", env, stat)
	if script.Command != nil {
		t.Errorf("script source: command = %v, want none", script.Command)
	}
	if script.Script == nil || script.Script.Interpreter != "bash" || script.Script.URL != "https://opencode.ai/install" {
		t.Errorf("script source: plan = %+v, want the opencode.ai install script", script)
	}
	npm := resolveUpdatePlan("opencode", SourceNpm, "linux", env, stat)
	if got := strings.Join(npm.Command, " "); got != "npm install -g @opencode-ai/cli@latest" {
		t.Errorf("npm source: command = %q, want npm refresh", got)
	}
}

// TestUpdateManagerlessSourceRefuses: a source whose manager the agent has
// no plan for refuses by name instead of emitting an empty package argv
// (found live 2026-09-09: bun-installed omp refreshed via a literal
// "npm install -g ").
func TestUpdateManagerlessSourceRefuses(t *testing.T) {
	env, stat := updateEnv(false)
	ompNpm := resolveUpdatePlan("omp", SourceNpm, "linux", env, stat)
	if ompNpm.Command != nil || !strings.Contains(ompNpm.Unsupported, "no npm package") {
		t.Errorf("omp npm: %+v, want named refusal", ompNpm)
	}
	codexBun := resolveUpdatePlan("codex", SourceBun, "linux", env, stat)
	if codexBun.Command != nil || !strings.Contains(codexBun.Unsupported, "no bun package") {
		t.Errorf("codex bun: %+v, want named refusal", codexBun)
	}
	ompEnv, ompStat := updateEnv(true)
	ompPnpm := resolveUpdatePlan("omp", SourcePnpm, "linux", ompEnv, ompStat)
	if ompPnpm.Command != nil || !strings.Contains(ompPnpm.Unsupported, "no npm-family package") {
		t.Errorf("omp pnpm: %+v, want named refusal", ompPnpm)
	}
}

// TestUpdateUnknownSourceRefusalIsHonest: unknown sources refuse with a
// reason, never a fabricated command.
func TestUpdateUnknownSourceRefusalIsHonest(t *testing.T) {
	env, stat := updateEnv(false)
	for _, key := range []string{"codex", "claude", "grok", "omp", "pi", "opencode", "hermes"} {
		plan := resolveUpdatePlan(key, SourceUnknown, "linux", env, stat)
		if plan.Command != nil {
			t.Errorf("%s: unknown source produced command %v", key, plan.Command)
		}
		if plan.Unsupported == "" {
			t.Errorf("%s: unknown source refusal is empty", key)
		}
	}
}
