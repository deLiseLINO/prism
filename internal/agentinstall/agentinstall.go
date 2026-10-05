// Package agentinstall installs and updates client binaries integrated by prism.
package agentinstall

import "github.com/deLiseLINO/prism/internal/integrations"

type Definition struct {
	Key        string
	IDs        []integrations.ID
	Binary     string
	VerifyArg  string
	Plans      map[string][]Plan
	SelfUpdate []string
}

func unixPlans(plans ...Plan) map[string][]Plan {
	return map[string][]Plan{"darwin": plans, "linux": plans}
}
func shellPlan(url, shell string) Plan {
	return Plan{Method: MethodScript, Tool: shell, Script: &Script{URL: url, Interpreter: shell, Requires: []string{"curl"}}}
}

var definitions = []Definition{
	{Key: "codex", IDs: []integrations.ID{integrations.Codex}, Binary: "codex", VerifyArg: "--version",
		Plans: unixPlans(shellPlan("https://chatgpt.com/codex/install.sh", "sh"), Plan{Method: MethodNpm, Tool: "npm", Package: "@openai/codex"})},
	{Key: "claude", IDs: []integrations.ID{integrations.Claude}, Binary: "claude", VerifyArg: "--version",
		Plans: unixPlans(shellPlan("https://claude.ai/install.sh", "bash")), SelfUpdate: []string{"claude", "update"}},
	{Key: "grok", IDs: []integrations.ID{integrations.Grok}, Binary: "grok", VerifyArg: "version",
		Plans: unixPlans(shellPlan("https://x.ai/cli/install.sh", "bash"), Plan{Method: MethodNpm, Tool: "npm", Package: "@xai-official/grok"}), SelfUpdate: []string{"grok", "update"}},
	{Key: "omp", IDs: []integrations.ID{integrations.Omp}, Binary: "omp", VerifyArg: "--version",
		Plans: unixPlans(Plan{Method: MethodBun, Tool: "bun", Package: "@oh-my-pi/pi-coding-agent"}, Plan{Method: MethodBrew, Tool: "brew", Package: "can1357/tap/omp"}, Plan{Method: MethodScript, Tool: "sh", Script: &Script{URL: "https://omp.sh/install", Interpreter: "sh", Args: []string{"--binary"}, Requires: []string{"curl"}}}), SelfUpdate: []string{"omp", "update"}},
	{Key: "pi", IDs: []integrations.ID{integrations.Pi}, Binary: "pi", VerifyArg: "--version",
		Plans: unixPlans(shellPlan("https://pi.dev/install.sh", "sh"), Plan{Method: MethodNpm, Tool: "npm", Package: "@earendil-works/pi-coding-agent", IgnoreScripts: true}), SelfUpdate: []string{"pi", "update", "--self"}},
	{Key: "opencode", IDs: []integrations.ID{integrations.Opencode}, Binary: "opencode", VerifyArg: "--version",
		Plans: unixPlans(shellPlan("https://opencode.ai/install", "bash"), Plan{Method: MethodNpm, Tool: "npm", Package: "@opencode/cli", Aliases: []string{"opencode-ai"}}), SelfUpdate: []string{"opencode", "upgrade"}},
	{Key: "hermes", IDs: []integrations.ID{integrations.Hermes}, Binary: "hermes", VerifyArg: "--version",
		Plans: unixPlans(shellPlan("https://hermes-agent.nousresearch.com/install.sh", "bash")), SelfUpdate: []string{"hermes", "update", "--yes"}},
}

func definitionByID(id integrations.ID) (Definition, bool) {
	for _, d := range definitions {
		for _, candidate := range d.IDs {
			if candidate == id {
				return d, true
			}
		}
	}
	return Definition{}, false
}
