package chat

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"prism/internal/canon"
	"prism/internal/conformance"
)

type body struct {
	Model             canon.ModelID   `json:"model"`
	Messages          []any           `json:"messages"`
	Stream            bool            `json:"stream"`
	ServiceTier       *string         `json:"service_tier,omitempty"`
	ReasoningSplit    *bool           `json:"reasoning_split,omitempty"`
	Provider          json.RawMessage `json:"provider,omitempty"`
	Tools             []tool          `json:"tools,omitempty"`
	ToolChoice        json.RawMessage `json:"tool_choice,omitempty"`
	MaxTokens         *int            `json:"max_tokens,omitempty"`
	Temperature       *float64        `json:"temperature,omitempty"`
	TopP              *float64        `json:"top_p,omitempty"`
	Stop              []string        `json:"stop,omitempty"`
	ReasoningEffort   *string         `json:"reasoning_effort,omitempty"`
	Reasoning         *reasoningWire  `json:"reasoning,omitempty"`
	PresencePenalty   *float64        `json:"presence_penalty,omitempty"`
	FrequencyPenalty  *float64        `json:"frequency_penalty,omitempty"`
	PromptCacheKey    *string         `json:"prompt_cache_key,omitempty"`
	ResponseFormat    json.RawMessage `json:"response_format,omitempty"`
	ParallelToolCalls *bool           `json:"parallel_tool_calls,omitempty"`
	StreamOptions     json.RawMessage `json:"stream_options,omitempty"`
}

type message struct {
	Role       string     `json:"role"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Content    any        `json:"content"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
}

type imageURLPart struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type reasoningWire struct {
	Enabled bool   `json:"enabled"`
	Effort  string `json:"effort,omitempty"`
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

type tool struct {
	Type     string       `json:"type"`
	Function toolFunction `json:"function,omitempty"`
}

type toolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

func messagesFrom(items []canon.Item) ([]any, []string, error) {
	var out []any
	var warnings []string
	var pending []pendingToolCall
	var imageParts []any
	flushImages := func() {
		if len(imageParts) == 0 {
			return
		}
		content := []any{map[string]any{"type": "text", "text": "[prism] image output from the preceding tool result(s):"}}
		content = append(content, imageParts...)
		out = append(out, message{Role: "user", Content: content})
		imageParts = nil
	}
	flushPending := func() {
		if len(pending) == 0 {
			return
		}
		for _, call := range pending {
			out = append(out, message{
				Role:       "tool",
				ToolCallID: call.id,
				Content:    fmt.Sprintf("[prism] no tool result was recorded for %q; execution status unknown — do not treat this as success, failure, or user-provided input.", call.name),
			})
		}
		pending = nil
		flushImages()
	}
	for _, item := range items {
		switch it := item.(type) {
		case canon.Message:
			flushPending()
			out = append(out, message{Role: roleWire(it.Role), Content: contentWire(it.Content)})
		case canon.FunctionCall:
			args := string(it.Arguments)
			if args == "" {
				args = "{}"
			}
			out = append(out, message{
				Role:    "assistant",
				Content: "",
				ToolCalls: []toolCall{{
					ID:   string(it.CallID),
					Type: "function",
					Function: toolCallFunction{
						Name:      string(it.Name),
						Arguments: args,
					},
				}},
			})
			pending = append(pending, pendingToolCall{id: string(it.CallID), name: string(it.Name)})
		case canon.CustomToolCall:
			args, err := json.Marshal(map[string]any{"input": it.Input})
			if err != nil {
				return nil, nil, err
			}
			out = append(out, message{
				Role:    "assistant",
				Content: "",
				ToolCalls: []toolCall{{
					ID:   string(it.CallID),
					Type: "function",
					Function: toolCallFunction{
						Name:      string(it.Name),
						Arguments: string(args),
					},
				}},
			})
		case canon.FunctionOutput:
			id := string(it.CallID)
			text := toolResultTextForWire(it.Output)
			parts := toolResultImageChatParts(it.Output)
			matched := false
			for i, call := range pending {
				if call.id == id {
					pending = append(pending[:i], pending[i+1:]...)
					matched = true
					break
				}
			}
			if !matched {
				flushPending()
				for _, tm := range toolResultMessages(it) {
					out = append(out, tm)
				}
				continue
			}
			out = append(out, message{Role: "tool", ToolCallID: id, Content: text})
			imageParts = append(imageParts, parts...)
			if len(pending) == 0 {
				flushImages()
			}
		case canon.CustomToolOutput:
			out = append(out, message{Role: "tool", ToolCallID: string(it.CallID), Content: it.Output})
		case canon.CompactionMarker, canon.LocalShellCall, canon.LocalShellOutput, canon.ToolSearchCall, canon.ToolSearchOutput:
			warnings = append(warnings, "skip_exotic_item:"+conformance.ExoticItemName(item))
		default:
			return nil, nil, fmt.Errorf("chat messages: unsupported canonical item %T", item)
		}
	}
	flushPending()
	return out, warnings, nil
}

func toolResultMessages(fo canon.FunctionOutput) []message {
	var text []string
	var images []any
	for _, c := range fo.Output {
		switch p := c.(type) {
		case canon.TextContent:
			text = append(text, p.Text)
		case canon.ImageContent:
			url := "data:" + p.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(p.Data)
			images = append(images, map[string]any{"type": "image_url", "image_url": imageURLPart{URL: url, Detail: p.Detail}})
		}
	}
	content := strings.Join(text, "")
	if content == "" && len(images) > 0 {
		content = strings.Repeat("[image]", len(images))
	}
	out := []message{{Role: "tool", ToolCallID: string(fo.CallID), Content: content}}
	if len(images) > 0 {
		out = append(out, message{Role: "user", Content: images})
	}
	return out
}

type pendingToolCall struct{ id, name string }

func toolResultTextForWire(content []canon.Content) string {
	var text strings.Builder
	for _, c := range content {
		if t, ok := c.(canon.TextContent); ok {
			text.WriteString(t.Text)
		}
	}
	return text.String()
}

func toolResultImageChatParts(content []canon.Content) []any {
	var parts []any
	for _, c := range content {
		img, ok := c.(canon.ImageContent)
		if !ok {
			continue
		}
		url := "data:" + img.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(img.Data)
		imageURL := map[string]any{"url": url}
		if img.Detail != "" {
			imageURL["detail"] = img.Detail
		}
		parts = append(parts, map[string]any{"type": "image_url", "image_url": imageURL})
	}
	return parts
}

func contentWire(content []canon.Content) any {
	allText := true
	for _, c := range content {
		if _, ok := c.(canon.TextContent); !ok {
			allText = false
			break
		}
	}
	if allText {
		var sb strings.Builder
		for _, c := range content {
			sb.WriteString(c.(canon.TextContent).Text)
		}
		return sb.String()
	}
	parts := make([]any, 0, len(content))
	for _, c := range content {
		switch p := c.(type) {
		case canon.TextContent:
			parts = append(parts, map[string]any{"type": "text", "text": p.Text})
		case canon.ImageContent:
			url := "data:" + p.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(p.Data)
			parts = append(parts, map[string]any{"type": "image_url", "image_url": imageURLPart{URL: url, Detail: p.Detail}})
		}
	}
	return parts
}

func roleWire(r canon.Role) string {
	switch r {
	case canon.RoleAssistant:
		return "assistant"
	case canon.RoleSystem:
		return "system"
	case canon.RoleDeveloper:
		return "developer"
	default:
		return "user"
	}
}

func effortWire(e canon.ReasoningEffort) string {
	switch e {
	case canon.EffortMinimal:
		return "minimal"
	case canon.EffortLow:
		return "low"
	case canon.EffortMedium:
		return "medium"
	case canon.EffortHigh:
		return "high"
	case canon.EffortXHigh:
		return "high"
	default:
		return ""
	}
}

const effortOmitSentinel = "__omit__"

func mapReasoningEffort(m map[string]string, requested string) (string, bool) {
	if m == nil {
		return requested, true
	}
	if v, ok := m[requested]; ok {
		if v == effortOmitSentinel {
			return "", false
		}
		return v, true
	}
	return requested, true
}

func toolsFrom(tools []canon.Tool) ([]tool, error) {
	var out []tool
	for _, t := range tools {
		fn, ok := t.(canon.FunctionTool)
		if !ok {
			return nil, fmt.Errorf("chat tools: unsupported canonical tool %T", t)
		}
		tf := toolFunction{Name: string(fn.Name), Description: fn.Description}
		if len(fn.Parameters) > 0 {
			tf.Parameters = fn.Parameters
		}
		if fn.Strict {
			strict := true
			tf.Strict = &strict
		}
		out = append(out, tool{Type: "function", Function: tf})
	}
	return out, nil
}

func toolChoiceFrom(tc canon.ToolChoice) (json.RawMessage, bool) {
	switch t := tc.(type) {
	case canon.ToolNone:
		return json.RawMessage(`"none"`), true
	case canon.ToolAuto:
		return json.RawMessage(`"auto"`), true
	case canon.ToolRequired:
		return json.RawMessage(`"required"`), true
	case canon.ToolNamed:
		raw, _ := json.Marshal(map[string]any{"type": "function", "function": map[string]any{"name": string(t.Name)}})
		return raw, true
	case canon.ToolAllowed:
		switch {
		case t.Mode == canon.AllowedRequired && len(t.Tools) == 1:
			name, _ := json.Marshal(string(t.Tools[0]))
			raw := json.RawMessage(`{"type":"function","function":{"name":` + string(name) + `}}`)
			return raw, true
		case t.Mode == canon.AllowedRequired:
			return json.RawMessage(`"required"`), true
		default:
			return json.RawMessage(`"auto"`), true
		}
	default:
		return nil, false
	}
}

func filterToolsByChoice(tools []canon.Tool, choice canon.ToolChoice) []canon.Tool {
	switch choice.(type) {
	case canon.ToolAllowed, canon.ToolNamed, canon.ToolNone:
	default:
		return tools
	}
	allowed := map[string]struct{}{}
	named := ""
	switch t := choice.(type) {
	case canon.ToolAllowed:
		for _, name := range t.Tools {
			allowed[string(name)] = struct{}{}
		}
	case canon.ToolNamed:
		named = string(t.Name)
	case canon.ToolNone:
		return nil
	}
	var out []canon.Tool
	for _, tool := range tools {
		name := toolName(tool)
		if name == "" {
			continue
		}
		if named != "" {
			if name == named {
				out = append(out, tool)
			}
			continue
		}
		if len(allowed) > 0 {
			if _, ok := allowed[name]; ok {
				out = append(out, tool)
			}
			continue
		}
		out = append(out, tool)
	}
	return out
}

func toolName(t canon.Tool) string {
	switch tt := t.(type) {
	case canon.FunctionTool:
		return string(tt.Name)
	case canon.CustomToolDef:
		return string(tt.Name)
	default:
		return ""
	}
}
