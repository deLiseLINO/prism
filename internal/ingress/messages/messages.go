package messages

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"prism/internal/canon"
	"prism/internal/execution"
	"prism/internal/reasonenv"
)

const aliasPrefix = "claude-"

var forwardableHeaders = map[string]bool{
	"x-session-id": true,
	"originator":   true,
	"user-agent":   true,
}

type Ingress struct{}

func (Ingress) Parse(_ context.Context, hr *http.Request) (canon.Request, execution.Facts, error) {
	facts, err := factsFromRequest(hr)
	if err != nil {
		return canon.Request{}, execution.Facts{}, err
	}
	raw, err := io.ReadAll(hr.Body)
	if err != nil {
		return canon.Request{}, execution.Facts{}, &ParseError{Reason: ReasonInvalidJSON, Field: "body", Err: err}
	}
	req, err := decodeRequest(raw)
	if err != nil {
		return canon.Request{}, execution.Facts{}, err
	}
	return req, facts, nil
}

func factsFromRequest(hr *http.Request) (execution.Facts, error) {
	filtered := http.Header{}
	for name, vals := range hr.Header {
		if forwardableHeaders[strings.ToLower(name)] {
			filtered[name] = vals
		}
	}
	forward, err := execution.NewForwardSet(filtered)
	if err != nil {
		return execution.Facts{}, err
	}
	facts := execution.Facts{Client: execution.ClientAnthropic, Forward: forward}
	if v, ok := forward.Get(execution.ForwardSessionID); ok {
		facts.Session = execution.SessionKey(v)
	}
	return facts, nil
}

func ParseModelAlias(model string) (provider, name string, err error) {
	rest, ok := strings.CutPrefix(model, aliasPrefix)
	if !ok {
		return "", "", &ParseError{Reason: ReasonInvalidField, Field: "model", Err: fmt.Errorf("model %q is not a %s alias", model, aliasPrefix)}
	}
	provider, name, ok = strings.Cut(rest, "--")
	if !ok || provider == "" || name == "" {
		return "", "", &ParseError{Reason: ReasonInvalidField, Field: "model", Err: fmt.Errorf("model %q must be %s<provider>--<model>", model, aliasPrefix)}
	}
	return provider, name, nil
}

type ParseReason uint8

const (
	ReasonInvalidJSON ParseReason = iota + 1
	ReasonMissingField
	ReasonInvalidField
)

func (r ParseReason) String() string {
	switch r {
	case ReasonInvalidJSON:
		return "invalid_json"
	case ReasonMissingField:
		return "missing_field"
	case ReasonInvalidField:
		return "invalid_field"
	default:
		return fmt.Sprintf("ParseReason(%d)", uint8(r))
	}
}

type ParseError struct {
	Reason ParseReason
	Field  string
	Err    error
}

func (e *ParseError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("messages ingress: %s %s: %v", e.Reason, e.Field, e.Err)
	}
	return fmt.Sprintf("messages ingress: %s %s", e.Reason, e.Field)
}

func (e *ParseError) Unwrap() error { return e.Err }

func (e *ParseError) HTTPStatus() int { return http.StatusBadRequest }

type wireRequest struct {
	Model         string            `json:"model"`
	System        json.RawMessage   `json:"system"`
	Messages      []json.RawMessage `json:"messages"`
	MaxTokens     *int              `json:"max_tokens"`
	Stream        bool              `json:"stream"`
	Temperature   *float64          `json:"temperature"`
	TopP          *float64          `json:"top_p"`
	StopSequences []string          `json:"stop_sequences"`
	Tools         []json.RawMessage `json:"tools"`
	ToolChoice    json.RawMessage   `json:"tool_choice"`
	Thinking      json.RawMessage   `json:"thinking"`
}

func decodeRequest(raw []byte) (canon.Request, error) {
	var wire wireRequest
	if err := json.Unmarshal(raw, &wire); err != nil {
		return canon.Request{}, &ParseError{Reason: ReasonInvalidJSON, Field: "body", Err: err}
	}
	if wire.Model == "" {
		return canon.Request{}, &ParseError{Reason: ReasonMissingField, Field: "model"}
	}
	if _, _, err := ParseModelAlias(wire.Model); err != nil {
		return canon.Request{}, err
	}
	if wire.MaxTokens == nil {
		return canon.Request{}, &ParseError{Reason: ReasonMissingField, Field: "max_tokens"}
	}
	if *wire.MaxTokens <= 0 {
		return canon.Request{}, &ParseError{Reason: ReasonInvalidField, Field: "max_tokens"}
	}
	if len(wire.Messages) == 0 {
		return canon.Request{}, &ParseError{Reason: ReasonMissingField, Field: "messages"}
	}
	req := canon.Request{
		Model:           canon.ModelID(wire.Model),
		Stream:          wire.Stream,
		MaxOutputTokens: *wire.MaxTokens,
	}
	req.Sampling.Temperature = wire.Temperature
	req.Sampling.TopP = wire.TopP
	req.Sampling.Stop = wire.StopSequences
	if text, ok := systemText(wire.System); ok {
		req.Instructions = append(req.Instructions, canon.TextContent{Text: text})
	}
	for _, rawMsg := range wire.Messages {
		var msg struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(rawMsg, &msg); err != nil {
			return canon.Request{}, &ParseError{Reason: ReasonInvalidJSON, Field: "messages", Err: err}
		}
		switch msg.Role {
		case "user":
			items, err := userItems(msg.Content)
			if err != nil {
				return canon.Request{}, err
			}
			req.Input = append(req.Input, items...)
		case "assistant":
			items, err := assistantItems(msg.Content)
			if err != nil {
				return canon.Request{}, err
			}
			req.Input = append(req.Input, items...)
		case "system":
			if text, ok := systemText(msg.Content); ok {
				req.Instructions = append(req.Instructions, canon.TextContent{Text: text})
			}
		default:
			return canon.Request{}, &ParseError{Reason: ReasonInvalidField, Field: "messages.role", Err: fmt.Errorf("unsupported role %q", msg.Role)}
		}
	}
	tools, err := decodeTools(wire.Tools)
	if err != nil {
		return canon.Request{}, err
	}
	req.Tools = tools
	if err := decodeToolChoice(wire.ToolChoice, &req); err != nil {
		return canon.Request{}, err
	}
	reasoning, err := decodeThinking(wire.Thinking)
	if err != nil {
		return canon.Request{}, err
	}
	req.Reasoning = reasoning
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

type contentBlock struct {
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
	URL       string          `json:"url"`
}

func userItems(raw json.RawMessage) ([]canon.Item, error) {
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
		return items, nil
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, &ParseError{Reason: ReasonInvalidField, Field: "messages.content", Err: fmt.Errorf("content is neither string nor block array")}
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
		case "web_search_tool_result":
			var results []string
			if len(b.Content) > 0 {
				var parts []contentBlock
				if err := json.Unmarshal(b.Content, &parts); err == nil {
					for _, p := range parts {
						if p.Type == "web_search_result" {
							results = append(results, p.URL)
						}
					}
				}
			}
			pending = append(pending, canon.TextContent{Text: "[web search results: " + strings.Join(results, " ") + "]"})
		default:
			continue
		}
	}
	flush()
	return items, nil
}

func toolResultContent(b contentBlock) []canon.Content {
	var s string
	if err := json.Unmarshal(b.Content, &s); err == nil {
		if b.IsError {
			return []canon.Content{canon.TextContent{Text: "[tool error] " + s}}
		}
		return []canon.Content{canon.TextContent{Text: s}}
	}
	var out []canon.Content
	if len(b.Content) > 0 {
		var parts []contentBlock
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

func assistantItems(raw json.RawMessage) ([]canon.Item, error) {
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
		return items, nil
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, &ParseError{Reason: ReasonInvalidField, Field: "messages.content", Err: fmt.Errorf("content is neither string nor block array")}
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			pendingText = append(pendingText, canon.TextContent{Text: b.Text})
		case "thinking":
			if b.Thinking == "" && b.Signature == "" {
				continue
			}
			if strings.HasPrefix(b.Signature, reasonenv.Prefix) {
				if _, ok := reasonenv.Decode(b.Signature); !ok {
					return nil, &ParseError{Reason: ReasonInvalidField, Field: "messages.content", Err: fmt.Errorf("malformed prism thinking signature")}
				}
			}
			flush()
			items = append(items, canon.ReasoningItem{ID: canon.ItemID(b.ID), Content: b.Thinking, Signature: b.Signature})
		case "redacted_thinking":
			flush()
			items = append(items, canon.ReasoningItem{ID: canon.ItemID(b.ID), Signature: reasonenv.EncodeRedacted([]string{b.Data})})
		case "server_tool_use":
			flush()
			query := ""
			if len(b.Input) > 0 {
				var input map[string]any
				if err := json.Unmarshal(b.Input, &input); err == nil {
					if q, ok := input["query"].(string); ok {
						query = q
					}
				}
			}
			pendingText = append(pendingText, canon.TextContent{Text: "[server tool " + b.Name + ": " + query + "]"})
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
	return items, nil
}

func decodeTools(raw []json.RawMessage) ([]canon.Tool, error) {
	var tools []canon.Tool
	for _, rawTool := range raw {
		var t struct {
			Type        string          `json:"type"`
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"input_schema"`
		}
		if err := json.Unmarshal(rawTool, &t); err != nil {
			return nil, &ParseError{Reason: ReasonInvalidJSON, Field: "tools", Err: err}
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

func decodeToolChoice(raw json.RawMessage, req *canon.Request) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var choice struct {
		Type                   string `json:"type"`
		Name                   string `json:"name"`
		DisableParallelToolUse bool   `json:"disable_parallel_tool_use"`
	}
	if err := json.Unmarshal(raw, &choice); err != nil {
		return &ParseError{Reason: ReasonInvalidJSON, Field: "tool_choice", Err: err}
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
			return &ParseError{Reason: ReasonMissingField, Field: "tool_choice.name"}
		}
		req.ToolChoice = canon.ToolNamed{Name: canon.ToolName(choice.Name)}
	default:
		return &ParseError{Reason: ReasonInvalidField, Field: "tool_choice.type", Err: fmt.Errorf("unsupported tool_choice type %q", choice.Type)}
	}
	return nil
}

func decodeThinking(raw json.RawMessage) (canon.ReasoningConfig, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return canon.ReasoningConfig{}, nil
	}
	var t struct {
		Type         string `json:"type"`
		BudgetTokens *int   `json:"budget_tokens"`
	}
	if err := json.Unmarshal(raw, &t); err != nil {
		return canon.ReasoningConfig{}, &ParseError{Reason: ReasonInvalidJSON, Field: "thinking", Err: err}
	}
	switch t.Type {
	case "disabled":
		return canon.ReasoningConfig{}, nil
	case "enabled":
		if t.BudgetTokens == nil {
			return canon.ReasoningConfig{Summary: canon.SummaryAuto}, nil
		}
		return canon.ReasoningConfig{Effort: effortForBudget(*t.BudgetTokens), Summary: canon.SummaryAuto}, nil
	case "adaptive":
		return canon.ReasoningConfig{Summary: canon.SummaryAuto}, nil
	default:
		return canon.ReasoningConfig{}, &ParseError{Reason: ReasonInvalidField, Field: "thinking.type", Err: fmt.Errorf("unsupported thinking type %q", t.Type)}
	}
}

func effortForBudget(budget int) canon.ReasoningEffort {
	switch {
	case budget <= 4096:
		return canon.EffortLow
	case budget <= 16384:
		return canon.EffortMedium
	default:
		return canon.EffortHigh
	}
}
