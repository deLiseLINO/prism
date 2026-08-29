package conformance

import (
	"testing"

	"prism/internal/canon"
)

func TestGoldenDiff(t *testing.T) {
	got := &UpstreamRequest{
		Method: "POST",
		URL:    "https://api.openai.com/v1/chat/completions",
		Headers: Headers{
			{Name: "Content-Type", Value: "application/json"},
			{Name: "Authorization", Value: "Bearer fixture-key"},
		},
		Body: []byte(`{"model":"fixture-model"}`),
	}
	want := Golden{
		Method: "POST",
		URL:    "https://api.openai.com/v1/chat/completions",
		Headers: Headers{
			{Name: "Content-Type", Value: "application/json"},
			{Name: "Authorization", Value: "Bearer fixture-key"},
		},
		Body: `{"model":"fixture-model"}`,
	}
	if err := CheckGolden(got, want); err != nil {
		t.Fatalf("exact match must pass: %v", err)
	}
	want.Body = `{"model":"other"}`
	if err := CheckGolden(got, want); err == nil {
		t.Fatal("body mismatch must fail")
	}
	want.Body = `{"model":"fixture-model"}`
	want.Headers = Headers{
		{Name: "authorization", Value: "Bearer fixture-key"},
		{Name: "content-type", Value: "application/json"},
	}
	matched, headerMatched, _ := goldenDiff(got, want)
	if !matched || headerMatched {
		t.Fatalf("header case/order must fail the case+order check: matched=%v headerMatched=%v", matched, headerMatched)
	}
}

func TestVectorToRequest(t *testing.T) {
	v := map[string]any{
		"modelId": "fixture-model",
		"context": map[string]any{
			"messages": []any{
				map[string]any{"role": "user", "content": "PING", "timestamp": 0},
			},
		},
		"stream":  false,
		"options": map[string]any{"temperature": float64(0)},
	}
	req := VectorToRequest(v)
	if req.Model != "fixture-model" {
		t.Fatalf("model %s", req.Model)
	}
	if req.Stream {
		t.Fatal("stream must be false")
	}
	if req.Sampling.Temperature == nil || *req.Sampling.Temperature != 0 {
		t.Fatalf("temperature must carry 0 through the pointer")
	}
	if len(req.Input) != 1 {
		t.Fatalf("input %d", len(req.Input))
	}
	m, ok := req.Input[0].(canon.Message)
	if !ok || m.Role != canon.RoleUser || len(m.Content) != 1 {
		t.Fatalf("input message %+v", req.Input[0])
	}
	text, ok := m.Content[0].(canon.TextContent)
	if !ok || text.Text != "PING" {
		t.Fatalf("content %+v", m.Content)
	}

	fallback := VectorToRequest(map[string]any{})
	if fallback.Model != "fixture-model" {
		t.Fatalf("fallback model %s", fallback.Model)
	}
	m2, ok := fallback.Input[0].(canon.Message)
	if !ok || m2.Role != canon.RoleUser {
		t.Fatalf("fallback input %+v", fallback.Input[0])
	}
	t2, ok := m2.Content[0].(canon.TextContent)
	if !ok || t2.Text != "PING" {
		t.Fatalf("fallback content %+v", m2.Content)
	}
}
