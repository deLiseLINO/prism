package customchat

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"prism/internal/canon"
)

type body struct {
	Model               string          `json:"model"`
	Messages            []message       `json:"messages"`
	Stream              bool            `json:"stream"`
	StreamOptions       *streamOptions  `json:"stream_options,omitempty"`
	MaxCompletionTokens *int            `json:"max_completion_tokens,omitempty"`
	Temperature         *float64        `json:"temperature,omitempty"`
	TopP                *float64        `json:"top_p,omitempty"`
	Stop                []string        `json:"stop,omitempty"`
	ParallelToolCalls   *bool           `json:"parallel_tool_calls,omitempty"`
	PresencePenalty     *float64        `json:"presence_penalty,omitempty"`
	FrequencyPenalty    *float64        `json:"frequency_penalty,omitempty"`
	ServiceTier         *string         `json:"service_tier,omitempty"`
	ReasoningEffort     *string         `json:"reasoning_effort,omitempty"`
	Verbosity           *string         `json:"verbosity,omitempty"`
	ResponseFormat      *responseFormat `json:"response_format,omitempty"`
	Tools               []tool          `json:"tools,omitempty"`
	ToolChoice          json.RawMessage `json:"tool_choice,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type message struct {
	Role       string     `json:"role"`
	Content    any        `json:"content,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
}

type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function funcCallWire `json:"function"`
}

type funcCallWire struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type namedToolChoice struct {
	Type     string        `json:"type"`
	Function namedFunction `json:"function"`
}

type namedFunction struct {
	Name string `json:"name"`
}

type tool struct {
	Type     string      `json:"type"`
	Function functionDef `json:"function"`
}

type functionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

type responseFormat struct {
	Type       string      `json:"type"`
	JSONSchema *wireSchema `json:"json_schema,omitempty"`
}

type wireSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

func buildBody(req canon.Request) ([]byte, error) {
	messages, err := messagesFrom(req)
	if err != nil {
		return nil, err
	}
	out := body{Model: string(req.Model), Messages: messages, Stream: req.Stream}
	if req.Stream {
		out.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	if req.MaxOutputTokens > 0 {
		tokens := req.MaxOutputTokens
		out.MaxCompletionTokens = &tokens
	}
	out.Temperature = req.Sampling.Temperature
	out.TopP = req.Sampling.TopP
	out.Stop = req.Sampling.Stop
	out.ParallelToolCalls = req.Sampling.ParallelToolCalls
	out.PresencePenalty = req.Sampling.PresencePenalty
	out.FrequencyPenalty = req.Sampling.FrequencyPenalty
	switch req.Sampling.ServiceTier {
	case canon.TierFlex:
		tier := "flex"
		out.ServiceTier = &tier
	case canon.TierPriority:
		tier := "priority"
		out.ServiceTier = &tier
	}
	if effort := effortWire(req.Reasoning.Effort); effort != "" {
		out.ReasoningEffort = &effort
	}
	if verbosity := verbosityWire(req.Text.Verbosity); verbosity != "" {
		out.Verbosity = &verbosity
	}
	if req.Text.Format != nil {
		format, err := responseFormatFrom(req.Text.Format)
		if err != nil {
			return nil, err
		}
		out.ResponseFormat = format
	}
	if len(req.Tools) > 0 {
		out.Tools, err = toolsFrom(req.Tools)
		if err != nil {
			return nil, err
		}
	}
	if choice, ok, err := toolChoiceFrom(req.ToolChoice); err != nil {
		return nil, err
	} else if ok {
		out.ToolChoice = choice
	}
	return json.Marshal(out)
}

func messagesFrom(req canon.Request) ([]message, error) {
	out := make([]message, 0, len(req.Input)+1)
	if len(req.Instructions) > 0 {
		text, err := textFromContent(req.Instructions)
		if err != nil {
			return nil, err
		}
		out = append(out, message{Role: "system", Content: text})
	}
	for i := 0; i < len(req.Input); i++ {
		switch req.Input[i].(type) {
		case canon.FunctionCall:
			calls, advanced, err := standaloneCalls(req.Input, i)
			if err != nil {
				return nil, err
			}
			out = append(out, message{Role: "assistant", ToolCalls: calls})
			i += advanced
		case canon.Message:
			msg, advanced, err := messageFrom(req.Input, i)
			if err != nil {
				return nil, err
			}
			out = append(out, msg)
			i += advanced
		case canon.FunctionOutput:
			output := req.Input[i].(canon.FunctionOutput)
			text, err := toolOutputText(output.Output)
			if err != nil {
				return nil, err
			}
			if output.CallID == "" {
				return nil, fmt.Errorf("customchat input: tool result is missing call id")
			}
			out = append(out, message{Role: "tool", Content: text, ToolCallID: string(output.CallID)})
		case canon.CustomToolCall:
			call := req.Input[i].(canon.CustomToolCall)
			if call.CallID == "" {
				return nil, fmt.Errorf("customchat input: custom tool call is missing call id")
			}
			arguments, err := json.Marshal(call.Input)
			if err != nil {
				return nil, fmt.Errorf("customchat input: custom tool call arguments: %w", err)
			}
			out = append(out, message{Role: "assistant", ToolCalls: []toolCall{{
				ID:       string(call.CallID),
				Type:     "function",
				Function: funcCallWire{Name: string(call.Name), Arguments: string(arguments)},
			}}})
		case canon.CustomToolOutput:
			output := req.Input[i].(canon.CustomToolOutput)
			if output.CallID == "" {
				return nil, fmt.Errorf("customchat input: custom tool result is missing call id")
			}
			out = append(out, message{Role: "tool", Content: output.Output, ToolCallID: string(output.CallID)})
		case canon.ReasoningItem, canon.CompactionMarker, canon.LocalShellCall, canon.LocalShellOutput, canon.ToolSearchCall, canon.ToolSearchOutput:
			continue
		default:
			return nil, fmt.Errorf("customchat input: unsupported canonical item %T", req.Input[i])
		}
	}
	return out, nil
}

func standaloneCalls(items []canon.Item, i int) ([]toolCall, int, error) {
	var calls []toolCall
	j := i
	for ; j < len(items); j++ {
		call, ok := items[j].(canon.FunctionCall)
		if !ok {
			break
		}
		if call.CallID == "" {
			return nil, 0, fmt.Errorf("customchat input: function call is missing call id")
		}
		calls = append(calls, toolCall{
			ID:       string(call.CallID),
			Type:     "function",
			Function: funcCallWire{Name: string(call.Name), Arguments: string(call.Arguments)},
		})
	}
	if len(calls) == 0 {
		return nil, 0, fmt.Errorf("customchat input: unsupported canonical item %T", items[i])
	}
	return calls, j - i - 1, nil
}

func messageFrom(items []canon.Item, i int) (message, int, error) {
	m := items[i].(canon.Message)
	if m.Role != canon.RoleAssistant {
		content, err := contentFromContent(m.Content)
		if err != nil {
			return message{}, 0, err
		}
		return message{Role: roleWire(m.Role), Content: content}, 0, nil
	}
	text, err := textFromContent(m.Content)
	if err != nil {
		return message{}, 0, err
	}
	var calls []toolCall
	j := i + 1
	for ; j < len(items); j++ {
		call, ok := items[j].(canon.FunctionCall)
		if !ok {
			break
		}
		if call.CallID == "" {
			return message{}, 0, fmt.Errorf("customchat input: function call is missing call id")
		}
		calls = append(calls, toolCall{
			ID:       string(call.CallID),
			Type:     "function",
			Function: funcCallWire{Name: string(call.Name), Arguments: string(call.Arguments)},
		})
	}
	msg := message{Role: "assistant"}
	if text != "" {
		msg.Content = text
	}
	if len(calls) > 0 {
		msg.ToolCalls = calls
		return msg, j - i - 1, nil
	}
	return msg, 0, nil
}

func contentFromContent(content []canon.Content) (any, error) {
	if len(content) == 1 {
		if t, ok := content[0].(canon.TextContent); ok {
			return t.Text, nil
		}
	}
	if len(content) == 0 {
		return nil, nil
	}
	parts := make([]contentPart, 0, len(content))
	for _, c := range content {
		switch p := c.(type) {
		case canon.TextContent:
			parts = append(parts, contentPart{Type: "text", Text: p.Text})
		case canon.ImageContent:
			parts = append(parts, contentPart{Type: "image_url", ImageURL: &imageURL{URL: imageDataURL(p), Detail: p.Detail}})
		default:
			return nil, fmt.Errorf("customchat input: unsupported canonical content %T", c)
		}
	}
	return parts, nil
}

func textFromContent(content []canon.Content) (string, error) {
	texts := make([]string, 0, len(content))
	for _, c := range content {
		t, ok := c.(canon.TextContent)
		if !ok {
			return "", fmt.Errorf("customchat input: unsupported canonical content %T", c)
		}
		texts = append(texts, t.Text)
	}
	return strings.Join(texts, "\n\n"), nil
}

func toolOutputText(content []canon.Content) (string, error) {
	texts := make([]string, 0, len(content))
	for _, c := range content {
		t, ok := c.(canon.TextContent)
		if !ok {
			return "", fmt.Errorf("customchat input: tool result content %T is not representable in chat completions", c)
		}
		texts = append(texts, t.Text)
	}
	return strings.Join(texts, "\n\n"), nil
}

func imageDataURL(img canon.ImageContent) string {
	return "data:" + img.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(img.Data)
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
	case canon.EffortOff:
		return "off"
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

func verbosityWire(v canon.TextVerbosity) string {
	switch v {
	case canon.VerbosityLow:
		return "low"
	case canon.VerbosityMedium:
		return "medium"
	case canon.VerbosityHigh:
		return "high"
	default:
		return ""
	}
}

func responseFormatFrom(f *canon.TextFormat) (*responseFormat, error) {
	switch f.Type {
	case "json_schema":
		return &responseFormat{
			Type: "json_schema",
			JSONSchema: &wireSchema{
				Name:        f.Name,
				Description: f.Description,
				Schema:      json.RawMessage(f.Schema),
				Strict:      f.Strict,
			},
		}, nil
	case "text", "json_object":
		return &responseFormat{Type: f.Type}, nil
	default:
		return nil, fmt.Errorf("customchat input: text format %q is not representable in chat completions", f.Type)
	}
}

func toolsFrom(tools []canon.Tool) ([]tool, error) {
	out := make([]tool, 0, len(tools))
	for _, t := range tools {
		var def functionDef
		switch tt := t.(type) {
		case canon.FunctionTool:
			def = functionDef{Name: string(tt.Name), Description: tt.Description}
			if len(tt.Parameters) > 0 {
				def.Parameters = tt.Parameters
			}
			if tt.Strict {
				strict := true
				def.Strict = &strict
			}
		case canon.CustomToolDef:
			def = functionDef{Name: string(tt.Name), Description: tt.Description}
		default:
			continue
		}
		out = append(out, tool{Type: "function", Function: def})
	}
	return out, nil
}

func toolChoiceFrom(tc canon.ToolChoice) (json.RawMessage, bool, error) {
	switch t := tc.(type) {
	case nil:
		return nil, false, nil
	case canon.ToolAuto:
		return json.RawMessage(`"auto"`), true, nil
	case canon.ToolNone:
		return json.RawMessage(`"none"`), true, nil
	case canon.ToolRequired:
		return json.RawMessage(`"required"`), true, nil
	case canon.ToolNamed:
		raw, err := json.Marshal(namedToolChoice{Type: "function", Function: namedFunction{Name: string(t.Name)}})
		if err != nil {
			return nil, false, err
		}
		return raw, true, nil
	default:
		return nil, false, fmt.Errorf("customchat tool_choice: unsupported canonical tool choice %T", t)
	}
}

func chatURL(base string) string {
	return chatBase(base) + "/v1/chat/completions"
}

func chatBase(base string) string {
	u := strings.TrimRight(strings.TrimSpace(base), "/")
	u = strings.TrimSuffix(u, "/chat/completions")
	u = strings.TrimSuffix(u, "/v1")
	return u
}
