package chat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/execution"
)

type Reason uint8

const (
	ReasonInvalidJSON Reason = iota + 1
	ReasonMissingField
	ReasonInvalidField
)

func (r Reason) String() string {
	switch r {
	case ReasonInvalidJSON:
		return "invalid_json"
	case ReasonMissingField:
		return "missing_field"
	case ReasonInvalidField:
		return "invalid_field"
	default:
		return fmt.Sprintf("reason(%d)", uint8(r))
	}
}

type ParseError struct {
	Reason Reason
	Field  string
	Err    error
}

func (e *ParseError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("chat ingress: %s: %s: %v", e.Field, e.Reason, e.Err)
	}
	return fmt.Sprintf("chat ingress: %s: %s", e.Field, e.Reason)
}

func (e *ParseError) Unwrap() error { return e.Err }

func missing(field string) error {
	return &ParseError{Reason: ReasonMissingField, Field: field}
}

func invalid(field string, err error) error {
	return &ParseError{Reason: ReasonInvalidField, Field: field, Err: err}
}

type Ingress struct{}

func (Ingress) Parse(_ context.Context, hr *http.Request) (canon.Request, execution.Facts, error) {
	facts, err := factsFrom(hr)
	if err != nil {
		return canon.Request{}, execution.Facts{}, err
	}
	raw, err := io.ReadAll(hr.Body)
	if err != nil {
		return canon.Request{}, execution.Facts{}, &ParseError{Reason: ReasonInvalidJSON, Field: "body", Err: err}
	}
	var b body
	if err := json.Unmarshal(raw, &b); err != nil {
		return canon.Request{}, execution.Facts{}, &ParseError{Reason: ReasonInvalidJSON, Field: "body", Err: err}
	}
	if b.Model == "" {
		return canon.Request{}, execution.Facts{}, missing("model")
	}
	if len(b.Messages) == 0 {
		return canon.Request{}, execution.Facts{}, missing("messages")
	}
	items, err := itemsFrom(b.Messages)
	if err != nil {
		return canon.Request{}, execution.Facts{}, err
	}
	sampling, err := samplingFrom(b)
	if err != nil {
		return canon.Request{}, execution.Facts{}, err
	}
	req := canon.Request{
		Model:           canon.ModelID(b.Model),
		Stream:          b.Stream,
		Input:           items,
		MaxOutputTokens: maxTokensFrom(b),
		Sampling:        sampling,
		Reasoning:       reasoningFrom(b.ReasoningEffort),
	}
	switch b.Verbosity {
	case "low":
		req.Text.Verbosity = canon.VerbosityLow
	case "medium":
		req.Text.Verbosity = canon.VerbosityMedium
	case "high":
		req.Text.Verbosity = canon.VerbosityHigh
	case "":
	default:
		return canon.Request{}, execution.Facts{}, invalid("verbosity", fmt.Errorf("unsupported verbosity %q", b.Verbosity))
	}
	switch b.ServiceTier {
	case "default":
		req.Sampling.ServiceTier = canon.TierDefault
	case "flex":
		req.Sampling.ServiceTier = canon.TierFlex
	case "priority":
		req.Sampling.ServiceTier = canon.TierPriority
	}
	if req.Text.Format, err = responseFormatFrom(b.ResponseFormat); err != nil {
		return canon.Request{}, execution.Facts{}, err
	}
	if req.Tools, err = toolsFrom(b.Tools); err != nil {
		return canon.Request{}, execution.Facts{}, err
	}
	if req.ToolChoice, err = toolChoiceFrom(b.ToolChoice); err != nil {
		return canon.Request{}, execution.Facts{}, err
	}
	if allowed, ok := req.ToolChoice.(canon.ToolAllowed); ok {
		selected := make([]canon.Tool, 0, len(allowed.Tools))
		for _, name := range allowed.Tools {
			found := false
			for _, tool := range req.Tools {
				if tool := tool.(canon.FunctionTool); tool.Name == name {
					selected = append(selected, tool)
					found = true
					break
				}
			}
			if !found {
				return canon.Request{}, execution.Facts{}, invalid("tool_choice", fmt.Errorf("allowed tool %q is not declared", name))
			}
		}
		req.Tools = selected
		if allowed.Mode == canon.AllowedRequired {
			req.ToolChoice = canon.ToolRequired{}
		} else {
			req.ToolChoice = canon.ToolAuto{}
		}
	}
	return req, facts, nil
}

var forwardableNames = map[string]bool{
	"x-session-id": true,
	"originator":   true,
	"user-agent":   true,
}

func factsFrom(hr *http.Request) (execution.Facts, error) {
	h := make(http.Header, len(hr.Header))
	for k, v := range hr.Header {
		if forwardableNames[strings.ToLower(k)] {
			h[k] = v
		}
	}
	forward, err := execution.NewForwardSet(h)
	if err != nil {
		return execution.Facts{}, invalid("header", err)
	}
	return execution.Facts{Client: execution.ClientOMP, Forward: forward}, nil
}

func maxTokensFrom(b body) int {
	if n, ok := numberField(b.MaxCompletionTokens); ok {
		return n
	}
	if n, ok := numberField(b.MaxTokens); ok {
		return n
	}
	return 0
}

func numberField(raw json.RawMessage) (int, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, false
	}
	f, ok := v.(float64)
	if !ok || f != math.Trunc(f) || f < math.MinInt || f > math.MaxInt {
		return 0, false
	}
	return int(f), true
}

func reasoningFrom(effort *string) canon.ReasoningConfig {
	if effort == nil {
		return canon.ReasoningConfig{}
	}
	switch *effort {
	case "minimal":
		return canon.ReasoningConfig{Effort: canon.EffortMinimal}
	case "off", "none":
		return canon.ReasoningConfig{Effort: canon.EffortOff}
	case "low":
		return canon.ReasoningConfig{Effort: canon.EffortLow}
	case "medium":
		return canon.ReasoningConfig{Effort: canon.EffortMedium}
	case "high":
		return canon.ReasoningConfig{Effort: canon.EffortHigh}
	case "xhigh":
		return canon.ReasoningConfig{Effort: canon.EffortXHigh}
	case "max":
		return canon.ReasoningConfig{Effort: canon.EffortMax}
	}
	return canon.ReasoningConfig{}
}

func samplingFrom(b body) (canon.Sampling, error) {
	stop, err := stopFrom(b.Stop)
	if err != nil {
		return canon.Sampling{}, err
	}
	return canon.Sampling{
		Temperature:       b.Temperature,
		TopP:              b.TopP,
		Stop:              stop,
		ParallelToolCalls: b.ParallelToolCalls,
		PresencePenalty:   b.PresencePenalty,
		FrequencyPenalty:  b.FrequencyPenalty,
	}, nil
}

// stopFrom accepts both shapes the wire allows: one string or a list.
func stopFrom(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		if one == "" {
			return nil, nil
		}
		return []string{one}, nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil, invalid("stop", fmt.Errorf("stop must be a string or an array of strings"))
	}
	kept := many[:0:0]
	for _, m := range many {
		if m != "" {
			kept = append(kept, m)
		}
	}
	if len(kept) == 0 {
		return nil, nil
	}
	return kept, nil
}

func responseFormatFrom(raw json.RawMessage) (*canon.TextFormat, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var rf struct {
		Type       string `json:"type"`
		JSONSchema *struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Schema      json.RawMessage `json:"schema"`
			Strict      *bool           `json:"strict"`
		} `json:"json_schema"`
	}
	if err := json.Unmarshal(raw, &rf); err != nil {
		return nil, invalid("response_format", err)
	}
	switch rf.Type {
	case "text", "json_object":
		return &canon.TextFormat{Type: rf.Type}, nil
	case "json_schema":
		if rf.JSONSchema == nil {
			return nil, missing("response_format.json_schema")
		}
		if rf.JSONSchema.Name == "" {
			return nil, missing("response_format.json_schema.name")
		}
		var schema map[string]json.RawMessage
		if json.Unmarshal(rf.JSONSchema.Schema, &schema) != nil || schema == nil {
			return nil, invalid("response_format.json_schema.schema", fmt.Errorf("schema must be an object"))
		}
		return &canon.TextFormat{
			Type:        "json_schema",
			Name:        rf.JSONSchema.Name,
			Description: rf.JSONSchema.Description,
			Schema:      rf.JSONSchema.Schema,
			Strict:      rf.JSONSchema.Strict,
		}, nil
	}
	return nil, invalid("response_format.type", fmt.Errorf("unsupported response_format type %q", rf.Type))
}

func itemsFrom(msgs []message) ([]canon.Item, error) {
	items := make([]canon.Item, 0, len(msgs))
	for i, m := range msgs {
		switch m.Role {
		case "system":
			content, err := contentFrom(i, m.Content)
			if err != nil {
				return nil, err
			}
			items = append(items, canon.Message{Role: canon.RoleSystem, Content: content})
		case "user":
			content, err := contentFrom(i, m.Content)
			if err != nil {
				return nil, err
			}
			items = append(items, canon.Message{Role: canon.RoleUser, Content: content})
		case "developer":
			content, err := contentFrom(i, m.Content)
			if err != nil {
				return nil, err
			}
			items = append(items, canon.Message{Role: canon.RoleDeveloper, Content: content})
		case "assistant":
			content, err := contentFrom(i, m.Content)
			if err != nil {
				return nil, err
			}
			items = append(items, canon.Message{Role: canon.RoleAssistant, Content: content})
			for j, tc := range m.ToolCalls {
				call, err := functionCallFrom(i, j, tc)
				if err != nil {
					return nil, err
				}
				items = append(items, call)
			}
		case "tool":
			if m.ToolCallID == "" {
				return nil, missing(fmt.Sprintf("messages[%d].tool_call_id", i))
			}
			content, err := contentFrom(i, m.Content)
			if err != nil {
				return nil, err
			}
			items = append(items, canon.FunctionOutput{CallID: canon.CallID(m.ToolCallID), Output: content})
		default:
			return nil, invalid(fmt.Sprintf("messages[%d].role", i), fmt.Errorf("unsupported role %q", m.Role))
		}
	}
	return items, nil
}

func functionCallFrom(msgIdx, callIdx int, tc toolCall) (canon.FunctionCall, error) {
	field := fmt.Sprintf("messages[%d].tool_calls[%d]", msgIdx, callIdx)
	if tc.ID == "" {
		return canon.FunctionCall{}, missing(field + ".id")
	}
	if tc.Type != "function" {
		return canon.FunctionCall{}, invalid(field+".type", fmt.Errorf("unsupported tool call type %q", tc.Type))
	}
	if tc.Function.Name == "" {
		return canon.FunctionCall{}, missing(field + ".function.name")
	}
	return canon.FunctionCall{
		CallID:    canon.CallID(tc.ID),
		Name:      canon.ToolName(tc.Function.Name),
		Arguments: []byte(tc.Function.Arguments),
	}, nil
}

type contentPart struct {
	Type     string       `json:"type"`
	Text     string       `json:"text"`
	Refusal  string       `json:"refusal"`
	ImageURL *imageURLRef `json:"image_url"`
}

type imageURLRef struct {
	URL    string `json:"url"`
	Detail string `json:"detail"`
}

func contentFrom(msgIdx int, raw json.RawMessage) ([]canon.Content, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return []canon.Content{canon.TextContent{Text: text}}, nil
	}
	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, invalid(fmt.Sprintf("messages[%d].content", msgIdx), err)
	}
	out := make([]canon.Content, 0, len(parts))
	for i, p := range parts {
		field := fmt.Sprintf("messages[%d].content[%d]", msgIdx, i)
		switch p.Type {
		case "text":
			out = append(out, canon.TextContent{Text: p.Text})
		case "refusal":
			out = append(out, canon.TextContent{Text: p.Refusal})
		case "image_url":
			if p.ImageURL == nil {
				return nil, missing(field + ".image_url")
			}
			img, err := imageFrom(p.ImageURL)
			if err != nil {
				return nil, invalid(field+".image_url.url", err)
			}
			out = append(out, img)
		default:
			return nil, invalid(field+".type", fmt.Errorf("unsupported content part type %q", p.Type))
		}
	}
	return out, nil
}

func imageFrom(ref *imageURLRef) (canon.ImageContent, error) {
	const prefix = "data:"
	if !strings.HasPrefix(ref.URL, prefix) {
		return canon.ImageContent{}, fmt.Errorf("image url %q is not a data URL", ref.URL)
	}
	rest := ref.URL[len(prefix):]
	comma := strings.IndexByte(rest, ',')
	if comma < 0 {
		return canon.ImageContent{}, fmt.Errorf("image data URL %q has no payload", ref.URL)
	}
	meta := rest[:comma]
	payload := rest[comma+1:]
	if !strings.HasSuffix(meta, ";base64") {
		return canon.ImageContent{}, fmt.Errorf("image data URL %q is not base64", ref.URL)
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return canon.ImageContent{}, fmt.Errorf("image data URL %q: %w", ref.URL, err)
	}
	return canon.ImageContent{
		MIMEType: strings.TrimSuffix(meta, ";base64"),
		Data:     data,
		Detail:   ref.Detail,
	}, nil
}

type tool struct {
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

type toolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      *bool           `json:"strict"`
}

func toolsFrom(tools []tool) ([]canon.Tool, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	out := make([]canon.Tool, 0, len(tools))
	for i, t := range tools {
		if t.Type != "function" {
			return nil, invalid(fmt.Sprintf("tools[%d].type", i), fmt.Errorf("unsupported tool type %q", t.Type))
		}
		if t.Function.Name == "" {
			return nil, missing(fmt.Sprintf("tools[%d].function.name", i))
		}
		out = append(out, canon.FunctionTool{
			Name:        canon.ToolName(t.Function.Name),
			Description: t.Function.Description,
			Parameters:  t.Function.Parameters,
			Strict:      t.Function.Strict != nil && *t.Function.Strict,
		})
	}
	return out, nil
}

func toolChoiceFrom(raw json.RawMessage) (canon.ToolChoice, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var mode string
	if err := json.Unmarshal(raw, &mode); err == nil {
		switch mode {
		case "auto":
			return canon.ToolAuto{}, nil
		case "none":
			return canon.ToolNone{}, nil
		case "required":
			return canon.ToolRequired{}, nil
		default:
			return nil, invalid("tool_choice", fmt.Errorf("unsupported tool_choice %q", mode))
		}
	}
	var allowed struct {
		Type    string `json:"type"`
		Allowed struct {
			Mode  string `json:"mode"`
			Tools []struct {
				Type     string `json:"type"`
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		} `json:"allowed_tools"`
	}
	if json.Unmarshal(raw, &allowed) == nil && allowed.Type == "allowed_tools" {
		if len(allowed.Allowed.Tools) == 0 || allowed.Allowed.Mode != "auto" && allowed.Allowed.Mode != "required" {
			return nil, invalid("tool_choice.allowed_tools", fmt.Errorf("nonempty tools and auto or required mode are required"))
		}
		choice := canon.ToolAllowed{Mode: canon.AllowedAuto}
		if allowed.Allowed.Mode == "required" {
			choice.Mode = canon.AllowedRequired
		}
		for _, tool := range allowed.Allowed.Tools {
			if tool.Type != "function" || tool.Function.Name == "" {
				return nil, invalid("tool_choice.allowed_tools.tools", fmt.Errorf("function name is required"))
			}
			choice.Tools = append(choice.Tools, canon.ToolName(tool.Function.Name))
		}
		return choice, nil
	}
	var named struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &named); err != nil {
		return nil, invalid("tool_choice", err)
	}
	if named.Type == "function" && named.Function.Name != "" {
		return canon.ToolNamed{Name: canon.ToolName(named.Function.Name)}, nil
	}
	return nil, invalid("tool_choice", fmt.Errorf("unsupported tool_choice %s", raw))
}

type body struct {
	Model               string          `json:"model"`
	Messages            []message       `json:"messages"`
	Stream              bool            `json:"stream"`
	MaxTokens           json.RawMessage `json:"max_tokens"`
	MaxCompletionTokens json.RawMessage `json:"max_completion_tokens"`
	Temperature         *float64        `json:"temperature"`
	TopP                *float64        `json:"top_p"`
	Stop                json.RawMessage `json:"stop"`
	ResponseFormat      json.RawMessage `json:"response_format"`
	PresencePenalty     *float64        `json:"presence_penalty"`
	FrequencyPenalty    *float64        `json:"frequency_penalty"`
	ParallelToolCalls   *bool           `json:"parallel_tool_calls"`
	ReasoningEffort     *string         `json:"reasoning_effort"`
	Tools               []tool          `json:"tools"`
	ToolChoice          json.RawMessage `json:"tool_choice"`
	Verbosity           string          `json:"verbosity"`
	ServiceTier         string          `json:"service_tier"`
}

type message struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"tool_call_id"`
	ToolCalls  []toolCall      `json:"tool_calls"`
}

type toolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function toolCallFunction `json:"function"`
}

type toolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
