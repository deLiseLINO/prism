package responses

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"prism/internal/canon"
)

type body struct {
	Model           canon.ModelID   `json:"model"`
	Input           any             `json:"input"`
	Stream          bool            `json:"stream"`
	Instructions    string          `json:"instructions,omitempty"`
	MaxOutputTokens *int            `json:"max_output_tokens,omitempty"`
	Temperature     *float64        `json:"temperature,omitempty"`
	TopP            *float64        `json:"top_p,omitempty"`
	Stop            []string        `json:"stop,omitempty"`
	ServiceTier     *string         `json:"service_tier,omitempty"`
	Reasoning       *reasoning      `json:"reasoning,omitempty"`
	Tools           []tool          `json:"tools,omitempty"`
	ToolChoice      json.RawMessage `json:"tool_choice,omitempty"`
}

type reasoning struct {
	Effort *string `json:"effort,omitempty"`
}

type inputItem struct {
	Type    string        `json:"type"`
	Role    string        `json:"role,omitempty"`
	Content []contentPart `json:"content,omitempty"`
}

type contentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

type tool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

func inputFrom(items []canon.Item) (any, error) {
	out := make([]inputItem, 0, len(items))
	for _, item := range items {
		m, ok := item.(canon.Message)
		if !ok {
			return nil, fmt.Errorf("responses input: unsupported canonical item %T", item)
		}
		parts := make([]contentPart, 0, len(m.Content))
		for _, c := range m.Content {
			switch p := c.(type) {
			case canon.TextContent:
				parts = append(parts, contentPart{Type: "input_text", Text: p.Text})
			case canon.ImageContent:
				url := "data:" + p.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(p.Data)
				parts = append(parts, contentPart{Type: "input_image", ImageURL: url})
			}
		}
		out = append(out, inputItem{Type: "message", Role: roleWire(m.Role), Content: parts})
	}
	return out, nil
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

func toolsFrom(tools []canon.Tool) ([]tool, error) {
	var out []tool
	for _, t := range tools {
		fn, ok := t.(canon.FunctionTool)
		if !ok {
			return nil, fmt.Errorf("responses tools: unsupported canonical tool %T", t)
		}
		tf := tool{Type: "function", Name: string(fn.Name), Description: fn.Description}
		if len(fn.Parameters) > 0 {
			tf.Parameters = fn.Parameters
		}
		if fn.Strict {
			strict := true
			tf.Strict = &strict
		}
		out = append(out, tf)
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
		raw, _ := json.Marshal(map[string]any{"type": "function", "name": string(t.Name)})
		return raw, true
	default:
		return nil, false
	}
}
