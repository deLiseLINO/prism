package conformance

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"prism/internal/canon"
)

func DecodeMessagesRequest(raw []byte) (canon.Request, error) {
	var body struct {
		Model         string            `json:"model"`
		System        json.RawMessage   `json:"system"`
		Messages      []json.RawMessage `json:"messages"`
		MaxTokens     int               `json:"max_tokens"`
		Stream        bool              `json:"stream"`
		Temperature   *float64          `json:"temperature"`
		TopP          *float64          `json:"top_p"`
		StopSequences []string          `json:"stop_sequences"`
		Tools         []json.RawMessage `json:"tools"`
		ToolChoice    json.RawMessage   `json:"tool_choice"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return canon.Request{}, fmt.Errorf("anthropic messages decode: %v", err)
	}
	if body.Model == "" {
		return canon.Request{}, fmt.Errorf("anthropic messages: model is required")
	}
	if len(body.Messages) == 0 {
		return canon.Request{}, fmt.Errorf("anthropic messages: messages must be a non-empty array")
	}
	req := canon.Request{
		Model:           canon.ModelID(body.Model),
		Stream:          body.Stream,
		MaxOutputTokens: body.MaxTokens,
	}
	req.Sampling.Temperature = body.Temperature
	req.Sampling.TopP = body.TopP
	req.Sampling.Stop = body.StopSequences

	if text, ok := systemText(body.System); ok {
		req.Instructions = append(req.Instructions, canon.TextContent{Text: text})
	}

	var input []canon.Item
	for _, rawMsg := range body.Messages {
		var msg struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(rawMsg, &msg); err != nil {
			return canon.Request{}, fmt.Errorf("anthropic messages: message decode: %v", err)
		}
		switch msg.Role {
		case "user":
			input = append(input, userMessageItems(msg.Content)...)
		case "assistant":
			input = append(input, assistantMessageItems(msg.Content)...)
		case "system":
			if text, ok := systemText(msg.Content); ok {
				req.Instructions = append(req.Instructions, canon.TextContent{Text: text})
			}
		default:
			return canon.Request{}, fmt.Errorf("anthropic messages: unsupported message role %q", msg.Role)
		}
	}
	req.Input = input

	tools, err := toolsFromMessages(body.Tools)
	if err != nil {
		return canon.Request{}, err
	}
	req.Tools = tools
	if err := toolChoiceFromMessages(body.ToolChoice, &req); err != nil {
		return canon.Request{}, err
	}
	return req, nil
}

func systemText(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, s != ""
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", false
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, "\n\n"), true
}

type messagesBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
	MediaType string          `json:"media_type"`
	Data      string          `json:"data"`
	Source    json.RawMessage `json:"source"`
	Title     string          `json:"title"`
	Thinking  string          `json:"thinking"`
	Signature string          `json:"signature"`
}

func userMessageItems(raw json.RawMessage) []canon.Item {
	var items []canon.Item
	var pending []canon.Content
	flush := func() {
		if len(pending) > 0 {
			items = append(items, canon.Message{Role: canon.RoleUser, Content: pending})
			pending = nil
		}
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s != "" {
			items = append(items, canon.Message{Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: s}}})
		}
		return items
	}
	var blocks []messagesBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return items
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			pending = append(pending, canon.TextContent{Text: b.Text})
		case "image":
			if img := imageContent(b.Source, b.MediaType, b.Data); img != nil {
				pending = append(pending, img)
			}
		case "document":
			title := ""
			if b.Title != "" {
				title = ": " + b.Title
			}
			pending = append(pending, canon.TextContent{Text: "[document" + title + "]"})
		case "tool_result":
			flush()
			if b.ToolUseID == "" {
				continue
			}
			items = append(items, canon.FunctionOutput{CallID: canon.CallID(b.ToolUseID), Output: toolResultContent(b)})
		default:
			continue
		}
	}
	flush()
	return items
}

func toolResultContent(b messagesBlock) []canon.Content {
	var s string
	if err := json.Unmarshal(b.Content, &s); err == nil {
		if b.IsError {
			return []canon.Content{canon.TextContent{Text: "[tool error] " + s}}
		}
		return []canon.Content{canon.TextContent{Text: s}}
	}
	var out []canon.Content
	if len(b.Content) > 0 {
		var parts []messagesBlock
		if err := json.Unmarshal(b.Content, &parts); err != nil {
			return out
		}
		for _, p := range parts {
			switch p.Type {
			case "text":
				out = append(out, canon.TextContent{Text: p.Text})
			case "image":
				if img := imageContent(p.Source, p.MediaType, p.Data); img != nil {
					out = append(out, img)
				}
			case "document":
				title := ""
				if p.Title != "" {
					title = ": " + p.Title
				}
				out = append(out, canon.TextContent{Text: "[document" + title + "]"})
			}
		}
	}
	if b.IsError {
		out = append([]canon.Content{canon.TextContent{Text: "[tool error]"}}, out...)
	}
	return out
}

func imageContent(source json.RawMessage, mediaType, data string) canon.Content {
	if len(source) > 0 && string(source) != "null" {
		var src struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
		}
		if err := json.Unmarshal(source, &src); err == nil && src.Type == "base64" {
			raw, err := base64.StdEncoding.DecodeString(src.Data)
			if err != nil {
				return nil
			}
			mt := src.MediaType
			if mt == "" {
				mt = "image/png"
			}
			return canon.ImageContent{MIMEType: mt, Data: raw}
		}
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return nil
	}
	mt := mediaType
	if mt == "" {
		mt = "image/png"
	}
	return canon.ImageContent{MIMEType: mt, Data: raw}
}

func assistantMessageItems(raw json.RawMessage) []canon.Item {
	var items []canon.Item
	var pendingText []canon.Content
	flush := func() {
		if len(pendingText) > 0 {
			items = append(items, canon.Message{Role: canon.RoleAssistant, Content: pendingText})
			pendingText = nil
		}
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s != "" {
			items = append(items, canon.Message{Role: canon.RoleAssistant, Content: []canon.Content{canon.TextContent{Text: s}}})
		}
		return items
	}
	var blocks []messagesBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return items
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			pendingText = append(pendingText, canon.TextContent{Text: b.Text})
		case "tool_use":
			flush()
			if b.ID == "" || b.Name == "" {
				continue
			}
			arguments := b.Input
			if len(arguments) == 0 {
				arguments = json.RawMessage(`{}`)
			}
			items = append(items, canon.FunctionCall{
				CallID:    canon.CallID(b.ID),
				Name:      canon.ToolName(b.Name),
				Arguments: arguments,
			})
		default:
			continue
		}
	}
	flush()
	return items
}

func toolsFromMessages(raw []json.RawMessage) ([]canon.Tool, error) {
	var tools []canon.Tool
	for _, rawTool := range raw {
		var t struct {
			Type        string          `json:"type"`
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"input_schema"`
		}
		if err := json.Unmarshal(rawTool, &t); err != nil {
			return nil, fmt.Errorf("anthropic messages: tool decode: %v", err)
		}
		if strings.HasPrefix(t.Type, "web_search") {
			continue
		}
		if t.Name == "" || len(t.InputSchema) == 0 {
			continue
		}
		tools = append(tools, canon.FunctionTool{
			Name:        canon.ToolName(t.Name),
			Description: t.Description,
			Parameters:  t.InputSchema,
		})
	}
	return tools, nil
}

func toolChoiceFromMessages(raw json.RawMessage, req *canon.Request) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var choice struct {
		Type                   string `json:"type"`
		Name                   string `json:"name"`
		DisableParallelToolUse bool   `json:"disable_parallel_tool_use"`
	}
	if err := json.Unmarshal(raw, &choice); err != nil {
		return fmt.Errorf("anthropic messages: tool_choice decode: %v", err)
	}
	if choice.DisableParallelToolUse {
		v := false
		req.Sampling.ParallelToolCalls = &v
	}
	switch choice.Type {
	case "auto":
		req.ToolChoice = canon.ToolAuto{}
	case "none":
		req.ToolChoice = canon.ToolNone{}
	case "any":
		req.ToolChoice = canon.ToolRequired{}
	case "tool":
		if choice.Name == "" {
			return fmt.Errorf("anthropic messages: tool_choice.tool requires a name")
		}
		req.ToolChoice = canon.ToolNamed{Name: canon.ToolName(choice.Name)}
	}
	return nil
}

func TranslateResponsesEvents(events []NormalizedEvent) []NormalizedEvent {
	var out []NormalizedEvent
	ordinal := 0
	emit := func(name string, data any) {
		out = append(out, NormalizedEvent{Event: name, Data: data, Ordinal: ordinal})
		ordinal++
	}
	type openBlock struct {
		kind  string
		index int
	}
	var open *openBlock
	started := false
	blockIndex := 0
	sawToolUse := false
	ensureStarted := func() {
		if started {
			return
		}
		started = true
		emit("message_start", map[string]any{"type": "message_start", "message": messageSnapshot()})
	}
	closeOpenBlock := func() {
		if open == nil {
			return
		}
		emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": open.index})
		open = nil
	}
	ensureBlock := func(kind string) {
		ensureStarted()
		if open != nil && open.kind == kind {
			return
		}
		closeOpenBlock()
		index := blockIndex
		blockIndex++
		var contentBlock map[string]any
		if kind == "text" {
			contentBlock = map[string]any{"type": "text", "text": ""}
		} else {
			contentBlock = map[string]any{"type": "thinking", "thinking": "", "signature": ""}
		}
		emit("content_block_start", map[string]any{"type": "content_block_start", "index": index, "content_block": contentBlock})
		open = &openBlock{kind: kind, index: index}
	}
	finish := func(stopReason string, usage any) {
		ensureStarted()
		closeOpenBlock()
		emit("message_delta", map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil},
			"usage": anthropicUsage(usage),
		})
		emit("message_stop", map[string]any{"type": "message_stop"})
	}
	failStatus := func(status int, message string) {
		closeOpenBlock()
		emit("error", anthropicErrorBody(status, message))
	}

	for _, ev := range events {
		data, ok := ev.Data.(map[string]any)
		if !ok {
			continue
		}
		switch ev.Event {
		case "response.created":
			continue
		case "response.heartbeat":
			continue
		case "response.output_text.delta":
			delta, _ := data["delta"].(string)
			if delta == "" {
				continue
			}
			ensureBlock("text")
			emit("content_block_delta", map[string]any{
				"type": "content_block_delta", "index": open.index,
				"delta": map[string]any{"type": "text_delta", "text": delta},
			})
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			delta, _ := data["delta"].(string)
			if delta == "" {
				continue
			}
			ensureBlock("thinking")
			emit("content_block_delta", map[string]any{
				"type": "content_block_delta", "index": open.index,
				"delta": map[string]any{"type": "thinking_delta", "thinking": delta},
			})
		case "response.output_item.added":
			item, _ := data["item"].(map[string]any)
			if item == nil || item["type"] != "function_call" {
				continue
			}
			ensureStarted()
			closeOpenBlock()
			sawToolUse = true
			index := blockIndex
			blockIndex++
			name, _ := item["name"].(string)
			callID, _ := item["call_id"].(string)
			emit("content_block_start", map[string]any{
				"type": "content_block_start", "index": index,
				"content_block": map[string]any{"type": "tool_use", "id": callID, "name": name, "input": map[string]any{}},
			})
			open = &openBlock{kind: "tool_use", index: index}
		case "response.function_call_arguments.delta":
			delta, _ := data["delta"].(string)
			if delta == "" || open == nil || open.kind != "tool_use" {
				continue
			}
			emit("content_block_delta", map[string]any{
				"type": "content_block_delta", "index": open.index,
				"delta": map[string]any{"type": "input_json_delta", "partial_json": delta},
			})
		case "response.output_item.done":
			item, _ := data["item"].(map[string]any)
			if item == nil || open == nil {
				continue
			}
			if (open.kind == "tool_use" && item["type"] == "function_call") ||
				(open.kind == "text" && item["type"] == "message") ||
				(open.kind == "thinking" && item["type"] == "reasoning") {
				closeOpenBlock()
			}
		case "response.completed":
			response, _ := data["response"].(map[string]any)
			endTurn := true
			if v, ok := response["end_turn"].(bool); ok {
				endTurn = v
			}
			if !endTurn && !sawToolUse {
				failStatus(529, "upstream turn ended without a final answer")
				continue
			}
			reason := "end_turn"
			if sawToolUse {
				reason = "tool_use"
			}
			finish(reason, response["usage"])
		case "response.incomplete":
			response, _ := data["response"].(map[string]any)
			details, _ := response["incomplete_details"].(map[string]any)
			reason, _ := details["reason"].(string)
			switch reason {
			case "max_output_tokens":
				finish("max_tokens", response["usage"])
			case "content_filter":
				finish("refusal", response["usage"])
			default:
				message := "upstream response was incomplete"
				if m, ok := details["message"].(string); ok && strings.TrimSpace(m) != "" {
					message = m
				}
				failStatus(529, message)
			}
		case "response.failed":
			response, _ := data["response"].(map[string]any)
			errObj, _ := response["error"].(map[string]any)
			message := "upstream request failed"
			if m, ok := errObj["message"].(string); ok && m != "" {
				message = m
			}
			failStatus(httpStatusFromTerminalError(errObj), message)
		}
	}
	return out
}

func messageSnapshot() map[string]any {
	return map[string]any{
		"id":            "msg_fixture",
		"type":          "message",
		"role":          "assistant",
		"content":       []any{},
		"model":         "fixture-model",
		"stop_reason":   nil,
		"stop_sequence": nil,
		"usage":         map[string]any{"input_tokens": 0, "output_tokens": 0},
	}
}

func anthropicUsage(usage any) map[string]any {
	var input, output, cached, cacheWrite float64
	if u, ok := usage.(map[string]any); ok {
		input, _ = u["input_tokens"].(float64)
		output, _ = u["output_tokens"].(float64)
		if details, ok := u["input_tokens_details"].(map[string]any); ok {
			cached, _ = details["cached_tokens"].(float64)
			cacheWrite, _ = details["cache_write_tokens"].(float64)
		}
	}
	return map[string]any{
		"input_tokens":                max(0, input-cached-cacheWrite),
		"output_tokens":               output,
		"cache_read_input_tokens":     cached,
		"cache_creation_input_tokens": cacheWrite,
	}
}

func anthropicErrorBody(status int, message string) map[string]any {
	typ := anthropicErrorType(status)
	if isTransientUpstreamStatus(status) {
		typ = "overloaded_error"
	}
	return map[string]any{
		"type":  "error",
		"error": map[string]any{"type": typ, "message": message},
	}
}

func anthropicErrorType(status int) string {
	switch status {
	case 400:
		return "invalid_request_error"
	case 401:
		return "authentication_error"
	case 402:
		return "billing_error"
	case 403:
		return "permission_error"
	case 404:
		return "not_found_error"
	case 409:
		return "conflict_error"
	case 413:
		return "request_too_large"
	case 429:
		return "rate_limit_error"
	case 504:
		return "timeout_error"
	case 529:
		return "overloaded_error"
	default:
		if status >= 500 {
			return "api_error"
		}
		return "invalid_request_error"
	}
}

func isTransientUpstreamStatus(status int) bool {
	switch status {
	case 500, 502, 503, 504, 520, 521, 522:
		return true
	}
	return false
}

func httpStatusFromTerminalError(err map[string]any) int {
	if len(err) == 0 {
		return 502
	}
	code, _ := err["code"].(string)
	typ, _ := err["type"].(string)
	switch {
	case code == "client_closed_request" || code == "client_cancelled":
		return 499
	case typ == "rate_limit_error" || code == "rate_limit_exceeded":
		return 429
	case typ == "authentication_error" || code == "invalid_api_key":
		return 401
	case typ == "permission_error" || code == "permission_denied" || code == "subscription_required":
		return 403
	case typ == "insufficient_quota" || code == "insufficient_quota":
		return 429
	case typ == "server_error" && code == "server_is_overloaded":
		return 503
	case typ == "invalid_request_error":
		return 400
	case typ == "proxy_error":
		return 500
	}
	message, _ := err["message"].(string)
	structuredServer := typ == "server_error" || code == "upstream_server_error"
	if message != "" {
		inferred := inferHttpStatusFromAdapterMessage(message)
		if structuredServer && inferred == 400 {
			return 502
		}
		return inferred
	}
	return 502
}

func inferHttpStatusFromAdapterMessage(message string) int {
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "resource_exhausted") ||
		strings.Contains(lower, "resource exhausted") ||
		strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "too many requests") ||
		strings.Contains(lower, "throttling"):
		return 429
	case strings.Contains(lower, "failed_precondition") || strings.Contains(lower, "failed precondition"):
		return 400
	case strings.Contains(lower, "unavailable") ||
		strings.Contains(lower, "overloaded") ||
		strings.Contains(lower, "temporarily") ||
		strings.Contains(lower, "server is busy"):
		return 503
	case strings.Contains(lower, "invalid") ||
		strings.Contains(lower, "not found") ||
		strings.Contains(lower, "unsupported") ||
		strings.Contains(lower, "malformed") ||
		strings.Contains(lower, "unimplemented"):
		return 400
	case strings.Contains(lower, "timed out") ||
		strings.Contains(lower, "timeout") ||
		strings.Contains(lower, "etimedout") ||
		strings.Contains(lower, "deadline"):
		return 504
	}
	return 502
}
