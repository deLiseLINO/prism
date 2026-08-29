package codex

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"prism/internal/canon"
)

type wireBody struct {
	Model             canon.ModelID   `json:"model"`
	Instructions      string          `json:"instructions,omitempty"`
	Input             []wireItem      `json:"input"`
	Tools             []wireTool      `json:"tools,omitempty"`
	ToolChoice        json.RawMessage `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool           `json:"parallel_tool_calls,omitempty"`
	Reasoning         *wireReasoning  `json:"reasoning,omitempty"`
	Text              *wireText       `json:"text,omitempty"`
	Temperature       *float64        `json:"temperature,omitempty"`
	TopP              *float64        `json:"top_p,omitempty"`
	Stream            bool            `json:"stream"`
	Store             bool            `json:"store"`
	Include           []string        `json:"include,omitempty"`
	ServiceTier       *string         `json:"service_tier,omitempty"`
}

type wireReasoning struct {
	Effort  string  `json:"effort,omitempty"`
	Summary *string `json:"summary,omitempty"`
}

type wireText struct {
	Format    *wireTextFormat `json:"format,omitempty"`
	Verbosity string          `json:"verbosity,omitempty"`
}

type wireTextFormat struct {
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

type wireItem struct {
	Type             string        `json:"type"`
	ID               string        `json:"id,omitempty"`
	Role             string        `json:"role,omitempty"`
	Content          []wireContent `json:"content,omitempty"`
	Summary          []wireContent `json:"summary,omitempty"`
	CallID           string        `json:"call_id,omitempty"`
	Name             string        `json:"name,omitempty"`
	Arguments        string        `json:"arguments,omitempty"`
	Input            string        `json:"input,omitempty"`
	Output           any           `json:"output,omitempty"`
	EncryptedContent *string       `json:"encrypted_content,omitempty"`
}

type wireContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

type wireTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
	Format      *wireToolFormat `json:"format,omitempty"`
}

type wireToolFormat struct {
	Type       string `json:"type"`
	Syntax     string `json:"syntax,omitempty"`
	Definition string `json:"definition,omitempty"`
}

type BuildResult struct {
	Body     []byte
	Warnings []string
}

func BuildRequestBody(req canon.Request) (BuildResult, error) {
	var warnings []string
	if req.MaxOutputTokens > 0 {
		warnings = append(warnings, "drop:max_output_tokens")
	}
	if req.Sampling.Stop != nil {
		warnings = append(warnings, "drop:stop")
	}
	if req.Sampling.PresencePenalty != nil {
		warnings = append(warnings, "drop:presence_penalty")
	}
	if req.Sampling.FrequencyPenalty != nil {
		warnings = append(warnings, "drop:frequency_penalty")
	}
	input, err := inputFrom(req.Input)
	if err != nil {
		return BuildResult{}, fmt.Errorf("codex body: %w", err)
	}
	reasoning := reasoningFrom(req.Reasoning, &warnings)
	body := wireBody{
		Model:     req.Model,
		Input:     input,
		Stream:    req.Stream,
		Store:     false,
		Reasoning: reasoning,
	}
	if reasoning != nil {
		body.Include = []string{"reasoning.encrypted_content"}
	}
	var systemParts []string
	for _, c := range req.Instructions {
		if t, ok := c.(canon.TextContent); ok {
			systemParts = append(systemParts, t.Text)
		}
	}
	body.Instructions = strings.Join(systemParts, "\n\n")
	if len(req.Tools) > 0 {
		tools, err := toolsFrom(req.Tools)
		if err != nil {
			return BuildResult{}, fmt.Errorf("codex body: %w", err)
		}
		body.Tools = tools
	}
	if tc, ok := toolChoiceFrom(req.ToolChoice); ok {
		body.ToolChoice = tc
	}
	body.ParallelToolCalls = req.Sampling.ParallelToolCalls
	if req.Sampling.Temperature != nil {
		body.Temperature = req.Sampling.Temperature
	}
	if req.Sampling.TopP != nil {
		body.TopP = req.Sampling.TopP
	}
	if req.Text.Format != nil || req.Text.Verbosity != 0 {
		body.Text = textFrom(req.Text)
	}
	switch req.Sampling.ServiceTier {
	case canon.TierFlex:
		s := "flex"
		body.ServiceTier = &s
	case canon.TierPriority:
		s := "priority"
		body.ServiceTier = &s
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return BuildResult{}, fmt.Errorf("codex body: %w", err)
	}
	return BuildResult{Body: raw, Warnings: warnings}, nil
}

func reasoningFrom(cfg canon.ReasoningConfig, warnings *[]string) *wireReasoning {
	effort := effortWire(cfg.Effort)
	if effort == "" {
		return nil
	}
	out := wireReasoning{Effort: effort}
	switch cfg.Summary {
	case canon.SummaryAuto:
		s := "auto"
		out.Summary = &s
	case canon.SummaryConcise:
		s := "concise"
		out.Summary = &s
	case canon.SummaryDetailed:
		s := "detailed"
		out.Summary = &s
	}
	return &out
}

func textFrom(out canon.TextOutput) *wireText {
	w := &wireText{}
	if out.Format != nil {
		f := &wireTextFormat{Type: out.Format.Type, Name: out.Format.Name, Description: out.Format.Description}
		if len(out.Format.Schema) > 0 {
			f.Schema = json.RawMessage(out.Format.Schema)
		}
		if out.Format.Strict != nil {
			f.Strict = out.Format.Strict
		}
		w.Format = f
	}
	switch out.Verbosity {
	case canon.VerbosityLow:
		w.Verbosity = "low"
	case canon.VerbosityMedium:
		w.Verbosity = "medium"
	case canon.VerbosityHigh:
		w.Verbosity = "high"
	}
	return w
}

func inputFrom(items []canon.Item) ([]wireItem, error) {
	out := make([]wireItem, 0, len(items))
	for _, item := range items {
		switch m := item.(type) {
		case canon.Message:
			parts := make([]wireContent, 0, len(m.Content))
			for _, c := range m.Content {
				switch p := c.(type) {
				case canon.TextContent:
					parts = append(parts, wireContent{Type: "input_text", Text: p.Text})
				case canon.ImageContent:
					url := "data:" + p.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(p.Data)
					parts = append(parts, wireContent{Type: "input_image", ImageURL: url, Detail: p.Detail})
				}
			}
			out = append(out, wireItem{Type: "message", ID: string(m.ID), Role: roleWire(m.Role), Content: parts})
		case canon.ReasoningItem:
			out = append(out, reasoningItemFrom(m))
		case canon.FunctionCall:
			out = append(out, wireItem{
				Type: "function_call", ID: string(m.ID), CallID: string(m.CallID),
				Name: string(m.Name), Arguments: string(m.Arguments),
			})
		case canon.FunctionOutput:
			out = append(out, wireItem{Type: "function_call_output", ID: string(m.ID), CallID: string(m.CallID), Output: functionCallOutputWire(m.Output)})
		case canon.CustomToolCall:
			out = append(out, wireItem{Type: "custom_tool_call", ID: string(m.ID), CallID: string(m.CallID), Name: string(m.Name), Input: m.Input})
		case canon.CustomToolOutput:
			out = append(out, wireItem{Type: "custom_tool_call_output", ID: string(m.ID), CallID: string(m.CallID), Output: m.Output})
		case canon.LocalShellCall:
			out = append(out, wireItem{Type: "local_shell_call", ID: string(m.ID), CallID: string(m.CallID), Input: m.Command})
		case canon.LocalShellOutput:
			out = append(out, wireItem{Type: "local_shell_output", ID: string(m.ID), CallID: string(m.CallID), Output: shellOutputWire(m)})
		default:
			return nil, fmt.Errorf("unsupported canonical item %T", item)
		}
	}
	return out, nil
}

func reasoningItemFrom(m canon.ReasoningItem) wireItem {
	w := wireItem{Type: "reasoning", ID: string(m.ID)}
	if m.Content != "" {
		w.Content = []wireContent{{Type: "reasoning_text", Text: m.Content}}
	}
	for _, s := range m.Summary {
		w.Summary = append(w.Summary, wireContent{Type: "summary_text", Text: s.Text})
	}
	if m.State.Store == reasoningStoreNative && m.State.Key != "" {
		key := m.State.Key
		w.EncryptedContent = &key
	}
	return w
}

func shellOutputWire(m canon.LocalShellOutput) string {
	return m.Output
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
	parts := make([]wireContent, 0, len(content))
	for _, c := range content {
		switch p := c.(type) {
		case canon.TextContent:
			parts = append(parts, wireContent{Type: "input_text", Text: p.Text})
		case canon.ImageContent:
			url := "data:" + p.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(p.Data)
			parts = append(parts, wireContent{Type: "input_image", ImageURL: url})
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

func toolsFrom(tools []canon.Tool) ([]wireTool, error) {
	var out []wireTool
	for _, t := range tools {
		switch tt := t.(type) {
		case canon.FunctionTool:
			w := wireTool{Type: "function", Name: string(tt.Name), Description: tt.Description}
			if len(tt.Parameters) > 0 {
				w.Parameters = json.RawMessage(tt.Parameters)
			}
			if tt.Strict {
				strict := true
				w.Strict = &strict
			}
			out = append(out, w)
		case canon.CustomToolDef:
			w := wireTool{Type: "custom", Name: string(tt.Name), Description: tt.Description}
			switch {
			case tt.Grammar != nil:
				w.Format = &wireToolFormat{Type: "grammar", Syntax: tt.Grammar.Syntax, Definition: tt.Grammar.Definition}
			default:
				return nil, fmt.Errorf("unsupported custom tool format %d", tt.Format)
			}
			out = append(out, w)
		case canon.LocalShellToolDef:
			out = append(out, wireTool{Type: "local_shell"})
		default:
			return nil, fmt.Errorf("unsupported canonical tool %T", t)
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
		raw, _ := json.Marshal(struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}{Type: "function", Name: string(t.Name)})
		return raw, true
	default:
		return nil, false
	}
}
