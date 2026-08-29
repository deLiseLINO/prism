package conformance

import (
	"context"
	"testing"

	"prism/internal/canon"
)

func mcpCase(t *testing.T, id string) Case {
	t.Helper()
	a, err := Load(fixturePath())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range a.Cases {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("case %s not found", id)
	return Case{}
}

func TestMcpNamespaceRoundTrip(t *testing.T) {
	c := mcpCase(t, "mcp-core.protocol.namespace-mapping")
	o, err := ExecuteMcpSyntheticAction(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Upstream.Requests) != 1 {
		t.Fatalf("upstream requests %d", len(o.Upstream.Requests))
	}
	root, ok := o.Upstream.Requests[0].JSON.(map[string]any)
	if !ok {
		t.Fatalf("upstream json %T", o.Upstream.Requests[0].JSON)
	}
	if root["model"] != "fixture-model" {
		t.Fatalf("model %v", root["model"])
	}
	tools, ok := root["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools %v", root["tools"])
	}
	tool, ok := tools[0].(map[string]any)
	if !ok || tool["name"] != "mcp__fixture__lookup" {
		t.Fatalf("wire name %v", tool["name"])
	}
	if o.Client.Response.Status != 200 {
		t.Fatalf("status %d", o.Client.Response.Status)
	}
	if len(o.Client.Response.McpCalls) != 1 {
		t.Fatalf("mcpCalls %d", len(o.Client.Response.McpCalls))
	}
	if o.Client.Response.McpCalls[0] != (McpCallProjection{Namespace: "mcp__fixture", Name: "lookup"}) {
		t.Fatalf("mcpCall %+v", o.Client.Response.McpCalls[0])
	}
	AttachMcpVerifiers(o, c)
	if len(o.Client.Response.McpCalls) != 1 {
		t.Fatalf("mcpCalls after attach %d", len(o.Client.Response.McpCalls))
	}
}

func TestMcpSchemaBounds(t *testing.T) {
	c := mcpCase(t, "mcp-core.protocol.schema-and-bounds")
	o, err := ExecuteMcpSyntheticAction(c)
	if err != nil {
		t.Fatal(err)
	}
	if o.Verifiers["exact_bound"] != "pass" {
		t.Fatalf("exact_bound %v", o.Verifiers["exact_bound"])
	}
	if o.Verifiers["one_over_rejected"] != "pass" {
		t.Fatalf("one_over_rejected %v", o.Verifiers["one_over_rejected"])
	}
	if o.Verifiers["partial_commit"] != false {
		t.Fatalf("partial_commit %v", o.Verifiers["partial_commit"])
	}
}

func TestMcpSchemaBoundsDegraded(t *testing.T) {
	base := Case{
		ID: "degraded", Suite: "mcp-core",
		Fixture:      Fixture{Role: RoleSyntheticTool, Bytes: `{"limitBytes":0,"exactSchema":"{}","overSchema":"{}"}`},
		Requirements: Requirements{RequiredHarnessFeatures: []string{"mcp_schema_bounds_v1"}},
	}
	o, err := ExecuteMcpSyntheticAction(base)
	if err != nil {
		t.Fatal(err)
	}
	if o.Verifiers["exact_bound"] != "fail" || o.Verifiers["one_over_rejected"] != "fail" {
		t.Fatalf("verifiers %v", o.Verifiers)
	}
	base.Fixture.Bytes = `{"limitBytes":2,"exactSchema":"{}","overSchema":"xx"}`
	o, err = ExecuteMcpSyntheticAction(base)
	if err != nil {
		t.Fatal(err)
	}
	if o.Verifiers["exact_bound"] != "pass" {
		t.Fatalf("exact_bound %v", o.Verifiers["exact_bound"])
	}
	if o.Verifiers["one_over_rejected"] != "fail" {
		t.Fatalf("one_over_rejected %v", o.Verifiers["one_over_rejected"])
	}
}

func TestMcpCallResult(t *testing.T) {
	c := mcpCase(t, "mcp-core.protocol.call-result")
	o, err := ExecuteMcpSyntheticAction(c)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"namespace": "mcp__fixture", "name": "lookup",
		"arguments": map[string]any{"q": "x"},
	}
	if !Equal(o.Verifiers["stub_received"], want) {
		t.Fatalf("stub_received %v", o.Verifiers["stub_received"])
	}
	wantResult := map[string]any{
		"content": []any{map[string]any{"type": "text", "text": "RESULT"}},
		"isError": false,
	}
	if !Equal(o.Client.Response.JSON, wantResult) {
		t.Fatalf("client json %v", o.Client.Response.JSON)
	}
	if len(o.Client.Response.McpCalls) != 1 {
		t.Fatalf("mcpCalls %d", len(o.Client.Response.McpCalls))
	}
}

func TestMcpResourceRoundTrip(t *testing.T) {
	c := mcpCase(t, "mcp-core.protocol.resource-round-trip")
	o, err := ExecuteMcpSyntheticAction(c)
	if err != nil {
		t.Fatal(err)
	}
	root, ok := o.Client.Response.JSON.(map[string]any)
	if !ok {
		t.Fatalf("client json %T", o.Client.Response.JSON)
	}
	wantResources := []any{map[string]any{"uri": "fixture://one", "name": "one"}}
	if !Equal(root["resources"], wantResources) {
		t.Fatalf("resources %v", root["resources"])
	}
	wantContents := []any{map[string]any{"uri": "fixture://one", "text": "RESOURCE"}}
	if !Equal(root["contents"], wantContents) {
		t.Fatalf("contents %v", root["contents"])
	}
}

func TestMcpActionTokenAmbiguous(t *testing.T) {
	c := Case{
		ID: "ambiguous", Suite: "mcp-core",
		Fixture: Fixture{Role: RoleSyntheticTool, Bytes: `{}`},
		Requirements: Requirements{RequiredHarnessFeatures: []string{
			"mcp_namespace_round_trip_v1", "mcp_call_result_v1",
		}},
	}
	if _, err := ExecuteMcpSyntheticAction(c); err == nil {
		t.Fatal("ambiguous token must error")
	}
	c.Requirements.RequiredHarnessFeatures = nil
	if _, err := ExecuteMcpSyntheticAction(c); err == nil {
		t.Fatal("missing token must error")
	}
}

type noopBuilder struct{}

func (noopBuilder) Build(ctx context.Context, req canon.Request, opts BuildOptions) (*UpstreamRequest, error) {
	return &UpstreamRequest{Method: "POST", URL: opts.BaseURL}, nil
}

func TestMcpRunnerEndToEnd(t *testing.T) {
	a, err := Load(fixturePath())
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(noopBuilder{})
	opts := Options{BaseURL: "http://127.0.0.1:1/v1", APIKey: "fixture-key", Goldens: map[string]Golden{}}
	for _, c := range a.Cases {
		if c.Suite != "mcp-core" {
			continue
		}
		res := runner.Run(context.Background(), c, opts)
		if !res.Passed {
			t.Fatalf("%s: failed with %s/%s, diagnostics %v", c.ID, res.Classification, res.SecondaryCode, res.Diagnostics)
		}
		for _, ar := range res.AssertionResults {
			if !ar.Passed {
				t.Fatalf("%s: assertion %s failed: %s", c.ID, ar.ID, ar.Reason)
			}
		}
	}
}
