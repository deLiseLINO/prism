package anthropic

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"prism/internal/canon"
	"prism/internal/reasonenv"
)

type wireTextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type wireImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type wireBlock struct {
	Type      string           `json:"type"`
	Text      string           `json:"text,omitempty"`
	Source    *wireImageSource `json:"source,omitempty"`
	Thinking  string           `json:"thinking,omitempty"`
	Data      string           `json:"data,omitempty"`
	Signature string           `json:"signature,omitempty"`
	ID        string           `json:"id,omitempty"`
	Name      string           `json:"name,omitempty"`
	Input     json.RawMessage  `json:"input,omitempty"`
	ToolUseID string           `json:"tool_use_id,omitempty"`
	Content   []wireBlock      `json:"content,omitempty"`
}

type wireMessage struct {
	Role    string      `json:"role"`
	Content []wireBlock `json:"content"`
}

type wireTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type wireToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

type wireThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

type wireRequest struct {
	Model         string          `json:"model"`
	Messages      []wireMessage   `json:"messages"`
	System        []wireTextBlock `json:"system,omitempty"`
	Tools         []wireTool      `json:"tools,omitempty"`
	ToolChoice    *wireToolChoice `json:"tool_choice,omitempty"`
	MaxTokens     int             `json:"max_tokens,omitempty"`
	Temperature   *float64        `json:"temperature,omitempty"`
	TopP          *float64        `json:"top_p,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
	Stream        bool            `json:"stream,omitempty"`
	Thinking      *wireThinking   `json:"thinking,omitempty"`
}

type budgetRow map[canon.ReasoningEffort]int

var referenceBudgets = budgetRow{
	canon.EffortMinimal: 1024,
	canon.EffortLow:     4096,
	canon.EffortMedium:  8192,
	canon.EffortHigh:    16384,
	canon.EffortXHigh:   24576,
	canon.EffortMax:     28672,
}

var budgetTable = map[string]budgetRow{
	"sonnet": referenceBudgets,
	"opus":   referenceBudgets,
	"haiku":  referenceBudgets,
	"fable":  referenceBudgets,
}

var familyPattern = regexp.MustCompile(`(?:^|/)claude-([a-z]+)-\d+`)

func budgetFor(model canon.ModelID, effort canon.ReasoningEffort) (int, bool) {
	row, ok := budgetTable[modelFamily(model)]
	if !ok {
		row = referenceBudgets
	}
	budget, ok := row[effort]
	return budget, ok
}

func modelFamily(model canon.ModelID) string {
	m := familyPattern.FindStringSubmatch(strings.ToLower(string(model)))
	if m == nil {
		return ""
	}
	return m[1]
}

func (r *Runner) render(request canon.Request, streaming bool) (*outbound, error) {
	wr, err := r.buildWireRequest(request, streaming)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(wr)
	if err != nil {
		return nil, fmt.Errorf("anthropic: encode request: %w", err)
	}
	return &outbound{body: body}, nil
}

func (r *Runner) buildWireRequest(request canon.Request, streaming bool) (*wireRequest, error) {
	system, err := systemBlocks(request.Instructions)
	if err != nil {
		return nil, err
	}
	messages, err := r.messagesFromItems(request.Input)
	if err != nil {
		return nil, err
	}
	tools, err := toolsFrom(request.Tools)
	if err != nil {
		return nil, err
	}
	choice, err := toolChoiceFrom(request.ToolChoice)
	if err != nil {
		return nil, err
	}
	maxTokens := request.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	wr := &wireRequest{
		Model:         string(request.Model),
		Messages:      messages,
		System:        system,
		Tools:         tools,
		ToolChoice:    choice,
		MaxTokens:     maxTokens,
		Temperature:   request.Sampling.Temperature,
		TopP:          request.Sampling.TopP,
		StopSequences: request.Sampling.Stop,
		Stream:        streaming,
	}
	if request.Reasoning.Effort != 0 {
		budget, ok := budgetFor(request.Model, request.Reasoning.Effort)
		if !ok {
			return nil, fmt.Errorf("anthropic: no thinking budget for model %q effort %d", request.Model, request.Reasoning.Effort)
		}
		if budget < minThinkingBudget {
			return nil, fmt.Errorf("anthropic: thinking budget %d below minimum %d", budget, minThinkingBudget)
		}
		wr.Thinking = &wireThinking{Type: "enabled", BudgetTokens: budget}
		floor := budget + thinkingHeadroom
		if maxTokens < floor {
			maxTokens = floor
		}
		if maxTokens > maxTokensCeiling {
			maxTokens = maxTokensCeiling
		}
		wr.MaxTokens = maxTokens
		wr.Temperature = nil
		wr.TopP = nil
	}
	return wr, nil
}

func systemBlocks(contents []canon.Content) ([]wireTextBlock, error) {
	var blocks []wireTextBlock
	for _, c := range contents {
		text, ok := c.(canon.TextContent)
		if !ok {
			return nil, fmt.Errorf("anthropic: unsupported system content %T", c)
		}
		if text.Text == "" {
			continue
		}
		blocks = append(blocks, wireTextBlock{Type: "text", Text: text.Text})
	}
	return blocks, nil
}

func roleString(role canon.Role) (string, error) {
	switch role {
	case canon.RoleUser:
		return "user", nil
	case canon.RoleAssistant:
		return "assistant", nil
	default:
		return "", fmt.Errorf("anthropic: unsupported message role %d", role)
	}
}

func contentBlocks(contents []canon.Content) ([]wireBlock, error) {
	var blocks []wireBlock
	for _, c := range contents {
		switch v := c.(type) {
		case canon.TextContent:
			if v.Text == "" {
				continue
			}
			blocks = append(blocks, wireBlock{Type: "text", Text: v.Text})
		case canon.ImageContent:
			blocks = append(blocks, wireBlock{Type: "image", Source: &wireImageSource{
				Type:      "base64",
				MediaType: v.MIMEType,
				Data:      base64.StdEncoding.EncodeToString(v.Data),
			}})
		default:
			return nil, fmt.Errorf("anthropic: unsupported content %T", c)
		}
	}
	return blocks, nil
}

func (r *Runner) messagesFromItems(items []canon.Item) ([]wireMessage, error) {
	var messages []wireMessage
	appendBlock := func(role string, block wireBlock) {
		if n := len(messages); n > 0 && messages[n-1].Role == role {
			messages[n-1].Content = append(messages[n-1].Content, block)
			return
		}
		messages = append(messages, wireMessage{Role: role, Content: []wireBlock{block}})
	}
	for _, item := range items {
		switch v := item.(type) {
		case canon.Message:
			role, err := roleString(v.Role)
			if err != nil {
				return nil, err
			}
			blocks, err := contentBlocks(v.Content)
			if err != nil {
				return nil, err
			}
			if len(blocks) == 0 {
				continue
			}
			for _, b := range blocks {
				appendBlock(role, b)
			}
		case canon.ReasoningItem:
			signature, ok := r.replaySignature(v)
			if !ok {
				return nil, fmt.Errorf("anthropic: thinking item %q has no signature for replay", v.ID)
			}
			if env, isEnv := reasonenv.Decode(signature); isEnv && len(env.Red) > 0 {
				for _, data := range env.Red {
					appendBlock("assistant", wireBlock{Type: "redacted_thinking", Data: data})
				}
				continue
			}
			appendBlock("assistant", wireBlock{Type: "thinking", Thinking: v.Content, Signature: signature})
		case canon.FunctionCall:
			args := v.Arguments
			if len(args) == 0 {
				args = []byte("{}")
			}
			if !json.Valid(args) {
				return nil, fmt.Errorf("anthropic: tool call %q has malformed arguments", v.CallID)
			}
			appendBlock("assistant", wireBlock{Type: "tool_use", ID: string(v.CallID), Name: string(v.Name), Input: json.RawMessage(args)})
		case canon.FunctionOutput:
			blocks, err := contentBlocks(v.Output)
			if err != nil {
				return nil, err
			}
			if len(blocks) == 0 {
				return nil, fmt.Errorf("anthropic: tool result %q has no representable content", v.CallID)
			}
			appendBlock("user", wireBlock{Type: "tool_result", ToolUseID: string(v.CallID), Content: blocks})
		default:
			return nil, fmt.Errorf("anthropic: unsupported input item %T", item)
		}
	}
	return messages, nil
}

func (r *Runner) replaySignature(item canon.ReasoningItem) (string, bool) {
	if !item.State.IsEmpty() && item.State.Store == stateStoreName {
		if blob, ok := r.state.get(item.State.Key); ok {
			return string(blob), true
		}
	}
	if item.Signature != "" {
		return item.Signature, true
	}
	return "", false
}

func toolsFrom(tools []canon.Tool) ([]wireTool, error) {
	var wire []wireTool
	for _, t := range tools {
		fn, ok := t.(canon.FunctionTool)
		if !ok {
			return nil, fmt.Errorf("anthropic: unsupported tool %T", t)
		}
		params := fn.Parameters
		if len(params) == 0 {
			params = []byte("{}")
		}
		if !json.Valid(params) {
			return nil, fmt.Errorf("anthropic: tool %q has malformed parameters", fn.Name)
		}
		wire = append(wire, wireTool{Name: string(fn.Name), Description: fn.Description, InputSchema: json.RawMessage(params)})
	}
	return wire, nil
}

func toolChoiceFrom(choice canon.ToolChoice) (*wireToolChoice, error) {
	switch v := choice.(type) {
	case nil:
		return nil, nil
	case canon.ToolAuto:
		return &wireToolChoice{Type: "auto"}, nil
	case canon.ToolNone:
		return &wireToolChoice{Type: "none"}, nil
	case canon.ToolRequired:
		return &wireToolChoice{Type: "any"}, nil
	case canon.ToolNamed:
		return &wireToolChoice{Type: "tool", Name: string(v.Name)}, nil
	case canon.ToolAllowed:
		if v.Mode == canon.AllowedRequired {
			return &wireToolChoice{Type: "any"}, nil
		}
		return &wireToolChoice{Type: "auto"}, nil
	default:
		return nil, fmt.Errorf("anthropic: unsupported tool choice %T", choice)
	}
}

func decodeJSON(body []byte) (map[string]any, error) {
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil, fmt.Errorf("anthropic: empty JSON body")
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}
