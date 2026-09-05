package integrations

import (
	"strings"
	"testing"
)

const userYAML = "theme: dark\n" +
	"providers:\n" +
	"  openai:\n" +
	"    baseUrl: https://api.openai.com/v1\n" +
	"    api: openai-completions\n" +
	"    apiKey: sk-user\n" +
	"    models: []\n" +
	"\n"

func testModels() []Model {
	return []Model{{ID: "gpt-5.2", Name: "GPT-5.2"}}
}

func TestYamlLeafInsertIntoExistingProviders(t *testing.T) {
	res := UpsertProviderLeaf(userYAML, "prism", NewOmpSpec(testPort, testModels()))
	if res.Kind != "written" || !res.Changed {
		t.Fatalf("expected written+changed, got %+v", res)
	}
	if !strings.Contains(res.Next, "theme: dark") {
		t.Fatalf("user bytes lost:\n%s", res.Next)
	}
	if !strings.Contains(res.Next, "openai:") {
		t.Fatalf("sibling provider lost:\n%s", res.Next)
	}
}

func TestYamlLeafIdempotent(t *testing.T) {
	first := UpsertProviderLeaf(userYAML, "prism", NewOmpSpec(testPort, testModels()))
	if first.Kind != "written" {
		t.Fatalf("expected written, got %+v", first)
	}
	second := UpsertProviderLeaf(first.Next, "prism", NewOmpSpec(testPort, testModels()))
	if second.Kind != "written" || second.Changed {
		t.Fatalf("expected no change, got %+v", second)
	}
}

func TestYamlLeafRewritesOwnRegion(t *testing.T) {
	first := UpsertProviderLeaf(userYAML, "prism", NewOmpSpec(testPort, testModels()))
	edited := strings.Replace(first.Next, "baseUrl: http://127.0.0.1:8787/v1", "baseUrl: http://mutated", 1)
	res := UpsertProviderLeaf(edited, "prism", NewOmpSpec(testPort, testModels()))
	if res.Kind != "written" || !res.Changed {
		t.Fatalf("expected rewrite-in-place, got %+v", res)
	}
	if !strings.Contains(res.Next, "theme: dark") || !strings.Contains(res.Next, "apiKey: sk-user") || strings.Contains(res.Next, "mutated") {
		t.Fatalf("rewrite lost user bytes or kept foreign edit:\n%s", res.Next)
	}
	again := UpsertProviderLeaf(res.Next, "prism", NewOmpSpec(testPort, testModels()))
	if again.Changed {
		t.Fatal("rewrite is not idempotent")
	}
}

func TestYamlLeafRefusesFlow(t *testing.T) {
	res := UpsertProviderLeaf("providers: {}\n", "prism", NewOmpSpec(testPort, testModels()))
	if res.Kind != "refused" || !strings.Contains(res.Reason, "flow-style value") {
		t.Fatalf("expected flow refusal, got %+v", res)
	}
}

func TestYamlLeafRefusesTabs(t *testing.T) {
	res := UpsertProviderLeaf("providers:\n\topenai:\n\t\t models: []\n", "prism", NewOmpSpec(testPort, testModels()))
	if res.Kind != "refused" || !strings.Contains(res.Reason, "tab indentation") {
		t.Fatalf("expected tab refusal, got %+v", res)
	}
}

func TestYamlLeafRefusesDuplicateTop(t *testing.T) {
	src := "providers:\n  a: {}\n---\nproviders:\n  b: {}\n"
	res := UpsertProviderLeaf(src, "prism", NewOmpSpec(testPort, testModels()))
	if res.Kind != "refused" || !strings.Contains(res.Reason, "duplicate") {
		t.Fatalf("expected duplicate refusal, got %+v", res)
	}
}

func TestYamlLeafRemovePrunesEmptyContainer(t *testing.T) {
	res := UpsertProviderLeaf("", "prism", NewOmpSpec(testPort, testModels()))
	if res.Kind != "written" {
		t.Fatalf("expected written, got %+v", res)
	}
	strip := RemoveProviderLeaf(res.Next, "prism")
	if strip.Kind != "written" {
		t.Fatalf("expected written remove, got %+v", strip)
	}
	if strip.Next != "" {
		t.Fatalf("expected empty after rollback of fresh doc, got %q", strip.Next)
	}
}

func TestYamlLeafReadEndpoint(t *testing.T) {
	res := UpsertProviderLeaf(userYAML, "prism", NewOmpSpec(testPort, testModels()))
	if res.Kind != "written" {
		t.Fatalf("expected written, got %+v", res)
	}
	read := ReadProviderLeaf(res.Next, "prism")
	if read.Kind != "present" || read.BaseURL == nil || *read.BaseURL != "http://127.0.0.1:8787/v1" {
		t.Fatalf("expected present+url, got %+v", read)
	}
	missing := ReadProviderLeaf("providers:\n  openai: {}\n", "prism")
	if missing.Kind != "absent" {
		t.Fatalf("expected absent, got %+v", missing)
	}
}
