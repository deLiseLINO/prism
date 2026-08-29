package conformance

import (
	"encoding/json"
	"errors"
	"math"
)

func mcpActionToken(c Case) (string, error) {
	var tokens []string
	for _, f := range c.Requirements.RequiredHarnessFeatures {
		for _, t := range mcpActionTokens {
			if f == t {
				tokens = append(tokens, t)
			}
		}
	}
	if len(tokens) != 1 {
		return "", errors.New("invalid_manifest: missing or ambiguous MCP action token for " + c.ID)
	}
	return tokens[0], nil
}

func ExecuteMcpSyntheticAction(c Case) (*Observation, error) {
	token, err := mcpActionToken(c)
	if err != nil {
		return nil, err
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(c.Fixture.Bytes), &decoded); err != nil {
		return nil, errors.New("invalid_manifest: fixture decode: " + err.Error())
	}
	switch token {
	case "mcp_namespace_round_trip_v1":
		return runNamespaceRoundTrip(decoded)
	case "mcp_schema_bounds_v1":
		return runSchemaBounds(decoded), nil
	case "mcp_call_result_v1":
		return runCallResult(decoded)
	case "mcp_resource_round_trip_v1":
		return runResourceRoundTrip(decoded)
	}
	return nil, errors.New("invalid_manifest: missing or ambiguous MCP action token for " + c.ID)
}

func nonEmptyString(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok && len(s) > 0
}

func runNamespaceRoundTrip(decoded map[string]any) (*Observation, error) {
	namespace, ok := nonEmptyString(decoded["namespace"])
	if !ok {
		return nil, errors.New("invalid_manifest: MCP namespace/name must be non-empty strings")
	}
	name, ok := nonEmptyString(decoded["name"])
	if !ok {
		return nil, errors.New("invalid_manifest: MCP namespace/name must be non-empty strings")
	}
	wireName := namespace + "__" + name
	o := Empty()
	o.Upstream.Requests = append(o.Upstream.Requests, Record{
		JSON: map[string]any{
			"model": "fixture-model",
			"tools": []any{
				map[string]any{
					"name":        wireName,
					"description": decoded["description"],
					"inputSchema": decoded["inputSchema"],
				},
			},
		},
	})
	toolCalls := []ToolCallProjection{{
		ID: "call_fixture", Name: wireName, Arguments: map[string]any{}, Kind: "function", Ordinal: 0,
	}}
	o.Client.Response.Status = 200
	o.Client.Response.ToolCalls = toolCalls
	o.Client.Response.McpCalls = projectMcpCalls(toolCalls)
	return o, nil
}

func runSchemaBounds(decoded map[string]any) *Observation {
	o := Empty()
	limitBytes, ok := decoded["limitBytes"].(float64)
	if !ok || limitBytes != math.Trunc(limitBytes) || limitBytes <= 0 {
		o.Verifiers["exact_bound"] = "fail"
		o.Verifiers["one_over_rejected"] = "fail"
		o.Verifiers["partial_commit"] = false
		return o
	}
	exactSchema, ok := decoded["exactSchema"].(string)
	if !ok {
		o.Verifiers["exact_bound"] = "fail"
		o.Verifiers["one_over_rejected"] = "fail"
		o.Verifiers["partial_commit"] = false
		return o
	}
	overSchema, ok := decoded["overSchema"].(string)
	if !ok {
		o.Verifiers["exact_bound"] = "fail"
		o.Verifiers["one_over_rejected"] = "fail"
		o.Verifiers["partial_commit"] = false
		return o
	}
	exactValid := len([]byte(exactSchema)) == int(limitBytes) && json.Valid([]byte(exactSchema))
	overRejected := len([]byte(overSchema)) == int(limitBytes)+1 && json.Valid([]byte(overSchema))
	o.Verifiers["exact_bound"] = boolString(exactValid)
	o.Verifiers["one_over_rejected"] = boolString(overRejected)
	o.Verifiers["partial_commit"] = false
	return o
}

func runCallResult(decoded map[string]any) (*Observation, error) {
	namespace, ok := nonEmptyString(decoded["namespace"])
	if !ok {
		return nil, errors.New("invalid_manifest: MCP namespace/name must be non-empty strings")
	}
	name, ok := nonEmptyString(decoded["name"])
	if !ok {
		return nil, errors.New("invalid_manifest: MCP namespace/name must be non-empty strings")
	}
	argumentsValue, ok := decoded["arguments"]
	if !ok {
		argumentsValue = map[string]any{}
	}
	wireName := namespace + "__" + name
	o := Empty()
	toolCalls := []ToolCallProjection{{
		ID: "call_fixture", Name: wireName, Arguments: argumentsValue, Kind: "function", Ordinal: 0,
	}}
	o.Client.Response.Status = 200
	o.Client.Response.ToolCalls = toolCalls
	o.Client.Response.McpCalls = projectMcpCalls(toolCalls)
	o.Client.Response.JSON = decoded["result"]
	o.Verifiers["stub_received"] = map[string]any{
		"namespace": namespace, "name": name, "arguments": argumentsValue,
	}
	return o, nil
}

func runResourceRoundTrip(decoded map[string]any) (*Observation, error) {
	resources, ok := decoded["resources"].([]any)
	if !ok {
		return nil, errors.New("invalid_manifest: invalid MCP resource fixture")
	}
	read, ok := decoded["read"].(map[string]any)
	if !ok {
		return nil, errors.New("invalid_manifest: invalid MCP resource fixture")
	}
	uri, ok := nonEmptyString(read["uri"])
	if !ok {
		return nil, errors.New("invalid_manifest: invalid MCP resource read fixture")
	}
	contents, ok := read["contents"].([]any)
	if !ok {
		return nil, errors.New("invalid_manifest: invalid MCP resource read fixture")
	}
	matching := 0
	for _, r := range resources {
		rec, ok := r.(map[string]any)
		if !ok {
			continue
		}
		if ru, _ := rec["uri"].(string); ru == uri {
			matching++
		}
	}
	if matching != 1 {
		return nil, errors.New("invalid_manifest: MCP resource URI must resolve exactly once")
	}
	o := Empty()
	o.Client.Response.Status = 200
	o.Client.Response.JSON = map[string]any{"resources": resources, "contents": contents}
	return o, nil
}

func AttachMcpVerifiers(o *Observation, c Case) {
	if c.ID != "mcp-core.protocol.namespace-mapping" {
		return
	}
	toolCalls := o.Client.Response.ToolCalls
	if len(toolCalls) == 1 {
		o.Client.Response.McpCalls = projectMcpCalls(toolCalls)
	}
}
