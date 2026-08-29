package chat

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"prism/internal/canon"
)

type body struct {
	Model             canon.ModelID   `json:"model"`
	Messages          []message       `json:"messages"`
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
	Role       string `json:"role"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Content    any    `json:"content"`
}

type imageURLPart struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type reasoningWire struct {
	Enabled bool   `json:"enabled"`
	Effort  string `json:"effort,omitempty"`
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

func messagesFrom(items []canon.Item) ([]message, error) {
	var out []message
	for _, item := range items {
		switch m := item.(type) {
		case canon.Message:
			out = append(out, message{Role: roleWire(m.Role), Content: contentWire(m.Content)})
		case canon.FunctionOutput:
			out = append(out, toolResultMessages(m)...)
		default:
			return nil, fmt.Errorf("chat messages: unsupported canonical item %T", item)
		}
	}
	return out, nil
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
	default:
		return nil, false
	}
}
