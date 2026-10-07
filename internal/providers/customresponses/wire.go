package customresponses

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/providers/openaierr"
)

type body struct {
	Model            canon.ModelID   `json:"model"`
	Input            any             `json:"input"`
	Stream           bool            `json:"stream"`
	Instructions     string          `json:"instructions,omitempty"`
	MaxOutputTokens  *int            `json:"max_output_tokens,omitempty"`
	Temperature      *float64        `json:"temperature,omitempty"`
	TopP             *float64        `json:"top_p,omitempty"`
	ServiceTier      *string         `json:"service_tier,omitempty"`
	PresencePenalty  *float64        `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64        `json:"frequency_penalty,omitempty"`
	Reasoning        *reasoning      `json:"reasoning,omitempty"`
	Text             *textConfig     `json:"text,omitempty"`
	Tools            []tool          `json:"tools,omitempty"`
	ToolChoice       json.RawMessage `json:"tool_choice,omitempty"`
	ParallelTools    *bool           `json:"parallel_tool_calls,omitempty"`
}

type textConfig struct {
	Verbosity string      `json:"verbosity,omitempty"`
	Format    *textFormat `json:"format,omitempty"`
}

type textFormat struct {
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

type reasoning struct {
	Effort  *string `json:"effort,omitempty"`
	Summary string  `json:"summary,omitempty"`
}

type inputItem struct {
	Type             string         `json:"type"`
	ID               string         `json:"id,omitempty"`
	Role             string         `json:"role,omitempty"`
	Content          []contentPart  `json:"content,omitempty"`
	Summary          *[]contentPart `json:"summary,omitempty"`
	EncryptedContent string         `json:"encrypted_content,omitempty"`
	CallID           string         `json:"call_id,omitempty"`
	Name             string         `json:"name,omitempty"`
	Arguments        string         `json:"arguments,omitempty"`
	Input            *string        `json:"input,omitempty"`
	Output           any            `json:"output,omitempty"`
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
	out.PresencePenalty = req.Sampling.PresencePenalty
	out.FrequencyPenalty = req.Sampling.FrequencyPenalty
	switch req.Sampling.ServiceTier {
	case canon.TierDefault:
		s := "default"
		out.ServiceTier = &s
	case canon.TierFlex:
		s := "flex"
		out.ServiceTier = &s
	case canon.TierPriority:
		s := "priority"
		out.ServiceTier = &s
	}
	rc := reasoning{}
	if effort := effortWire(req.Reasoning.Effort); effort != "" {
		rc.Effort = &effort
	}
	switch req.Reasoning.Summary {
	case canon.SummaryAuto:
		rc.Summary = "auto"
	case canon.SummaryConcise:
		rc.Summary = "concise"
	case canon.SummaryDetailed:
		rc.Summary = "detailed"
	}
	if rc.Effort != nil || rc.Summary != "" {
		out.Reasoning = &rc
	}
	if len(req.Tools) > 0 {
		out.Tools, err = toolsFrom(req.Tools)
		if err != nil {
			return nil, err
		}
	}
	if allowed, ok := req.ToolChoice.(canon.ToolAllowed); ok {
		selected := make([]tool, 0, len(allowed.Tools))
		for _, name := range allowed.Tools {
			found := false
			for _, tool := range out.Tools {
				if tool.Name == string(name) {
					selected = append(selected, tool)
					found = true
				}
			}
			if !found {
				return nil, fmt.Errorf("customresponses tool_choice: allowed tool %q is not declared", name)
			}
		}
		if len(selected) == 0 {
			return nil, fmt.Errorf("customresponses tool_choice: allowed set is empty")
		}
		out.Tools = selected
		if allowed.Mode == canon.AllowedRequired {
			req.ToolChoice = canon.ToolRequired{}
		} else {
			req.ToolChoice = canon.ToolAuto{}
		}
	}
	if tc, ok := toolChoiceFrom(req.ToolChoice); ok {
		out.ToolChoice = tc
		if named, ok := req.ToolChoice.(canon.ToolNamed); ok {
			for _, tool := range out.Tools {
				if tool.Name == string(named.Name) && tool.Type == "custom" {
					out.ToolChoice, _ = json.Marshal(map[string]any{"type": "custom", "name": string(named.Name)})
				}
			}
		}
	}
	out.ParallelTools = req.Sampling.ParallelToolCalls
	if cfg, err := textConfigFrom(req.Text); err != nil {
		return nil, err
	} else {
		out.Text = cfg
	}
	return json.Marshal(out)
}

func textConfigFrom(t canon.TextOutput) (*textConfig, error) {
	cfg := textConfig{}
	switch t.Verbosity {
	case canon.VerbosityLow:
		cfg.Verbosity = "low"
	case canon.VerbosityMedium:
		cfg.Verbosity = "medium"
	case canon.VerbosityHigh:
		cfg.Verbosity = "high"
	}
	if t.Format != nil {
		switch t.Format.Type {
		case "json_schema":
			cfg.Format = &textFormat{Type: "json_schema", Name: t.Format.Name, Description: t.Format.Description, Schema: json.RawMessage(t.Format.Schema), Strict: t.Format.Strict}
		case "text", "json_object":
			cfg.Format = &textFormat{Type: t.Format.Type}
		default:
			return nil, fmt.Errorf("customresponses input: text format %q is not representable", t.Format.Type)
		}
	}
	if cfg.Verbosity == "" && cfg.Format == nil {
		return nil, nil
	}
	return &cfg, nil
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
			id := itemIDWithPrefix(m.ID, "rs_")
			if m.State.Store != canon.StoreWire || m.State.Key == "" {
				continue
			}
			summary := make([]contentPart, 0, len(m.Summary))
			for _, part := range m.Summary {
				summary = append(summary, contentPart{Type: "summary_text", Text: part.Text})
			}
			out = append(out, inputItem{
				Type:             "reasoning",
				ID:               id,
				Summary:          &summary,
				EncryptedContent: m.State.Key,
			})
		case canon.FunctionCall:
			out = append(out, inputItem{
				Type: "function_call", ID: itemIDWithPrefix(m.ID, "fc_"), CallID: string(m.CallID),
				Name: string(m.Name), Arguments: string(m.Arguments),
			})
		case canon.FunctionOutput:
			out = append(out, inputItem{Type: "function_call_output", CallID: string(m.CallID), Output: functionCallOutputWire(m.Output)})
		case canon.CustomToolCall:
			out = append(out, inputItem{Type: "custom_tool_call", ID: itemIDWithPrefix(m.ID, "ctc_"), CallID: string(m.CallID), Name: string(m.Name), Input: &m.Input})
		case canon.CustomToolOutput:
			var output any = m.Output
			if m.Content != nil {
				parts := make([]contentPart, 0, len(m.Content))
				for _, part := range m.Content {
					switch part := part.(type) {
					case canon.TextContent:
						parts = append(parts, contentPart{Type: "input_text", Text: part.Text})
					case canon.ImageContent:
						parts = append(parts, contentPart{Type: "input_image", ImageURL: "data:" + part.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(part.Data), Detail: part.Detail})
					default:
						return nil, fmt.Errorf("customresponses input: unsupported custom tool result %T", part)
					}
				}
				output = parts
			}
			out = append(out, inputItem{Type: "custom_tool_call_output", CallID: string(m.CallID), Output: output})
		case canon.LocalShellCall, canon.LocalShellOutput, canon.ToolSearchCall, canon.ToolSearchOutput:
			continue
		default:
			return nil, fmt.Errorf("customresponses input: unsupported canonical item %T", item)
		}
	}
	return out, nil
}

func itemIDWithPrefix(id canon.ItemID, prefix string) string {
	if strings.HasPrefix(string(id), prefix) {
		return string(id)
	}
	return ""
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
		return "xhigh"
	case canon.EffortMax:
		return "max"
	case canon.EffortOff:
		return "none"
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
			case tt.Format == canon.FormatJSON:
				tf.Format = &toolFormat{Type: "json"}
			default:
				tf.Format = &toolFormat{Type: "text"}
			}
			out = append(out, tf)
		case canon.LocalShellToolDef, canon.ToolSearchToolDef:
			continue
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
	return openaierr.APIBase(base) + "/responses"
}

func modelsURL(base string) string {
	return openaierr.APIBase(base) + "/models"
}
