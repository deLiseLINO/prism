package conformance

import (
	"encoding/json"
	"strings"
)

func deriveTerminal(events []NormalizedEvent) *string {
	terminal := ""
	switch {
	case containsEvent(events, "error"):
		terminal = "failed"
	case containsEvent(events, "response.failed"):
		terminal = "failed"
	case containsEvent(events, "response.completed"):
		terminal = "completed"
	case containsEvent(events, "message_stop"):
		terminal = "message_stop"
	case containsEvent(events, "response.incomplete"):
		terminal = "incomplete"
	default:
		return nil
	}
	return &terminal
}

func containsEvent(events []NormalizedEvent, name string) bool {
	for _, ev := range events {
		if ev.Event == name {
			return true
		}
	}
	return false
}

func extractOutputText(json any) string {
	root, ok := json.(map[string]any)
	if !ok {
		return ""
	}
	output, ok := root["output"].([]any)
	if !ok {
		return ""
	}
	var text strings.Builder
	for _, item := range output {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		content, ok := m["content"].([]any)
		if !ok {
			continue
		}
		for _, part := range content {
			p, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if p["type"] == "output_text" {
				if s, ok := p["text"].(string); ok {
					text.WriteString(s)
				}
			}
		}
	}
	return text.String()
}

func deriveNormalizedText(events []NormalizedEvent, json any) string {
	if json != nil {
		if text := extractOutputText(json); text != "" {
			return text
		}
	}
	var text strings.Builder
	for _, ev := range events {
		data, ok := ev.Data.(map[string]any)
		if !ok {
			continue
		}
		switch ev.Event {
		case "response.output_text.delta":
			if delta, ok := data["delta"].(string); ok {
				text.WriteString(delta)
			}
		case "content_block_delta":
			delta, _ := data["delta"].(map[string]any)
			if s, ok := delta["text"].(string); ok {
				text.WriteString(s)
			}
		}
	}
	return text.String()
}

type toolCallProjectionResult struct {
	calls        []ToolCallProjection
	sawCallItems bool
	duplicateIds bool
}

func parseToolArguments(raw any, kind string) any {
	if kind == "custom" {
		if s, ok := raw.(string); ok {
			return s
		}
		return ""
	}
	if s, ok := raw.(string); ok {
		var parsed any
		if err := json.Unmarshal([]byte(s), &parsed); err != nil {
			return nil
		}
		return parsed
	}
	return raw
}

func projectToolCallsDetailed(output []any) toolCallProjectionResult {
	var calls []ToolCallProjection
	ordinal := 0
	sawCallItems := false
	for _, item := range output {
		rec, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch rec["type"] {
		case "function_call":
			sawCallItems = true
			id, _ := rec["call_id"].(string)
			if id == "" {
				if v, ok := rec["id"].(string); ok {
					id = v
				}
			}
			name, _ := rec["name"].(string)
			if id == "" || name == "" {
				continue
			}
			args := parseToolArguments(rec["arguments"], "function")
			if args == nil {
				continue
			}
			calls = append(calls, ToolCallProjection{ID: id, Name: name, Arguments: args, Kind: "function", Ordinal: ordinal})
			ordinal++
		case "custom_tool_call":
			sawCallItems = true
			id, _ := rec["call_id"].(string)
			if id == "" {
				if v, ok := rec["id"].(string); ok {
					id = v
				}
			}
			name, _ := rec["name"].(string)
			if id == "" || name == "" {
				continue
			}
			calls = append(calls, ToolCallProjection{
				ID: id, Name: name, Arguments: parseToolArguments(rec["input"], "custom"), Kind: "custom", Ordinal: ordinal,
			})
			ordinal++
		}
	}
	ids := make(map[string]struct{}, len(calls))
	duplicate := false
	for _, call := range calls {
		if _, seen := ids[call.ID]; seen {
			duplicate = true
			break
		}
		ids[call.ID] = struct{}{}
	}
	if duplicate {
		calls = nil
	}
	return toolCallProjectionResult{calls: calls, sawCallItems: sawCallItems, duplicateIds: duplicate}
}

func projectToolCallsFromEvents(events []NormalizedEvent) toolCallProjectionResult {
	var output []any
	for _, ev := range events {
		if ev.Event != "response.output_item.done" {
			continue
		}
		data, ok := ev.Data.(map[string]any)
		if !ok {
			continue
		}
		if item, ok := data["item"]; ok {
			output = append(output, item)
		}
	}
	return projectToolCallsDetailed(output)
}

func projectMcpCalls(toolCalls []ToolCallProjection) []McpCallProjection {
	var out []McpCallProjection
	for _, call := range toolCalls {
		if !strings.HasPrefix(call.Name, "mcp__") {
			continue
		}
		idx := strings.LastIndex(call.Name, "__")
		if idx <= 0 || idx >= len(call.Name)-2 {
			continue
		}
		namespace := call.Name[:idx]
		name := call.Name[idx+2:]
		if namespace == "" || name == "" {
			continue
		}
		if len(namespace) > 64 || len(name) > 64 {
			continue
		}
		out = append(out, McpCallProjection{Namespace: namespace, Name: name})
	}
	return out
}

func FinalizeObservation(o *Observation, events []NormalizedEvent, json any, status int) {
	eventProjection := projectToolCallsFromEvents(events)
	var jsonProjection toolCallProjectionResult
	if root, ok := json.(map[string]any); ok {
		if output, ok := root["output"].([]any); ok {
			jsonProjection = projectToolCallsDetailed(output)
		}
	}
	selected := jsonProjection
	if eventProjection.sawCallItems {
		selected = eventProjection
	}
	resolved := selected.calls
	o.Client.Response = ResponseRecord{
		Status:         status,
		JSON:           json,
		Events:         events,
		ToolCalls:      resolved,
		McpCalls:       projectMcpCalls(resolved),
		Terminal:       deriveTerminal(events),
		NormalizedText: deriveNormalizedText(events, json),
	}
	o.Verifiers["duplicate_tool_call_ids"] = selected.duplicateIds
}

func AttachVerifiers(o *Observation, c Case) {
	toolCalls := o.Client.Response.ToolCalls
	ids := []string{}
	nonoverlap := true
	for i, call := range toolCalls {
		if call.ID == "" || call.Arguments == nil || call.Ordinal != i {
			nonoverlap = false
			break
		}
		ids = append(ids, call.ID)
	}
	if nonoverlap {
		unique := map[string]struct{}{}
		for _, id := range ids {
			unique[id] = struct{}{}
		}
		if len(unique) != len(ids) {
			ids = []string{}
		}
	} else {
		ids = []string{}
	}
	o.Verifiers["nonoverlap_order"] = ids
	o.Verifiers["call_result_order"] = evaluateCallResultOrder(o)
	if c.ID == "responses-core.protocol.json-sse-equivalence" {
		o.Verifiers["json_sse_equivalence"] = evaluateJsonSseEquivalence(c)
	}
}

func evaluateCallResultOrder(o *Observation) string {
	if len(o.Upstream.Requests) == 0 {
		return "fail"
	}
	root, ok := o.Upstream.Requests[0].JSON.(map[string]any)
	if !ok {
		return "fail"
	}
	input, ok := root["input"].([]any)
	if !ok {
		return "fail"
	}
	pending := map[string]struct{}{}
	callCount := 0
	resultCount := 0
	for _, item := range input {
		rec, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch rec["type"] {
		case "function_call":
			callID, _ := rec["call_id"].(string)
			if callID == "" {
				return "fail"
			}
			if _, seen := pending[callID]; seen {
				return "fail"
			}
			pending[callID] = struct{}{}
			callCount++
		case "function_call_output":
			callID, _ := rec["call_id"].(string)
			if callID == "" {
				return "fail"
			}
			if _, seen := pending[callID]; !seen {
				return "fail"
			}
			delete(pending, callID)
			resultCount++
		}
	}
	if callCount > 0 && resultCount == callCount && len(pending) == 0 {
		return "pass"
	}
	return "fail"
}

func evaluateJsonSseEquivalence(c Case) string {
	var vector map[string]any
	if err := json.Unmarshal([]byte(c.Fixture.Bytes), &vector); err != nil {
		return "fail"
	}
	json, _ := vector["json"].(map[string]any)
	sse, _ := vector["sse"].(string)
	if json == nil {
		return "fail"
	}
	jsonProjection := map[string]any{
		"text":     extractOutputText(json),
		"terminal": fmtString(json["status"]),
	}
	events, err := NormalizeSseBytes([]byte(sse), "openai-responses")
	if err != nil {
		return "fail"
	}
	var sseText strings.Builder
	sseTerminal := ""
	for _, ev := range events {
		data, ok := ev.Data.(map[string]any)
		if !ok {
			continue
		}
		switch ev.Event {
		case "response.output_text.delta":
			if delta, ok := data["delta"].(string); ok {
				sseText.WriteString(delta)
			}
		case "response.completed":
			response, _ := data["response"].(map[string]any)
			if status, ok := response["status"].(string); ok {
				sseTerminal = status
			}
		}
	}
	sseProjection := map[string]any{"text": sseText.String(), "terminal": sseTerminal}
	return boolString(jsonStringify(jsonProjection) == jsonStringify(sseProjection))
}

func fmtString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func jsonStringify(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(raw)
}

func boolString(v bool) string {
	if v {
		return "pass"
	}
	return "fail"
}
