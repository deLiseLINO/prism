package chat

import (
	"encoding/base64"
	"encoding/json"

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
	PresencePenalty   *float64        `json:"presence_penalty,omitempty"`
	FrequencyPenalty  *float64        `json:"frequency_penalty,omitempty"`
	PromptCacheKey    *string         `json:"prompt_cache_key,omitempty"`
	ResponseFormat    json.RawMessage `json:"response_format,omitempty"`
	ParallelToolCalls *bool           `json:"parallel_tool_calls,omitempty"`
	StreamOptions     json.RawMessage `json:"stream_options,omitempty"`
}

type message struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
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

func messagesFrom(items []canon.Item) []message {
	var out []message
	for _, item := range items {
		m, ok := item.(canon.Message)
		if !ok {
			continue
		}
		out = append(out, message{Role: roleWire(m.Role), Content: contentWire(m.Content)})
	}
	return out
}

func contentWire(content []canon.Content) any {
	if len(content) == 1 {
		if t, ok := content[0].(canon.TextContent); ok {
			return t.Text
		}
	}
	parts := make([]any, 0, len(content))
	for _, c := range content {
		switch p := c.(type) {
		case canon.TextContent:
			parts = append(parts, map[string]any{"type": "text", "text": p.Text})
		case canon.ImageContent:
			url := "data:" + p.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(p.Data)
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}})
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

func toolsFrom(tools []canon.Tool) []tool {
	var out []tool
	for _, t := range tools {
		fn, ok := t.(canon.FunctionTool)
		if !ok {
			continue
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
	return out
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
