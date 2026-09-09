package agentinstall

import (
	"reflect"
	"testing"
)

// TestResolveInstallPlanPerToolAvailability walks every definition × OS ×
// tool-availability combination and asserts the winning plan follows the
// contract matrix: first plan whose Tool resolves, scripts need no tool.
func TestResolveInstallPlanPerToolAvailability(t *testing.T) {
	cases := []struct {
		name        string
		key         string
		goos        string
		tools       []string // tool names present on PATH (in /bin)
		wantOK      bool
		wantMethod  Method
		wantCommand string
	}{
		{name: "codex darwin brew", key: "codex", goos: "darwin", tools: []string{"brew"}, wantOK: true, wantMethod: MethodBrewCask, wantCommand: "brew install --cask codex"},
		{name: "codex darwin npm fallback", key: "codex", goos: "darwin", tools: []string{"npm"}, wantOK: true, wantMethod: MethodNpm, wantCommand: "npm install -g @openai/codex"},
		{name: "codex darwin script last", key: "codex", goos: "darwin", tools: []string{"bash"}, wantOK: true, wantMethod: MethodScript, wantCommand: "bash https://chatgpt.com/codex/install.sh"},
		{name: "codex linux no cask", key: "codex", goos: "linux", tools: []string{"brew", "npm"}, wantOK: true, wantMethod: MethodNpm, wantCommand: "npm install -g @openai/codex"},
		{name: "claude darwin brew", key: "claude", goos: "darwin", tools: []string{"brew"}, wantOK: true, wantMethod: MethodBrewCask, wantCommand: "brew install --cask claude-code"},
		{name: "claude darwin npm", key: "claude", goos: "darwin", tools: []string{"npm"}, wantOK: true, wantMethod: MethodNpm, wantCommand: "npm install -g @anthropic-ai/claude-code"},
		{name: "grok script first", key: "grok", goos: "darwin", tools: []string{"npm", "brew", "bash"}, wantOK: true, wantMethod: MethodScript, wantCommand: "bash https://x.ai/cli/install.sh"},
		{name: "omp darwin bun", key: "omp", goos: "darwin", tools: []string{"bun", "brew", "npm"}, wantOK: true, wantMethod: MethodBun, wantCommand: "bun install -g pi-coding-agent"},
		{name: "omp brew fallback", key: "omp", goos: "darwin", tools: []string{"brew"}, wantOK: true, wantMethod: MethodBrew, wantCommand: "brew install can1357/tap/omp"},
		{name: "omp linux plan keeps brew", key: "omp", goos: "linux", tools: []string{"brew"}, wantOK: true, wantMethod: MethodBrew, wantCommand: "brew install can1357/tap/omp"},
		{name: "pi npm first", key: "pi", goos: "linux", tools: []string{"npm"}, wantOK: true, wantMethod: MethodNpm, wantCommand: "npm install -g @earendil-works/pi-coding-agent"},
		{name: "opencode brew first", key: "opencode", goos: "darwin", tools: []string{"brew", "npm"}, wantOK: true, wantMethod: MethodBrew, wantCommand: "brew install anomalyco/tap/opencode"},
		{name: "opencode npm fallback", key: "opencode", goos: "darwin", tools: []string{"npm"}, wantOK: true, wantMethod: MethodNpm, wantCommand: "npm install -g opencode-ai@latest"},
		{name: "opencode2 npm only tool path", key: "opencode2", goos: "darwin", tools: []string{"npm"}, wantOK: true, wantMethod: MethodNpm, wantCommand: "npm install -g @opencode-ai/cli@beta"},
		{name: "hermes npm", key: "hermes", goos: "linux", tools: []string{"npm"}, wantOK: true, wantMethod: MethodNpm, wantCommand: "npm install -g hermes-agent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var paths []string
			for _, tool := range tc.tools {
				paths = append(paths, "/bin/"+tool)
			}
			env := envAdapter{env: pathEnv("/bin")}
			plan, _, ok := resolveInstallPlan(tc.key, tc.goos, env, statWith(paths...).stat)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if plan.Method != tc.wantMethod {
				t.Errorf("method = %q, want %q", plan.Method, tc.wantMethod)
			}
			if got := plan.Display(); got != tc.wantCommand {
				t.Errorf("display = %q, want %q", got, tc.wantCommand)
			}
		})
	}
}

// TestResolveInstallPlanCasksAbsentOnLinux asserts no linux plan uses a cask.
func TestResolveInstallPlanCasksAbsentOnLinux(t *testing.T) {
	for _, def := range definitions {
		for _, plan := range def.Plans["linux"] {
			if plan.Method == MethodBrewCask {
				t.Errorf("%s linux plan %v: brew-cask must not appear on linux", def.Key, plan.Command)
			}
		}
	}
}

// TestPlanArgvForce asserts force flags append only where honored: npm and
// brew get --force; script plans have no static argv (the manager fetches
// and reruns them), so force does not apply.
func TestPlanArgvForce(t *testing.T) {
	cases := []struct {
		name     string
		plan     Plan
		force    bool
		wantArgv []string
	}{
		{
			name:     "npm force",
			plan:     Plan{Method: MethodNpm, Command: []string{"npm", "install", "-g", "pkg"}, ForceArg: []string{"--force"}},
			force:    true,
			wantArgv: []string{"npm", "install", "-g", "pkg", "--force"},
		},
		{
			name:     "npm plain",
			plan:     Plan{Method: MethodNpm, Command: []string{"npm", "install", "-g", "pkg"}, ForceArg: []string{"--force"}},
			force:    false,
			wantArgv: []string{"npm", "install", "-g", "pkg"},
		},
		{
			name:     "brew cask force",
			plan:     Plan{Method: MethodBrewCask, Command: []string{"brew", "install", "--cask", "codex"}, ForceArg: []string{"--force"}},
			force:    true,
			wantArgv: []string{"brew", "install", "--cask", "codex", "--force"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.plan.argv(tc.force)
			if !reflect.DeepEqual(got, tc.wantArgv) {
				t.Errorf("argv = %v, want %v", got, tc.wantArgv)
			}
		})
	}
}

// TestPlanScriptHasNoStaticArgv: scripts are fetched to a temp file by the
// manager (interpreters cannot open URLs), so argv is empty for them.
func TestPlanScriptHasNoStaticArgv(t *testing.T) {
	plan := Plan{Method: MethodScript, Script: &Script{URL: "https://x.ai/cli/install.sh", Interpreter: "bash"}}
	if got := plan.argv(true); len(got) != 0 {
		t.Errorf("script argv = %v, want empty", got)
	}
	if got := plan.Display(); got != "bash https://x.ai/cli/install.sh" {
		t.Errorf("display = %q, want the readable interpreter URL form", got)
	}
}

// TestEveryDefinitionHasDarwinAndLinuxPlans is the table integrity check.
func TestEveryDefinitionHasDarwinAndLinuxPlans(t *testing.T) {
	for _, def := range definitions {
		if len(def.Plans["darwin"]) == 0 {
			t.Errorf("%s: no darwin plans", def.Key)
		}
		if len(def.Plans["linux"]) == 0 {
			t.Errorf("%s: no linux plans", def.Key)
		}
		if len(def.IDs) == 0 {
			t.Errorf("%s: no integration ids", def.Key)
		}
		if def.Binary == "" || def.VerifyArg == "" {
			t.Errorf("%s: missing binary or verify arg", def.Key)
		}
	}
	// opencode2 deliberately has no self-update argv.
	if def, ok := definition("opencode2"); ok && def.SelfUpdate != nil {
		t.Errorf("opencode2 SelfUpdate must be nil (verified 2026-09-09: no update subcommand)")
	}
}
