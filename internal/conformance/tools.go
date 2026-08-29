package conformance

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"prism/internal/canon"
)

func runToolsCoreAdapterVector(c Case, r *Runner, opts BuildOptions) (*Observation, error) {
	switch c.ID {
	case "tools-core.protocol.function-round-trip":
		return runToolFunctionRoundTrip(c, r, opts)
	case "tools-core.protocol.custom-freeform-round-trip":
		return runToolCustomRoundTrip(c, r, opts)
	case "tools-core.protocol.result-content":
		return runToolResultContent(c, r, opts)
	default:
		return nil, fmt.Errorf("unsupported tools-core adapter vector %s", c.ID)
	}
}

func decodeVector(c Case) (map[string]any, error) {
	var vector map[string]any
	if err := json.Unmarshal([]byte(c.Fixture.Bytes), &vector); err != nil {
		return nil, fmt.Errorf("fixture decode: %w", err)
	}
	return vector, nil
}

func fixtureUserMessage() canon.Item {
	return canon.Message{Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "PING"}}}
}

func runToolFunctionRoundTrip(c Case, r *Runner, opts BuildOptions) (*Observation, error) {
	vector, err := decodeVector(c)
	if err != nil {
		return nil, err
	}
	rawTools, ok := vector["tools"].([]any)
	if !ok {
		return nil, fmt.Errorf("fixture missing tools")
	}
	tools, err := vectorTools(rawTools)
	if err != nil {
		return nil, err
	}
	obs := Empty()
	req1 := canon.Request{Model: "fixture-model", Input: []canon.Item{fixtureUserMessage()}, Tools: tools, Stream: false}
	built1, err := r.builderFor("openai-chat").Build(context.Background(), req1, opts)
	if err != nil {
		return nil, fmt.Errorf("codec build request 1: %w", err)
	}
	RecordUpstreamRequest(obs, built1)

	upstreamCall, _ := vector["upstreamToolCall"].(map[string]any)
	callID, _ := upstreamCall["id"].(string)
	name, _ := upstreamCall["name"].(string)
	arguments, _ := upstreamCall["arguments"].(string)
	item := map[string]any{
		"type":      "function_call",
		"call_id":   callID,
		"name":      name,
		"arguments": arguments,
	}
	events := []NormalizedEvent{{Event: "response.output_item.done", Data: map[string]any{"item": item}, Ordinal: 0}}
	FinalizeObservation(obs, events, nil, 200)

	toolResult, _ := vector["toolResult"].(map[string]any)
	resultID, _ := toolResult["toolCallId"].(string)
	resultContent := ""
	if s, ok := toolResult["content"].(string); ok {
		resultContent = s
	}
	req2 := canon.Request{
		Model: "fixture-model",
		Input: []canon.Item{
			canon.FunctionCall{CallID: canon.CallID(callID), Name: canon.ToolName(name), Arguments: []byte(arguments)},
			canon.FunctionOutput{CallID: canon.CallID(resultID), Output: []canon.Content{canon.TextContent{Text: resultContent}}},
		},
		Stream: false,
	}
	built2, err := r.builderFor("openai-chat").Build(context.Background(), req2, opts)
	if err != nil {
		return nil, fmt.Errorf("codec build request 2: %w", err)
	}
	RecordUpstreamRequest(obs, built2)
	AttachVerifiers(obs, c)
	return obs, nil
}

func runToolCustomRoundTrip(c Case, r *Runner, opts BuildOptions) (*Observation, error) {
	vector, err := decodeVector(c)
	if err != nil {
		return nil, err
	}
	rawTool, ok := vector["tool"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("fixture missing tool")
	}
	tools, err := vectorTools([]any{rawTool})
	if err != nil {
		return nil, err
	}
	obs := Empty()
	req1 := canon.Request{Model: "fixture-model", Input: []canon.Item{fixtureUserMessage()}, Tools: tools, Stream: false}
	built1, err := r.builderFor("openai-responses").Build(context.Background(), req1, opts)
	if err != nil {
		return nil, fmt.Errorf("codec build request 1: %w", err)
	}
	RecordUpstreamRequest(obs, built1)

	call, _ := vector["call"].(map[string]any)
	callID, _ := call["id"].(string)
	name, _ := call["name"].(string)
	input, _ := call["input"].(string)
	item := map[string]any{
		"type":    "custom_tool_call",
		"call_id": callID,
		"name":    name,
		"input":   input,
	}
	events := []NormalizedEvent{{Event: "response.output_item.done", Data: map[string]any{"item": item}, Ordinal: 0}}
	FinalizeObservation(obs, events, nil, 200)

	output, _ := vector["output"].(map[string]any)
	outputID, _ := output["call_id"].(string)
	outputText, _ := output["output"].(string)
	req2 := canon.Request{
		Model: "fixture-model",
		Input: []canon.Item{
			canon.CustomToolCall{CallID: canon.CallID(callID), Name: canon.ToolName(name), Input: input},
			canon.CustomToolOutput{CallID: canon.CallID(outputID), Output: outputText},
		},
		Tools:  tools,
		Stream: false,
	}
	built2, err := r.builderFor("openai-responses").Build(context.Background(), req2, opts)
	if err != nil {
		return nil, fmt.Errorf("codec build request 2: %w", err)
	}
	RecordUpstreamRequest(obs, built2)
	AttachVerifiers(obs, c)
	return obs, nil
}

func runToolResultContent(c Case, r *Runner, opts BuildOptions) (*Observation, error) {
	vector, err := decodeVector(c)
	if err != nil {
		return nil, err
	}
	callID, _ := vector["callId"].(string)
	rawContent, _ := vector["content"].([]any)
	content := make([]canon.Content, 0, len(rawContent))
	for _, part := range rawContent {
		pm, ok := part.(map[string]any)
		if !ok {
			continue
		}
		switch pm["type"] {
		case "input_text":
			text, _ := pm["text"].(string)
			content = append(content, canon.TextContent{Text: text})
		case "input_image":
			url, _ := pm["image_url"].(string)
			mime, data := dataURLParts(url)
			detail, _ := pm["detail"].(string)
			content = append(content, canon.ImageContent{MIMEType: mime, Data: data, Detail: detail})
		}
	}
	req := canon.Request{
		Model:  "fixture-model",
		Input:  []canon.Item{canon.FunctionOutput{CallID: canon.CallID(callID), Output: content}},
		Stream: false,
	}
	built, err := r.builderFor("openai-chat").Build(context.Background(), req, opts)
	if err != nil {
		return nil, fmt.Errorf("codec build: %w", err)
	}
	normalized, err := normalizeImageToolResultBody(built.Body)
	if err != nil {
		return nil, err
	}
	obs := Empty()
	RecordUpstreamRequest(obs, &UpstreamRequest{Method: built.Method, URL: built.URL, Headers: built.Headers, Body: normalized})
	AttachVerifiers(obs, c)
	return obs, nil
}

func dataURLParts(url string) (string, []byte) {
	rest, ok := strings.CutPrefix(url, "data:")
	if !ok {
		return "", nil
	}
	meta, payload, ok := strings.Cut(rest, ",")
	if !ok {
		return "", nil
	}
	mime, _, _ := strings.Cut(meta, ";")
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return mime, nil
	}
	return mime, data
}

func normalizeImageToolResultBody(body []byte) ([]byte, error) {
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("normalize: %w", err)
	}
	messages, _ := parsed["messages"].([]any)
	var toolMsg map[string]any
	var imagePart map[string]any
	for _, m := range messages {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := mm["role"].(string); role == "tool" && toolMsg == nil {
			toolMsg = mm
			continue
		}
		if role, _ := mm["role"].(string); role == "user" {
			content, _ := mm["content"].([]any)
			for _, p := range content {
				pm, ok := p.(map[string]any)
				if !ok {
					continue
				}
				if t, _ := pm["type"].(string); t == "image_url" && imagePart == nil {
					imagePart = pm
				}
			}
		}
	}
	if toolMsg == nil || imagePart == nil {
		return nil, fmt.Errorf("normalize: tool message or image part missing")
	}
	normalized := map[string]any{
		"model": parsed["model"],
		"messages": []any{
			map[string]any{
				"role":         "tool",
				"tool_call_id": toolMsg["tool_call_id"],
				"content":      toolMsg["content"],
			},
			map[string]any{
				"role":    "user",
				"content": []any{imagePart},
			},
		},
		"stream": parsed["stream"],
	}
	out, err := json.Marshal(normalized)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func runToolsCoreChatSse(c Case, r *Runner, opts BuildOptions) (*Observation, error) {
	obs := Empty()
	if c.InitiatingRequest != nil {
		var vector map[string]any
		if err := json.Unmarshal([]byte(c.InitiatingRequest.Bytes), &vector); err != nil {
			return nil, fmt.Errorf("initiating request decode: %w", err)
		}
		built, err := r.builderFor("openai-chat").Build(context.Background(), VectorToRequest(vector), opts)
		if err != nil {
			return nil, fmt.Errorf("codec build initiating request: %w", err)
		}
		RecordUpstreamRequest(obs, built)
	}
	events, err := NormalizeSseBytes([]byte(c.Fixture.Bytes), "openai-chat")
	if err != nil {
		return nil, fmt.Errorf("sse normalize: %w", err)
	}
	items := chatToolCallItems(events)
	bridged := make([]NormalizedEvent, 0, len(items))
	for i, item := range items {
		bridged = append(bridged, NormalizedEvent{Event: "response.output_item.done", Data: map[string]any{"item": item}, Ordinal: i})
	}
	FinalizeObservation(obs, bridged, nil, 200)
	AttachVerifiers(obs, c)
	return obs, nil
}

type pendingChatCall struct {
	id   string
	name string
	args string
}

func chatToolCallItems(events []NormalizedEvent) []map[string]any {
	var order []int
	pending := map[int]*pendingChatCall{}
	flush := func() []map[string]any {
		var out []map[string]any
		for _, idx := range order {
			call := pending[idx]
			if call.name == "" {
				continue
			}
			args := call.args
			if args == "" {
				args = "{}"
			}
			out = append(out, map[string]any{
				"type":      "function_call",
				"call_id":   call.id,
				"name":      call.name,
				"arguments": args,
			})
		}
		order = nil
		pending = map[int]*pendingChatCall{}
		return out
	}
	var items []map[string]any
	for _, ev := range events {
		if ev.Event == "[DONE]" {
			items = append(items, flush()...)
			continue
		}
		data, ok := ev.Data.(map[string]any)
		if !ok {
			continue
		}
		choices, _ := data["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice, ok := choices[0].(map[string]any)
		if !ok {
			continue
		}
		finish, _ := choice["finish_reason"].(string)
		delta, _ := choice["delta"].(map[string]any)
		if delta != nil {
			rawCalls, _ := delta["tool_calls"].([]any)
			for _, rc := range rawCalls {
				tc, ok := rc.(map[string]any)
				if !ok {
					continue
				}
				key := -1
				if f, ok := tc["index"].(float64); ok {
					key = int(f)
				}
				if key < 0 {
					if len(order) > 0 {
						key = order[len(order)-1]
					} else {
						continue
					}
				}
				call, exists := pending[key]
				if !exists {
					call = &pendingChatCall{}
					pending[key] = call
					order = append(order, key)
				}
				if id, ok := tc["id"].(string); ok && id != "" && call.id == "" {
					call.id = id
				}
				fn, _ := tc["function"].(map[string]any)
				if fn == nil {
					continue
				}
				if name, ok := fn["name"].(string); ok && name != "" && call.name == "" {
					call.name = name
				}
				if args, ok := fn["arguments"].(string); ok {
					call.args += args
				}
			}
		}
		if finish != "" {
			items = append(items, flush()...)
		}
	}
	items = append(items, flush()...)
	return items
}
