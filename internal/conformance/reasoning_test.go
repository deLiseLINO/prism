package conformance_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"prism/internal/canon"
	"prism/internal/conformance"
	"prism/internal/conformance/codec/chat"
	"prism/internal/conformance/codec/responses"
)

func reasoningFixturePath() string {
	return filepath.Join("..", "..", "cmd", "prism-wirecheck", "fixtures", "protocol-v1-cases.json")
}

func reasoningCase(t *testing.T, id string) conformance.Case {
	t.Helper()
	a, err := conformance.Load(reasoningFixturePath())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range a.Cases {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("case %s not found", id)
	return conformance.Case{}
}

func TestReasoningCoreSuitePasses(t *testing.T) {
	r := conformance.NewRoutedRunner(chat.Builder{}, responses.Builder{})
	for _, id := range []string{
		"reasoning-core.protocol.effort-mapping",
		"reasoning-core.protocol.summary-stream",
		"reasoning-core.protocol.replay",
		"reasoning-core.protocol.private-content-isolation",
	} {
		c := reasoningCase(t, id)
		res := r.Run(context.Background(), c, conformance.Options{})
		if !res.Passed {
			t.Fatalf("%s failed: classification=%s secondary=%s diag=%v", id, res.Classification, res.SecondaryCode, res.Diagnostics)
		}
		for _, a := range res.AssertionResults {
			if a.Required && !a.Passed {
				t.Fatalf("%s assertion %s failed: %s", id, a.ID, a.Reason)
			}
		}
	}
}

func TestEffortMappingGatewayObjectWire(t *testing.T) {
	req := canon.Request{
		Model:  "fixture-model",
		Stream: false,
		Input:  []canon.Item{canon.Message{Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "PING"}}}},
		Reasoning: canon.ReasoningConfig{
			Effort: canon.EffortHigh,
		},
	}
	got, err := (chat.Builder{}).Build(context.Background(), req, conformance.BuildOptions{
		BaseURL:             "http://127.0.0.1:1/v1",
		ReasoningWireFormat: "gateway-object",
		ReasoningEffortMap:  map[string]string{"high": "adaptive"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"model":"fixture-model","messages":[{"role":"user","content":"PING"}],"stream":false,"reasoning":{"enabled":true,"effort":"adaptive"}}`
	if string(got.Body) != want {
		t.Fatalf("body %s want %s", got.Body, want)
	}
	var parsed map[string]any
	if err := json.Unmarshal(got.Body, &parsed); err != nil {
		t.Fatal(err)
	}
	if _, ok := parsed["reasoning_effort"]; ok {
		t.Fatal("gateway-object must not emit legacy reasoning_effort")
	}
}

func TestEffortMappingNativeKeepsLegacyField(t *testing.T) {
	req := canon.Request{
		Model:  "fixture-model",
		Stream: false,
		Input:  []canon.Item{canon.Message{Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "PING"}}}},
		Reasoning: canon.ReasoningConfig{
			Effort: canon.EffortHigh,
		},
	}
	got, err := (chat.Builder{}).Build(context.Background(), req, conformance.BuildOptions{
		BaseURL:             "https://api.openai.com/v1",
		ReasoningWireFormat: "gateway-object",
		ReasoningEffortMap:  map[string]string{"high": "adaptive"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"model":"fixture-model","messages":[{"role":"user","content":"PING"}],"stream":false,"reasoning_effort":"adaptive"}`
	if string(got.Body) != want {
		t.Fatalf("body %s want %s", got.Body, want)
	}
}

func TestReasoningReplayWire(t *testing.T) {
	req := canon.Request{
		Model:  "fixture-model",
		Stream: false,
		Input: []canon.Item{
			canon.ReasoningItem{ID: "rs_fixture", Content: "PLAN", Signature: "sig_fixture"},
			canon.FunctionOutput{CallID: "call_fixture", Output: []canon.Content{canon.TextContent{Text: "RESULT"}}},
		},
	}
	got, err := (responses.Builder{}).Build(context.Background(), req, conformance.BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"model":"fixture-model","input":[{"type":"reasoning","id":"rs_fixture","content":[{"type":"reasoning_text","text":"PLAN"}],"signature":"sig_fixture"},{"type":"function_call_output","call_id":"call_fixture","output":"RESULT"}],"stream":false}`
	if string(got.Body) != want {
		t.Fatalf("body %s want %s", got.Body, want)
	}
}

func TestPrivateContentIsolationWire(t *testing.T) {
	req := canon.Request{
		Model:  "fixture-model",
		Stream: false,
		Input: []canon.Item{
			canon.Message{Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "PING"}}},
			canon.Message{Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "NEXT"}}},
		},
	}
	got, err := (chat.Builder{}).Build(context.Background(), req, conformance.BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"model":"fixture-model","messages":[{"role":"user","content":"PING"},{"role":"user","content":"NEXT"}],"stream":false}`
	if string(got.Body) != want {
		t.Fatalf("body %s want %s", got.Body, want)
	}
	if strings.Contains(string(got.Body), "encrypted") {
		t.Fatalf("opaque reasoning content leaked into the wire: %s", got.Body)
	}
}
