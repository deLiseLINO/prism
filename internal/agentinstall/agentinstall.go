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

var definitions = []Definition{
	{
		Key:       "codex",
		IDs:       []integrations.ID{integrations.Codex},
		Binary:    "codex",
		VerifyArg: "--version",
		Plans: map[string][]Plan{
			"darwin": {
				{Method: "brew-cask", Tool: "brew", Package: "codex"},
				{Method: "npm", Tool: "npm", Package: "@openai/codex"},
				{Method: "script", Tool: "bash", Script: &Script{URL: "https://chatgpt.com/codex/install.sh", Interpreter: "bash", Downloader: true}},
			},
			"linux": {
				{Method: "npm", Tool: "npm", Package: "@openai/codex"},
				{Method: "script", Tool: "bash", Script: &Script{URL: "https://chatgpt.com/codex/install.sh", Interpreter: "bash", Downloader: true}},
			},
		},
		SelfUpdate: []string{"codex", "update"},
	},
	{
		Key:       "claude",
		IDs:       []integrations.ID{integrations.Claude},
		Binary:    "claude",
		VerifyArg: "--version",
		Plans: map[string][]Plan{
			"darwin": {
				{Method: "brew-cask", Tool: "brew", Package: "claude-code"},
				{Method: "npm", Tool: "npm", Package: "@anthropic-ai/claude-code"},
				{Method: "script", Tool: "bash", Script: &Script{URL: "https://claude.ai/install.sh", Interpreter: "bash", Downloader: true}},
			},
			"linux": {
				{Method: "npm", Tool: "npm", Package: "@anthropic-ai/claude-code"},
				{Method: "script", Tool: "bash", Script: &Script{URL: "https://claude.ai/install.sh", Interpreter: "bash", Downloader: true}},
			},
		},
		SelfUpdate: []string{"claude", "update"},
	},
	{
		Key:       "grok",
		IDs:       []integrations.ID{integrations.Grok},
		Binary:    "grok",
		VerifyArg: "--version",
		Plans: map[string][]Plan{
			"darwin": {
				{Method: "script", Tool: "bash", Script: &Script{URL: "https://x.ai/cli/install.sh", Interpreter: "bash", Downloader: true}},
				{Method: "npm", Tool: "npm", Package: "@xai-official/grok"},
			},
			"linux": {
				{Method: "script", Tool: "bash", Script: &Script{URL: "https://x.ai/cli/install.sh", Interpreter: "bash", Downloader: true}},
				{Method: "npm", Tool: "npm", Package: "@xai-official/grok"},
			},
		},
		SelfUpdate: []string{"grok", "update"},
	},
	{
		Key:       "omp",
		IDs:       []integrations.ID{integrations.Omp},
		Binary:    "omp",
		VerifyArg: "--version",
		Plans: map[string][]Plan{
			"darwin": {
				{Method: "bun", Tool: "bun", Package: "@oh-my-pi/pi-coding-agent"},
				{Method: "brew", Tool: "brew", Package: "can1357/tap/omp"},
				{Method: "script", Tool: "sh", Script: &Script{URL: "https://omp.sh/install", Interpreter: "sh", Args: []string{"--binary"}, Requires: []string{"curl"}}},
			},
			"linux": {
				{Method: "bun", Tool: "bun", Package: "@oh-my-pi/pi-coding-agent"},
				{Method: "brew", Tool: "brew", Package: "can1357/tap/omp"},
				{Method: "script", Tool: "sh", Script: &Script{URL: "https://omp.sh/install", Interpreter: "sh", Args: []string{"--binary"}, Requires: []string{"curl"}}},
			},
		},
		SelfUpdate: []string{"omp", "update"},
	},
	{
		Key:       "pi",
		IDs:       []integrations.ID{integrations.Pi},
		Binary:    "pi",
		VerifyArg: "--version",
		Plans: map[string][]Plan{
			"darwin": {
				{Method: "npm", Tool: "npm", Package: "@earendil-works/pi-coding-agent"},
			},
			"linux": {
				{Method: "npm", Tool: "npm", Package: "@earendil-works/pi-coding-agent"},
			},
		},
		SelfUpdate: []string{"pi", "update", "--self"},
	},
	{
		Key:       "opencode",
		IDs:       []integrations.ID{integrations.Opencode},
		Binary:    "opencode",
		VerifyArg: "--version",
		Plans: map[string][]Plan{
			"darwin": {
				{Method: "npm", Tool: "npm", Package: "@opencode/cli", Aliases: []string{"opencode-ai"}},
				{Method: "script", Tool: "bash", Script: &Script{URL: "https://opencode.ai/install", Interpreter: "bash", Requires: []string{"curl"}}},
			},
			"linux": {
				{Method: "npm", Tool: "npm", Package: "@opencode/cli", Aliases: []string{"opencode-ai"}},
				{Method: "script", Tool: "bash", Script: &Script{URL: "https://opencode.ai/install", Interpreter: "bash", Requires: []string{"curl"}}},
			},
		},
		SelfUpdate: nil,
	},
	{
		Key:       "hermes",
		IDs:       []integrations.ID{integrations.Hermes},
		Binary:    "hermes",
		VerifyArg: "--version",
		Plans: map[string][]Plan{
			"darwin": {
				{Method: "script", Tool: "bash", Script: &Script{URL: "https://hermes-agent.nousresearch.com/install.sh", Interpreter: "bash", Args: []string{"--non-interactive"}, Requires: []string{"git", "curl"}}},
			},
			"linux": {
				{Method: "script", Tool: "bash", Script: &Script{URL: "https://hermes-agent.nousresearch.com/install.sh", Interpreter: "bash", Args: []string{"--non-interactive"}, Requires: []string{"git", "curl"}}},
			},
		},
		SelfUpdate: []string{"hermes", "update", "--yes"},
	},
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
