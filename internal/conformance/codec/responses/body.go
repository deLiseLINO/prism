package responses

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

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
	Type      string        `json:"type"`
	ID        string        `json:"id,omitempty"`
	Role      string        `json:"role,omitempty"`
	Content   []contentPart `json:"content,omitempty"`
	CallID    string        `json:"call_id,omitempty"`
	Name      string        `json:"name,omitempty"`
	Arguments string        `json:"arguments,omitempty"`
	Input     string        `json:"input,omitempty"`
	Output    string        `json:"output,omitempty"`
	Signature string        `json:"signature,omitempty"`
}

type contentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

type tool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
	Format      *toolFormat     `json:"format,omitempty"`
}

type toolFormat struct {
	Type       string `json:"type"`
	Syntax     string `json:"syntax,omitempty"`
	Definition string `json:"definition,omitempty"`
}

func inputFrom(items []canon.Item) (any, error) {
	out := make([]inputItem, 0, len(items))
	for _, item := range items {
		switch it := item.(type) {
		case canon.Message:
			parts := make([]contentPart, 0, len(it.Content))
			for _, c := range it.Content {
				switch p := c.(type) {
				case canon.TextContent:
					parts = append(parts, contentPart{Type: "input_text", Text: p.Text})
				case canon.ImageContent:
					url := "data:" + p.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(p.Data)
					parts = append(parts, contentPart{Type: "input_image", ImageURL: url, Detail: p.Detail})
				}
			}
			out = append(out, inputItem{Type: "message", Role: roleWire(it.Role), Content: parts})
		case canon.ReasoningItem:
			out = append(out, inputItem{
				Type:      "reasoning",
				ID:        string(it.ID),
				Content:   []contentPart{{Type: "reasoning_text", Text: it.Content}},
				Signature: it.Signature,
			})
		case canon.FunctionCall:
			out = append(out, inputItem{Type: "function_call", CallID: string(it.CallID), Name: string(it.Name), Arguments: string(it.Arguments)})
		case canon.FunctionOutput:
			text, err := outputText(it.Output)
			if err != nil {
				return nil, err
			}
			out = append(out, inputItem{
				Type:   "function_call_output",
				CallID: string(it.CallID),
				Output: text,
			})
		case canon.CustomToolCall:
			out = append(out, inputItem{Type: "custom_tool_call", CallID: string(it.CallID), Name: string(it.Name), Input: it.Input})
		case canon.CustomToolOutput:
			out = append(out, inputItem{Type: "custom_tool_call_output", CallID: string(it.CallID), Output: it.Output})
		default:
			return nil, fmt.Errorf("responses input: unsupported canonical item %T", item)
		}
	}
	return out, nil
}

func outputText(content []canon.Content) (string, error) {
	var sb strings.Builder
	for _, c := range content {
		p, ok := c.(canon.TextContent)
		if !ok {
			return "", fmt.Errorf("responses output: unsupported canonical content %T", c)
		}
		sb.WriteString(p.Text)
	}
	return sb.String(), nil
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
		switch tt := t.(type) {
		case canon.FunctionTool:
			tf := tool{Type: "function", Name: string(tt.Name), Description: tt.Description}
			if len(tt.Parameters) > 0 {
				tf.Parameters = tt.Parameters
			}
			if tt.Strict {
				strict := true
				tf.Strict = &strict
			}
			out = append(out, tf)
		case canon.CustomToolDef:
			tf := tool{Type: "custom", Name: string(tt.Name), Description: tt.Description}
			switch {
			case tt.Grammar != nil:
				tf.Format = &toolFormat{Type: "grammar", Syntax: tt.Grammar.Syntax, Definition: tt.Grammar.Definition}
			default:
				return nil, fmt.Errorf("responses tools: unsupported custom tool format %d", tt.Format)
			}
			out = append(out, tf)
		default:
			return nil, fmt.Errorf("responses tools: unsupported canonical tool %T", t)
		}
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
