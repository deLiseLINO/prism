// Package agentinstall installs and updates the agent binaries Prism
// integrates. Each Definition names one binary identity and an ordered list
// of install plans per OS; the first plan whose PATH precondition resolves
// wins. Install state is derived, never stored: a binary is installed iff it
// resolves on PATH, so a daemon restart re-derives the truth.
package agentinstall

import "prism/internal/integrations"

type Definition struct {
	Key        string
	IDs        []integrations.ID
	Binary     string
	VerifyArg  string
	Plans      map[string][]Plan
	SelfUpdate []string
	DocsURL    string
}

var definitions = []Definition{
	{
		Key:       "codex",
		IDs:       []integrations.ID{integrations.Codex},
		Binary:    "codex",
		VerifyArg: "--version",
		Plans: map[string][]Plan{
			"darwin": {
				{Method: "brew-cask", Tool: "brew", Command: []string{"brew", "install", "--cask", "codex"}, Package: "codex", ForceArg: []string{"--force"}, DocsURL: "https://developers.openai.com/codex/"},
				{Method: "npm", Tool: "npm", Command: []string{"npm", "install", "-g", "@openai/codex"}, Package: "@openai/codex", ForceArg: []string{"--force"}, DocsURL: "https://developers.openai.com/codex/"},
				{Method: "script", Tool: "bash", Script: &Script{URL: "https://chatgpt.com/codex/install.sh", Interpreter: "bash"}, DocsURL: "https://developers.openai.com/codex/"},
			},
			"linux": {
				{Method: "npm", Tool: "npm", Command: []string{"npm", "install", "-g", "@openai/codex"}, Package: "@openai/codex", ForceArg: []string{"--force"}, DocsURL: "https://developers.openai.com/codex/"},
				{Method: "script", Tool: "bash", Script: &Script{URL: "https://chatgpt.com/codex/install.sh", Interpreter: "bash"}, DocsURL: "https://developers.openai.com/codex/"},
			},
		},
		SelfUpdate: []string{"codex", "update"},
		DocsURL:    "https://developers.openai.com/codex/",
	},
	{
		Key:       "claude",
		IDs:       []integrations.ID{integrations.Claude},
		Binary:    "claude",
		VerifyArg: "--version",
		Plans: map[string][]Plan{
			"darwin": {
				{Method: "brew-cask", Tool: "brew", Command: []string{"brew", "install", "--cask", "claude-code"}, Package: "claude-code", ForceArg: []string{"--force"}, DocsURL: "https://docs.anthropic.com/s/claude-code"},
				{Method: "npm", Tool: "npm", Command: []string{"npm", "install", "-g", "@anthropic-ai/claude-code"}, Package: "@anthropic-ai/claude-code", ForceArg: []string{"--force"}, DocsURL: "https://docs.anthropic.com/s/claude-code"},
				{Method: "script", Tool: "bash", Script: &Script{URL: "https://claude.ai/install.sh", Interpreter: "bash"}, DocsURL: "https://docs.anthropic.com/s/claude-code"},
			},
			"linux": {
				{Method: "npm", Tool: "npm", Command: []string{"npm", "install", "-g", "@anthropic-ai/claude-code"}, Package: "@anthropic-ai/claude-code", ForceArg: []string{"--force"}, DocsURL: "https://docs.anthropic.com/s/claude-code"},
				{Method: "script", Tool: "bash", Script: &Script{URL: "https://claude.ai/install.sh", Interpreter: "bash"}, DocsURL: "https://docs.anthropic.com/s/claude-code"},
			},
		},
		SelfUpdate: []string{"claude", "update"},
		DocsURL:    "https://docs.anthropic.com/s/claude-code",
	},
	{
		Key:       "grok",
		IDs:       []integrations.ID{integrations.Grok},
		Binary:    "grok",
		VerifyArg: "--version",
		Plans: map[string][]Plan{
			"darwin": {
				{Method: "script", Tool: "bash", Script: &Script{URL: "https://x.ai/cli/install.sh", Interpreter: "bash"}, DocsURL: "https://x.ai/cli"},
				{Method: "npm", Tool: "npm", Command: []string{"npm", "install", "-g", "@xai-official/grok"}, Package: "@xai-official/grok", ForceArg: []string{"--force"}, DocsURL: "https://x.ai/cli"},
			},
			"linux": {
				{Method: "script", Tool: "bash", Script: &Script{URL: "https://x.ai/cli/install.sh", Interpreter: "bash"}, DocsURL: "https://x.ai/cli"},
				{Method: "npm", Tool: "npm", Command: []string{"npm", "install", "-g", "@xai-official/grok"}, Package: "@xai-official/grok", ForceArg: []string{"--force"}, DocsURL: "https://x.ai/cli"},
			},
		},
		SelfUpdate: []string{"grok", "update"},
		DocsURL:    "https://x.ai/cli",
	},
	{
		Key:       "omp",
		IDs:       []integrations.ID{integrations.Omp},
		Binary:    "omp",
		VerifyArg: "--version",
		Plans: map[string][]Plan{
			"darwin": {
				{Method: "bun", Tool: "bun", Command: []string{"bun", "install", "-g", "pi-coding-agent"}, Package: "pi-coding-agent", ForceArg: []string{"--force"}, DocsURL: "https://omp.sh"},
				{Method: "brew", Tool: "brew", Command: []string{"brew", "install", "can1357/tap/omp"}, Package: "can1357/tap/omp", ForceArg: []string{"--force"}, DocsURL: "https://omp.sh"},
				{Method: "script", Tool: "sh", Script: &Script{URL: "https://omp.sh/install", Interpreter: "sh"}, DocsURL: "https://omp.sh"},
			},
			"linux": {
				{Method: "bun", Tool: "bun", Command: []string{"bun", "install", "-g", "pi-coding-agent"}, Package: "pi-coding-agent", ForceArg: []string{"--force"}, DocsURL: "https://omp.sh"},
				{Method: "brew", Tool: "brew", Command: []string{"brew", "install", "can1357/tap/omp"}, Package: "can1357/tap/omp", ForceArg: []string{"--force"}, DocsURL: "https://omp.sh"},
				{Method: "script", Tool: "sh", Script: &Script{URL: "https://omp.sh/install", Interpreter: "sh"}, DocsURL: "https://omp.sh"},
			},
		},
		SelfUpdate: []string{"omp", "update"},
		DocsURL:    "https://omp.sh",
	},
	{
		Key:       "pi",
		IDs:       []integrations.ID{integrations.Pi},
		Binary:    "pi",
		VerifyArg: "--version",
		// No script plan: pi's installer refuses to run without Node.js
		// 22.19+ and npm, but the script plan is only reachable when npm is
		// absent (the npm plan outranks it), so it can never succeed
		// (verified live 2026-09-09; Agent Orchestrator gates the same
		// installer behind a node/npm capability check).
		Plans: map[string][]Plan{
			"darwin": {
				{Method: "npm", Tool: "npm", Command: []string{"npm", "install", "-g", "@earendil-works/pi-coding-agent"}, Package: "@earendil-works/pi-coding-agent", ForceArg: []string{"--force"}, DocsURL: "https://pi.dev"},
			},
			"linux": {
				{Method: "npm", Tool: "npm", Command: []string{"npm", "install", "-g", "@earendil-works/pi-coding-agent"}, Package: "@earendil-works/pi-coding-agent", ForceArg: []string{"--force"}, DocsURL: "https://pi.dev"},
			},
		},
		SelfUpdate: []string{"pi", "update"},
		DocsURL:    "https://pi.dev",
	},
	{
		Key:       "opencode",
		IDs:       []integrations.ID{integrations.Opencode},
		Binary:    "opencode",
		VerifyArg: "--version",
		Plans: map[string][]Plan{
			"darwin": {
				{Method: "npm", Tool: "npm", Command: []string{"npm", "install", "-g", "@opencode-ai/cli@latest"}, Package: "@opencode-ai/cli", ForceArg: []string{"--force"}, DocsURL: "https://opencode.ai"},
				{Method: "script", Tool: "bash", Script: &Script{URL: "https://opencode.ai/install", Interpreter: "bash"}, DocsURL: "https://opencode.ai"},
			},
			"linux": {
				{Method: "npm", Tool: "npm", Command: []string{"npm", "install", "-g", "@opencode-ai/cli@latest"}, Package: "@opencode-ai/cli", ForceArg: []string{"--force"}, DocsURL: "https://opencode.ai"},
				{Method: "script", Tool: "bash", Script: &Script{URL: "https://opencode.ai/install", Interpreter: "bash"}, DocsURL: "https://opencode.ai"},
			},
		},
		SelfUpdate: nil,
		DocsURL:    "https://opencode.ai",
	},
	{
		Key:       "hermes",
		IDs:       []integrations.ID{integrations.Hermes},
		Binary:    "hermes",
		VerifyArg: "--version",
		Plans: map[string][]Plan{
			"darwin": {
				{Method: "npm", Tool: "npm", Command: []string{"npm", "install", "-g", "hermes-agent"}, Package: "hermes-agent", ForceArg: []string{"--force"}, DocsURL: "https://github.com/nousresearch/hermes-agent"},
				{Method: "script", Tool: "bash", Script: &Script{URL: "https://raw.githubusercontent.com/nousresearch/hermes-agent/refs/heads/main/install.sh", Interpreter: "bash"}, DocsURL: "https://github.com/nousresearch/hermes-agent"},
			},
			"linux": {
				{Method: "npm", Tool: "npm", Command: []string{"npm", "install", "-g", "hermes-agent"}, Package: "hermes-agent", ForceArg: []string{"--force"}, DocsURL: "https://github.com/nousresearch/hermes-agent"},
				{Method: "script", Tool: "bash", Script: &Script{URL: "https://raw.githubusercontent.com/nousresearch/hermes-agent/refs/heads/main/install.sh", Interpreter: "bash"}, DocsURL: "https://github.com/nousresearch/hermes-agent"},
			},
		},
		SelfUpdate: []string{"hermes", "update", "--yes"},
		DocsURL:    "https://github.com/nousresearch/hermes-agent",
	},
}

func definition(key string) (Definition, bool) {
	for _, d := range definitions {
		if d.Key == key {
			return d, true
		}
	}
	return Definition{}, false
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
