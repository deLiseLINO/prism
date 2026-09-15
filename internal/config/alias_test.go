package config

import (
	"errors"
	"testing"
)

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"Grok Build CLI":  "grok-build-cli",
		"  OpenAI  ":      "openai",
		"a__b--c":         "a-b-c",
		"Ünïcode Codé":    "n-code-cod",
		"---":             "",
		"Codex (2026) v1": "codex-2026-v1",
	}
	for in, want := range cases {
		if got := Sanitize(in); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClaudeAliasRoundTrip(t *testing.T) {
	a, err := ClaudeAlias("Codex Main", "gpt-5.2 codex")
	if err != nil {
		t.Fatal(err)
	}
	if a != "claude-codex-main--gpt-5-2-codex" {
		t.Fatalf("got %q", a)
	}
	provider, model, err := ParseClaudeAlias(a)
	if err != nil {
		t.Fatal(err)
	}
	a2, err := ClaudeAlias(provider, model)
	if err != nil {
		t.Fatal(err)
	}
	if a2 != a {
		t.Fatalf("round trip unstable: %q vs %q", a, a2)
	}
}

func TestAliasBuildersRejectEmptySanitizedParts(t *testing.T) {
	if _, err := ClaudeAlias("---", "model"); !errors.Is(err, ErrMalformedAlias) {
		t.Fatalf("want ErrMalformedAlias, got %v", err)
	}
	if _, err := ClaudeAlias("provider", "---"); !errors.Is(err, ErrMalformedAlias) {
		t.Fatalf("want ErrMalformedAlias, got %v", err)
	}
}

func TestAliasParsersRejectForeignFamilies(t *testing.T) {
	if _, _, err := ParseClaudeAlias("x-p"); !errors.Is(err, ErrMalformedAlias) {
		t.Fatalf("want ErrMalformedAlias, got %v", err)
	}
	if _, _, err := ParseClaudeAlias("claude-pmodel"); !errors.Is(err, ErrMalformedAlias) {
		t.Fatalf("want ErrMalformedAlias, got %v", err)
	}
}
