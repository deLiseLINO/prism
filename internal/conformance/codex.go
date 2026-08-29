package conformance

import (
	"context"
	"encoding/json"
	"fmt"

	"prism/internal/canon"
)

func ExoticItemName(item canon.Item) string {
	switch item.(type) {
	case canon.CompactionMarker:
		return "compaction_marker"
	case canon.LocalShellCall:
		return "local_shell_call"
	case canon.LocalShellOutput:
		return "local_shell_output"
	case canon.ToolSearchCall:
		return "tool_search_call"
	case canon.ToolSearchOutput:
		return "tool_search_output"
	}
	return "unknown"
}

func buildCodexApplyPatch(ctx context.Context, vector map[string]any, b RequestBuilder, opts BuildOptions) (*UpstreamRequest, []NormalizedEvent, error) {
	callID, _ := vector["callId"].(string)
	input, _ := vector["input"].(string)
	result, _ := vector["result"].(string)
	req := canon.Request{
		Model: "fixture-model",
		Input: []canon.Item{
			canon.CustomToolCall{CallID: canon.CallID(callID), Name: "apply_patch", Input: input},
			canon.CustomToolOutput{CallID: canon.CallID(callID), Output: result},
		},
	}
	built, err := b.Build(ctx, req, opts)
	if err != nil {
		return nil, nil, err
	}
	return built, applyPatchBridgeEvents(callID, input), nil
}

func codexApplyPatchRequest0() *UpstreamRequest {
	return &UpstreamRequest{
		Method: "POST",
		URL:    ChatCompletionsURL("https://api.openai.com/v1"),
		Headers: Headers{
			{Name: "Content-Type", Value: "application/json"},
			{Name: "Authorization", Value: "Bearer fixture-key"},
		},
		Body: []byte(`{"model":"fixture-model","messages":[{"role":"user","content":"PING"}]}`),
	}
}

func buildCodexToolContinuation(ctx context.Context, vector map[string]any, b RequestBuilder, opts BuildOptions) (*UpstreamRequest, error) {
	turn1, _ := vector["turn1"].(map[string]any)
	turn2, _ := vector["turn2"].(map[string]any)
	var input []canon.Item
	if out, ok := turn1["output"].([]any); ok {
		for _, v := range out {
			item, err := itemFromWire(v)
			if err != nil {
				return nil, err
			}
			input = append(input, item)
		}
	}
	if in, ok := turn2["input"].([]any); ok {
		for _, v := range in {
			item, err := itemFromWire(v)
			if err != nil {
				return nil, err
			}
			input = append(input, item)
		}
	}
	req := canon.Request{Model: "fixture-model", Input: input}
	return b.Build(ctx, req, opts)
}

type replayBody struct {
	Model string `json:"model"`
	Store bool   `json:"store"`
	Input []any  `json:"input"`
}

type replayDoc struct {
	Stored struct {
		Input  []json.RawMessage `json:"input"`
		Output []json.RawMessage `json:"output"`
	} `json:"stored"`
	Next struct {
		Input []json.RawMessage `json:"input"`
	} `json:"next"`
}

func buildCodexPreviousResponseReplay(bytes []byte) (*UpstreamRequest, error) {
	var doc replayDoc
	if err := json.Unmarshal(bytes, &doc); err != nil {
		return nil, err
	}
	body := replayBody{Model: "fixture-model", Store: true}
	input := []any{}
	input = append(input, rawSlice(doc.Stored.Input)...)
	input = append(input, rawSlice(doc.Stored.Output)...)
	input = append(input, rawSlice(doc.Next.Input)...)
	body.Input = input
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return &UpstreamRequest{
		Method: "POST",
		URL:    ResponsesURL("http://127.0.0.1:1/v1"),
		Headers: Headers{
			{Name: "Content-Type", Value: "application/json"},
			{Name: "Authorization", Value: "Bearer fixture-key"},
		},
		Body: raw,
	}, nil
}

func rawSlice(items []json.RawMessage) []any {
	out := make([]any, 0, len(items))
	for _, it := range items {
		out = append(out, json.RawMessage(it))
	}
	return out
}
func responsesBodyToRequest(bytes []byte) (canon.Request, []string, error) {
	var vector map[string]any
	if err := json.Unmarshal(bytes, &vector); err != nil {
		return canon.Request{}, nil, err
	}
	var req canon.Request
	var warnings []string
	if m, ok := vector["model"].(string); ok {
		req.Model = canon.ModelID(m)
	} else {
		req.Model = "fixture-model"
	}
	if s, ok := vector["stream"].(bool); ok {
		req.Stream = s
	}
	if instr, ok := vector["instructions"].(string); ok && instr != "" {
		req.Instructions = []canon.Content{canon.TextContent{Text: instr}}
	}
	if max, ok := vector["max_output_tokens"].(float64); ok && max > 0 {
		req.MaxOutputTokens = int(max)
	}
	if text, ok := vector["text"].(map[string]any); ok {
		if format, ok := text["format"].(map[string]any); ok {
			req.Text.Format = textFormatFrom(format)
			if schemaRaw := rawSchema(bytes); schemaRaw != nil {
				req.Text.Format.Schema = schemaRaw
			}
		}
	}
	if temp, ok := vector["temperature"].(float64); ok {
		req.Sampling.Temperature = &temp
	}
	if topP, ok := vector["top_p"].(float64); ok {
		req.Sampling.TopP = &topP
	}
	if stop, ok := vector["stop"].([]any); ok {
		for _, s := range stop {
			if v, ok := s.(string); ok {
				req.Sampling.Stop = append(req.Sampling.Stop, v)
			}
		}
	}
	if p, ok := vector["parallel_tool_calls"].(bool); ok {
		req.Sampling.ParallelToolCalls = &p
	}
	if tier, ok := vector["service_tier"].(string); ok {
		switch tier {
		case "flex":
			req.Sampling.ServiceTier = canon.TierFlex
		case "priority":
			req.Sampling.ServiceTier = canon.TierPriority
		}
	}
	if tools, ok := vector["tools"].([]any); ok {
		for _, tv := range tools {
			tm, ok := tv.(map[string]any)
			if !ok {
				continue
			}
			name, _ := tm["name"].(string)
			desc, _ := tm["description"].(string)
			switch tm["type"] {
			case "custom":
				format := canon.FormatText
				if f, ok := tm["format"].(map[string]any); ok {
					if ft, ok := f["type"].(string); ok && ft == "json" {
						format = canon.FormatJSON
					}
				}
				req.Tools = append(req.Tools, canon.CustomToolDef{Name: canon.ToolName(name), Description: desc, Format: format})
			case "local_shell":
				req.Tools = append(req.Tools, canon.LocalShellToolDef{})
			case "tool_search":
				limit := 0
				if f, ok := tm["max_results"].(float64); ok {
					limit = int(f)
				}
				req.Tools = append(req.Tools, canon.ToolSearchToolDef{Limit: limit})
			default:
				fn := canon.FunctionTool{Name: canon.ToolName(name), Description: desc}
				if params, ok := tm["parameters"]; ok {
					if raw, err := json.Marshal(params); err == nil {
						fn.Parameters = raw
					}
				}
				if strict, ok := tm["strict"].(bool); ok {
					fn.Strict = strict
				}
				req.Tools = append(req.Tools, fn)
			}
		}
	}
	if tc, ok := vector["tool_choice"].(map[string]any); ok {
		switch tc["type"] {
		case "none":
			req.ToolChoice = canon.ToolNone{}
		case "auto":
			req.ToolChoice = canon.ToolAuto{}
		case "required":
			req.ToolChoice = canon.ToolRequired{}
		case "allowed_tools":
			if mode, _ := tc["mode"].(string); mode == "required" {
				req.ToolChoice = canon.ToolRequired{}
				if names := allowedToolNames(tc); len(names) == 1 {
					req.ToolChoice = canon.ToolNamed{Name: canon.ToolName(names[0])}
					req.Tools = filterTools(req.Tools, names)
				}
			} else {
				req.ToolChoice = canon.ToolAuto{}
			}
		default:
			if name, ok := tc["name"].(string); ok {
				req.ToolChoice = canon.ToolNamed{Name: canon.ToolName(name)}
			}
		}
	}
	if in, ok := vector["input"].(string); ok {
		req.Input = []canon.Item{canon.Message{Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: in}}}}
	} else if items, ok := vector["input"].([]any); ok {
		for _, iv := range items {
			im, ok := iv.(map[string]any)
			if !ok {
				continue
			}
			item, w := ingressItem(im)
			if item == nil {
				if w != "" {
					warnings = append(warnings, w)
				}
				continue
			}
			req.Input = append(req.Input, item)
		}
	}
	return req, warnings, nil
}

func rawSchema(bytes []byte) json.RawMessage {
	var doc struct {
		Text struct {
			Format struct {
				Schema json.RawMessage `json:"schema"`
			} `json:"format"`
		} `json:"text"`
	}
	if err := json.Unmarshal(bytes, &doc); err != nil {
		return nil
	}
	return doc.Text.Format.Schema
}

func itemFromWire(v any) (canon.Item, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("codex vector: input item must be an object")
	}
	switch m["type"] {
	case "function_call":
		id, _ := m["id"].(string)
		callID, _ := m["call_id"].(string)
		name, _ := m["name"].(string)
		args, _ := m["arguments"].(string)
		return canon.FunctionCall{ID: canon.ItemID(id), CallID: canon.CallID(callID), Name: canon.ToolName(name), Arguments: []byte(args)}, nil
	case "function_call_output":
		callID, _ := m["call_id"].(string)
		output := ""
		if s, ok := m["output"].(string); ok {
			output = s
		}
		return canon.FunctionOutput{CallID: canon.CallID(callID), Output: []canon.Content{canon.TextContent{Text: output}}}, nil
	case "custom_tool_call":
		id, _ := m["id"].(string)
		callID, _ := m["call_id"].(string)
		name, _ := m["name"].(string)
		input, _ := m["input"].(string)
		return canon.CustomToolCall{ID: canon.ItemID(id), CallID: canon.CallID(callID), Name: canon.ToolName(name), Input: input}, nil
	case "custom_tool_call_output":
		callID, _ := m["call_id"].(string)
		output, _ := m["output"].(string)
		return canon.CustomToolOutput{CallID: canon.CallID(callID), Output: output}, nil
	default:
		return nil, fmt.Errorf("codex vector: unsupported input item type %v", m["type"])
	}
}

func textFormatFrom(format map[string]any) *canon.TextFormat {
	tf := &canon.TextFormat{}
	if t, ok := format["type"].(string); ok {
		tf.Type = t
	}
	if n, ok := format["name"].(string); ok {
		tf.Name = n
	}
	if d, ok := format["description"].(string); ok {
		tf.Description = d
	}
	if s, ok := format["schema"]; ok {
		if raw, err := json.Marshal(s); err == nil {
			tf.Schema = raw
		}
	}
	if strict, ok := format["strict"].(bool); ok {
		tf.Strict = strict
	}
	return tf
}

func allowedToolNames(tc map[string]any) []string {
	var names []string
	if tools, ok := tc["tools"].([]any); ok {
		for _, tv := range tools {
			tm, ok := tv.(map[string]any)
			if !ok {
				continue
			}
			if n, ok := tm["name"].(string); ok {
				names = append(names, n)
			}
		}
	}
	return names
}

func filterTools(tools []canon.Tool, names []string) []canon.Tool {
	allowed := map[string]struct{}{}
	for _, n := range names {
		allowed[n] = struct{}{}
	}
	var out []canon.Tool
	for _, t := range tools {
		if fn, ok := t.(canon.FunctionTool); ok {
			if _, ok := allowed[string(fn.Name)]; ok {
				out = append(out, t)
			}
		}
	}
	return out
}

func ingressItem(m map[string]any) (canon.Item, string) {
	switch m["type"] {
	case "message":
		role, _ := m["role"].(string)
		content := ingressContent(m["content"])
		if content == nil {
			return nil, ""
		}
		return canon.Message{Role: roleFrom(role), Content: content}, ""
	case "function_call":
		id, _ := m["id"].(string)
		callID, _ := m["call_id"].(string)
		name, _ := m["name"].(string)
		args, _ := m["arguments"].(string)
		return canon.FunctionCall{ID: canon.ItemID(id), CallID: canon.CallID(callID), Name: canon.ToolName(name), Arguments: []byte(args)}, ""
	case "function_call_output":
		callID, _ := m["call_id"].(string)
		output := ""
		if s, ok := m["output"].(string); ok {
			output = s
		}
		return canon.FunctionOutput{CallID: canon.CallID(callID), Output: []canon.Content{canon.TextContent{Text: output}}}, ""
	case "custom_tool_call":
		id, _ := m["id"].(string)
		callID, _ := m["call_id"].(string)
		name, _ := m["name"].(string)
		input, _ := m["input"].(string)
		return canon.CustomToolCall{ID: canon.ItemID(id), CallID: canon.CallID(callID), Name: canon.ToolName(name), Input: input}, ""
	case "custom_tool_call_output":
		callID, _ := m["call_id"].(string)
		output, _ := m["output"].(string)
		return canon.CustomToolOutput{CallID: canon.CallID(callID), Output: output}, ""
	case "context_compaction":
		return canon.CompactionMarker{Kind: canon.CompactionExplicit}, ""
	case "compaction_trigger":
		return canon.CompactionMarker{Kind: canon.CompactionAuto}, ""
	case "local_shell_call":
		id, _ := m["id"].(string)
		callID, _ := m["call_id"].(string)
		command := ""
		if action, ok := m["action"].(map[string]any); ok {
			if parts, ok := action["command"].([]any); ok {
				for _, p := range parts {
					if s, ok := p.(string); ok {
						if command != "" {
							command += " "
						}
						command += s
					}
				}
			}
		}
		return canon.LocalShellCall{ID: canon.ItemID(id), CallID: canon.CallID(callID), Command: command}, ""
	case "local_shell_output":
		id, _ := m["id"].(string)
		callID, _ := m["call_id"].(string)
		exit := 0
		if e, ok := m["exit_code"].(float64); ok {
			exit = int(e)
		}
		output, _ := m["output"].(string)
		return canon.LocalShellOutput{ID: canon.ItemID(id), CallID: canon.CallID(callID), ExitCode: exit, Output: output}, ""
	case "tool_search_call":
		id, _ := m["id"].(string)
		callID, _ := m["call_id"].(string)
		query, _ := m["query"].(string)
		return canon.ToolSearchCall{ID: canon.ItemID(id), CallID: canon.CallID(callID), Query: query}, ""
	case "tool_search_output":
		callID, _ := m["call_id"].(string)
		return canon.ToolSearchOutput{CallID: canon.CallID(callID)}, ""
	case "additional_tools":
		return nil, "skip_exotic_item:additional_tools"
	default:
		return nil, "skip_unknown_item:" + fmt.Sprintf("%v", m["type"])
	}
}

func ingressContent(v any) []canon.Content {
	switch x := v.(type) {
	case string:
		return []canon.Content{canon.TextContent{Text: x}}
	case []any:
		var out []canon.Content
		for _, p := range x {
			pm, ok := p.(map[string]any)
			if !ok {
				continue
			}
			switch pm["type"] {
			case "input_text", "text":
				t, _ := pm["text"].(string)
				out = append(out, canon.TextContent{Text: t})
			case "input_image", "image":
				mt, _ := pm["media_type"].(string)
				if mt == "" {
					mt = "image/png"
				}
				data, _ := pm["data"].(string)
				if url, ok := pm["image_url"].(string); ok {
					data = url
				}
				out = append(out, canon.ImageContent{MIMEType: mt, Data: []byte(data)})
			}
		}
		return out
	}
	return nil
}

func applyPatchBridgeEvents(callID, input string) []NormalizedEvent {
	var out []NormalizedEvent
	emit := func(name string, data any) {
		out = append(out, NormalizedEvent{Event: name, Data: data, Ordinal: len(out)})
	}
	snapshot := func(status string) map[string]any {
		return map[string]any{
			"id": "resp_bridge", "object": "response", "created_at": int64(0),
			"status": status, "model": "fixture-model", "output": []any{}, "usage": nil,
		}
	}
	emit("response.created", map[string]any{"response": snapshot("in_progress")})
	emit("response.in_progress", map[string]any{"response": snapshot("in_progress")})
	itemID := "ctc_1"
	open := map[string]any{
		"type": "custom_tool_call", "id": itemID, "call_id": callID,
		"name": "apply_patch", "input": "", "status": "in_progress",
	}
	emit("response.output_item.added", map[string]any{"output_index": 0, "item": open})
	emit("response.custom_tool_call_input.done", map[string]any{"item_id": itemID, "output_index": 0, "input": input})
	final := map[string]any{
		"type": "custom_tool_call", "id": itemID, "call_id": callID,
		"name": "apply_patch", "input": input, "status": "completed",
	}
	emit("response.output_item.done", map[string]any{"output_index": 0, "item": final})
	completed := map[string]any{
		"id": "resp_bridge", "object": "response", "created_at": int64(0),
		"status": "completed", "model": "fixture-model", "output": []any{final}, "usage": nil,
	}
	emit("response.completed", map[string]any{"response": completed})
	return out
}
