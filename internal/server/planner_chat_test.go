package server

import (
	"testing"

	"prism/internal/config"
	"prism/internal/provider"
)

func TestWireForChat(t *testing.T) {
	if wireFor(config.WireOpenAIChat) != provider.WireChat {
		t.Fatalf("chat wire not mapped: %d", wireFor(config.WireOpenAIChat))
	}
	if wireFor(config.WireOpenAIResponses) != provider.WireResponses {
		t.Fatal("responses wire mapping changed")
	}
	if wireFor(config.WireAnthropicMessages) != provider.WireMessages {
		t.Fatal("messages wire mapping changed")
	}
	if wireFor(config.WireCodex) != provider.WireCodex {
		t.Fatal("codex wire mapping changed")
	}
	if wireFor(config.WireAntigravity) != provider.WireAntigravity {
		t.Fatal("antigravity wire mapping changed")
	}
}
