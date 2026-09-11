package customresponses

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
	Output    any           `json:"output,omitempty"`
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

func buildBody(req canon.Request) ([]byte, error) {
	input, err := inputFrom(req.Input)
	if err != nil {
		return nil, err
	}
	out := body{Model: req.Model, Input: input, Stream: req.Stream}
	var systemParts []string
	for _, c := range req.Instructions {
		if t, ok := c.(canon.TextContent); ok {
			systemParts = append(systemParts, t.Text)
		}
	}
	if len(systemParts) > 0 {
		out.Instructions = strings.Join(systemParts, "\n\n")
	}
	if req.MaxOutputTokens > 0 {
		out.MaxOutputTokens = &req.MaxOutputTokens
	}
	if req.Sampling.Temperature != nil {
		out.Temperature = req.Sampling.Temperature
	}
	if req.Sampling.TopP != nil {
		out.TopP = req.Sampling.TopP
	}
	if req.Sampling.Stop != nil {
		out.Stop = req.Sampling.Stop
	}
	switch req.Sampling.ServiceTier {
	case canon.TierFlex:
		s := "flex"
		out.ServiceTier = &s
	case canon.TierPriority:
		s := "priority"
		out.ServiceTier = &s
	}
	if effort := effortWire(req.Reasoning.Effort); effort != "" {
		out.Reasoning = &reasoning{Effort: &effort}
	}
	if len(req.Tools) > 0 {
		out.Tools, err = toolsFrom(req.Tools)
		if err != nil {
			return nil, err
		}
	}
	if tc, ok := toolChoiceFrom(req.ToolChoice); ok {
		out.ToolChoice = tc
	}
	return json.Marshal(out)
}

func inputFrom(items []canon.Item) ([]inputItem, error) {
	out := make([]inputItem, 0, len(items))
	for _, item := range items {
		switch m := item.(type) {
		case canon.Message:
			parts := make([]contentPart, 0, len(m.Content))
			for _, c := range m.Content {
				switch p := c.(type) {
				case canon.TextContent:
					if m.Role == canon.RoleAssistant && p.Text == "" {
						continue
					}
					parts = append(parts, contentPart{Type: textWireType(m.Role), Text: p.Text})
				case canon.ImageContent:
					url := "data:" + p.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(p.Data)
					parts = append(parts, contentPart{Type: "input_image", ImageURL: url, Detail: p.Detail})
				}
			}
			if len(parts) == 0 && m.Role == canon.RoleAssistant {
				continue
			}
			out = append(out, inputItem{Type: "message", Role: roleWire(m.Role), Content: parts})
		case canon.ReasoningItem:
			out = append(out, inputItem{
				Type:      "reasoning",
				ID:        string(m.ID),
				Content:   []contentPart{{Type: "reasoning_text", Text: m.Content}},
				Signature: m.Signature,
			})
		case canon.FunctionCall:
			out = append(out, inputItem{
				Type: "function_call", ID: string(m.ID), CallID: string(m.CallID),
				Name: string(m.Name), Arguments: string(m.Arguments),
			})
		case canon.FunctionOutput:
			out = append(out, inputItem{Type: "function_call_output", CallID: string(m.CallID), Output: functionCallOutputWire(m.Output)})
		case canon.CustomToolCall:
			out = append(out, inputItem{Type: "custom_tool_call", ID: string(m.ID), CallID: string(m.CallID), Name: string(m.Name), Input: m.Input})
		case canon.CustomToolOutput:
			out = append(out, inputItem{Type: "custom_tool_call_output", CallID: string(m.CallID), Output: m.Output})
		default:
			return nil, fmt.Errorf("customresponses input: unsupported canonical item %T", item)
		}
	}
	return out, nil
}

func functionCallOutputWire(content []canon.Content) any {
	if len(content) == 1 {
		if t, ok := content[0].(canon.TextContent); ok {
			return t.Text
		}
	}
	if len(content) == 0 {
		return ""
	}
	parts := make([]contentPart, 0, len(content))
	for _, c := range content {
		switch p := c.(type) {
		case canon.TextContent:
			parts = append(parts, contentPart{Type: "input_text", Text: p.Text})
		case canon.ImageContent:
			url := "data:" + p.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(p.Data)
			parts = append(parts, contentPart{Type: "input_image", ImageURL: url})
		}
	}
	return parts
}

func textWireType(r canon.Role) string {
	if r == canon.RoleAssistant {
		return "output_text"
	}
	return "input_text"
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
		return "xhigh"
	case canon.EffortMax:
		return "max"
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
				return nil, fmt.Errorf("customresponses tools: unsupported custom tool format %d", tt.Format)
			}
			out = append(out, tf)
		default:
			return nil, fmt.Errorf("customresponses tools: unsupported canonical tool %T", t)
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

func responsesURL(base string) string {
	return wireBase(base) + "/v1/responses"
}

func modelsURL(base string) string {
	return wireBase(base) + "/v1/models"
}

func wireBase(base string) string {
	u := strings.TrimRight(strings.TrimSpace(base), "/")
	u = strings.TrimSuffix(u, "/responses")
	u = strings.TrimSuffix(u, "/v1")
	return u
}
