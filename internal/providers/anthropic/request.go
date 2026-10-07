package anthropic

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/reasonenv"
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
	Thinking  *string          `json:"thinking,omitempty"`
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
	Type                   string `json:"type"`
	Name                   string `json:"name,omitempty"`
	DisableParallelToolUse *bool  `json:"disable_parallel_tool_use,omitempty"`
}

type wireThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

type wireOutputFormat struct {
	Type   string          `json:"type"`
	Schema json.RawMessage `json:"schema"`
}

type wireOutputConfig struct {
	Format wireOutputFormat `json:"format"`
}

type wireRequest struct {
	Model         string            `json:"model"`
	Messages      []wireMessage     `json:"messages"`
	System        []wireTextBlock   `json:"system,omitempty"`
	Tools         []wireTool        `json:"tools,omitempty"`
	ToolChoice    *wireToolChoice   `json:"tool_choice,omitempty"`
	MaxTokens     int               `json:"max_tokens,omitempty"`
	Temperature   *float64          `json:"temperature,omitempty"`
	TopP          *float64          `json:"top_p,omitempty"`
	StopSequences []string          `json:"stop_sequences,omitempty"`
	Stream        bool              `json:"stream,omitempty"`
	Thinking      *wireThinking     `json:"thinking,omitempty"`
	OutputConfig  *wireOutputConfig `json:"output_config,omitempty"`
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
	if request.Text.Verbosity != 0 && request.Text.Verbosity != canon.VerbosityDefault {
		return nil, errors.New("anthropic: text verbosity is not representable on this wire")
	}
	messages, folded, err := r.messagesFromItems(request.Input)
	if err != nil {
		return nil, err
	}
	system, err := systemBlocks(slices.Concat(request.Instructions, folded))
	if err != nil {
		return nil, err
	}
	if allowed, ok := request.ToolChoice.(canon.ToolAllowed); ok && len(allowed.Tools) > 0 {
		filtered := make([]canon.Tool, 0, len(allowed.Tools))
		for _, name := range allowed.Tools {
			found := false
			for _, tool := range request.Tools {
				switch def := tool.(type) {
				case canon.FunctionTool:
					if def.Name == name {
						filtered = append(filtered, tool)
						found = true
					}
				case canon.CustomToolDef:
					if def.Name == name {
						filtered = append(filtered, tool)
						found = true
					}
				}
			}
			if !found {
				return nil, fmt.Errorf("anthropic: allowed tool %q has no declaration", name)
			}
		}
		request.Tools = filtered
	}
	tools, skipped, err := toolsFrom(request.Tools)
	if err != nil {
		return nil, err
	}
	if len(skipped) > 0 {
		r.log.Warn("anthropic: skipped tools the upstream cannot accept", "count", len(skipped), "tools", skipped)
	}
	choice, err := toolChoiceFrom(request.ToolChoice)
	if err != nil {
		return nil, err
	}
	if request.Sampling.ParallelToolCalls != nil && !*request.Sampling.ParallelToolCalls && len(tools) > 0 {
		if choice == nil {
			choice = &wireToolChoice{Type: "auto"}
		}
		if choice.Type != "none" {
			disable := true
			choice.DisableParallelToolUse = &disable
		}
	}
	outputConfig, err := outputConfigFrom(request.Text.Format)
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
		OutputConfig:  outputConfig,
	}
	if request.Reasoning.Effort != 0 && request.Reasoning.Effort != canon.EffortOff && !forcesToolUse(choice) {
		if request.Sampling.Temperature != nil || request.Sampling.TopP != nil {
			return nil, errors.New("anthropic: sampling controls are not representable with extended thinking")
		}
		budget, ok := budgetFor(request.Model, request.Reasoning.Effort)
		if !ok {
			return nil, fmt.Errorf("anthropic: no thinking budget for model %q effort %d", request.Model, request.Reasoning.Effort)
		}
		if budget < minThinkingBudget {
			return nil, fmt.Errorf("anthropic: thinking budget %d below minimum %d", budget, minThinkingBudget)
		}
		wr.Thinking = &wireThinking{Type: "enabled", BudgetTokens: budget}
		// Only a value prism raises is capped; the client's own limit stands.
		if floor := min(budget+thinkingHeadroom, maxTokensCeiling); maxTokens < floor {
			maxTokens = floor
		}
		wr.MaxTokens = maxTokens
		wr.Temperature = nil
		wr.TopP = nil
	}
	return wr, nil
}

// Upstream rejects extended thinking next to a forced tool choice, so the
// forced call wins over the optional reasoning.
func forcesToolUse(choice *wireToolChoice) bool {
	return choice != nil && (choice.Type == "any" || choice.Type == "tool")
}

func outputConfigFrom(format *canon.TextFormat) (*wireOutputConfig, error) {
	if format == nil {
		return nil, nil
	}
	if format.Type != "json_schema" {
		return nil, fmt.Errorf("anthropic: unsupported text format %q", format.Type)
	}
	if format.Strict != nil && !*format.Strict {
		return nil, errors.New("anthropic: non-strict JSON schema output is not supported")
	}
	var schema map[string]json.RawMessage
	if json.Unmarshal(format.Schema, &schema) != nil || schema == nil {
		return nil, errors.New("anthropic: output format requires a JSON schema object")
	}
	return &wireOutputConfig{Format: wireOutputFormat{Type: "json_schema", Schema: format.Schema}}, nil
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

func (r *Runner) messagesFromItems(items []canon.Item) ([]wireMessage, []canon.Content, error) {
	var messages []wireMessage
	var system []canon.Content
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
			if v.Role == canon.RoleSystem || v.Role == canon.RoleDeveloper {
				system = append(system, v.Content...)
				continue
			}
			role, err := roleString(v.Role)
			if err != nil {
				return nil, nil, err
			}
			blocks, err := contentBlocks(v.Content)
			if err != nil {
				return nil, nil, err
			}
			if len(blocks) == 0 {
				continue
			}
			for _, b := range blocks {
				appendBlock(role, b)
			}
		case canon.ReasoningItem:
			signature, fromState := r.replaySignature(v)
			for _, block := range thinkingBlocks(signature, fromState, v.Content) {
				appendBlock("assistant", block)
			}
		case canon.FunctionCall:
			args := v.Arguments
			if len(args) == 0 {
				args = []byte("{}")
			}
			if !json.Valid(args) {
				return nil, nil, fmt.Errorf("anthropic: tool call %q has malformed arguments", v.CallID)
			}
			appendBlock("assistant", wireBlock{Type: "tool_use", ID: string(v.CallID), Name: string(v.Name), Input: json.RawMessage(args)})
		case canon.FunctionOutput:
			blocks, err := contentBlocks(v.Output)
			if err != nil {
				return nil, nil, err
			}
			appendBlock("user", wireBlock{Type: "tool_result", ToolUseID: string(v.CallID), Content: blocks})
		case canon.CustomToolCall:
			if v.CallID == "" {
				return nil, nil, errors.New("anthropic: custom tool call has no call id")
			}
			input, err := json.Marshal(customToolInput{Input: v.Input})
			if err != nil {
				return nil, nil, fmt.Errorf("anthropic: encode custom tool call %q: %w", v.CallID, err)
			}
			appendBlock("assistant", wireBlock{Type: "tool_use", ID: string(v.CallID), Name: string(v.Name), Input: input})
		case canon.CustomToolOutput:
			if v.CallID == "" {
				return nil, nil, errors.New("anthropic: custom tool output has no call id")
			}
			result := wireBlock{Type: "tool_result", ToolUseID: string(v.CallID)}
			if v.Content != nil {
				blocks, err := contentBlocks(v.Content)
				if err != nil {
					return nil, nil, err
				}
				result.Content = blocks
			} else if v.Output != "" {
				result.Content = []wireBlock{{Type: "text", Text: v.Output}}
			}
			appendBlock("user", result)
		default:
			return nil, nil, fmt.Errorf("anthropic: unsupported input item %T", item)
		}
	}
	return messages, system, nil
}

func (r *Runner) replaySignature(item canon.ReasoningItem) (signature string, fromState bool) {
	if !item.State.IsEmpty() && item.State.Store == stateStoreName {
		if blob, ok := r.state.get(item.State.Key); ok {
			return string(blob), true
		}
	}
	return item.Signature, false
}

const minSignatureLength = 16

var (
	openAIIDPrefix   = regexp.MustCompile(`(?i)^(fc|call|msg|rs|resp|reasoning|item|ws|tool|func|function)[-_]`)
	signatureCharset = regexp.MustCompile(`^[A-Za-z0-9+/_=-]+$`)
)

func looksLikeAnthropicSignature(signature string) bool {
	if len(signature) < minSignatureLength || openAIIDPrefix.MatchString(signature) {
		return false
	}
	return signatureCharset.MatchString(signature)
}

func thinkingBlocks(signature string, fromState bool, content string) []wireBlock {
	if env, isEnv := reasonenv.Decode(signature); isEnv {
		var blocks []wireBlock
		for _, data := range env.Red {
			if data != "" {
				blocks = append(blocks, wireBlock{Type: "redacted_thinking", Data: data})
			}
		}
		return blocks
	}
	if signature == "" || (!fromState && !looksLikeAnthropicSignature(signature)) {
		return nil
	}
	return []wireBlock{{Type: "thinking", Thinking: &content, Signature: signature}}
}

const (
	applyPatchToolName     = "apply_patch"
	applyPatchInputDesc    = "Raw tool input. For apply_patch, begin exactly with `*** Begin Patch` (no trailing `***`), then use its standard patch envelope."
	genericCustomInputDesc = "Raw freeform input for this tool."
)

type customInputProperty struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

type customInputSchema struct {
	Type       string                         `json:"type"`
	Properties map[string]customInputProperty `json:"properties"`
	Required   []string                       `json:"required"`
}

type customToolInput struct {
	Input string `json:"input"`
}

type customTools map[canon.ToolName]struct{}

func customToolNames(tools []canon.Tool) customTools {
	var names customTools
	for _, t := range tools {
		if def, ok := t.(canon.CustomToolDef); ok {
			if names == nil {
				names = make(customTools)
			}
			names[def.Name] = struct{}{}
		}
	}
	return names
}

func (c customTools) has(name canon.ToolName) bool {
	_, ok := c[name]
	return ok
}

func customToolSchema(name canon.ToolName) (json.RawMessage, error) {
	desc := genericCustomInputDesc
	if name == applyPatchToolName {
		desc = applyPatchInputDesc
	}
	return json.Marshal(customInputSchema{
		Type:       "object",
		Properties: map[string]customInputProperty{"input": {Type: "string", Description: desc}},
		Required:   []string{"input"},
	})
}

func toolsFrom(tools []canon.Tool) ([]wireTool, []string, error) {
	var wire []wireTool
	var skipped []string
	for _, t := range tools {
		switch v := t.(type) {
		case canon.FunctionTool:
			schema, err := objectSchema(v.Parameters)
			if err != nil {
				return nil, nil, fmt.Errorf("anthropic: tool %q has malformed parameters", v.Name)
			}
			if !toolNamePattern.MatchString(string(v.Name)) || hasInvalidPropertyKey(schema) {
				skipped = append(skipped, string(v.Name))
				continue
			}
			wire = append(wire, wireTool{Name: string(v.Name), Description: v.Description, InputSchema: schema})
		case canon.CustomToolDef:
			schema, err := customToolSchema(v.Name)
			if err != nil {
				return nil, nil, fmt.Errorf("anthropic: encode custom tool %q schema: %w", v.Name, err)
			}
			if !toolNamePattern.MatchString(string(v.Name)) {
				skipped = append(skipped, string(v.Name))
				continue
			}
			wire = append(wire, wireTool{Name: string(v.Name), Description: v.Description, InputSchema: schema})
		default:
			return nil, nil, fmt.Errorf("anthropic: unsupported tool %T", t)
		}
	}
	return wire, skipped, nil
}

var (
	toolNamePattern    = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	propertyKeyPattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,64}$`)
)

func hasInvalidPropertyKey(schema json.RawMessage) bool {
	var node any
	if err := json.Unmarshal(schema, &node); err != nil {
		return false
	}
	return schemaHasInvalidPropertyKey(node)
}

func schemaHasInvalidPropertyKey(node any) bool {
	switch v := node.(type) {
	case map[string]any:
		for key, child := range v {
			if props, ok := child.(map[string]any); ok && key == "properties" {
				for name, sub := range props {
					if !propertyKeyPattern.MatchString(name) || schemaHasInvalidPropertyKey(sub) {
						return true
					}
				}
				continue
			}
			if schemaHasInvalidPropertyKey(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if schemaHasInvalidPropertyKey(child) {
				return true
			}
		}
	}
	return false
}

var schemaCombinatorKeys = []string{"oneOf", "anyOf", "allOf"}

func objectSchema(params []byte) (json.RawMessage, error) {
	root := map[string]json.RawMessage{}
	if len(strings.TrimSpace(string(params))) > 0 {
		var decoded any
		if err := json.Unmarshal(params, &decoded); err != nil {
			return nil, err
		}
		if _, isObject := decoded.(map[string]any); isObject {
			if err := json.Unmarshal(params, &root); err != nil {
				return nil, err
			}
		}
	}
	properties := rawObject(root["properties"])
	required := stringEntries(root["required"])
	for _, key := range schemaCombinatorKeys {
		var branches []json.RawMessage
		if err := json.Unmarshal(root[key], &branches); err != nil {
			continue
		}
		for _, branch := range branches {
			fields := rawObject(branch)
			for name, prop := range rawObject(fields["properties"]) {
				if _, exists := properties[name]; !exists {
					properties[name] = prop
				}
			}
			if key == "allOf" {
				required = appendUnique(required, stringEntries(fields["required"]))
			}
		}
	}
	for _, key := range schemaCombinatorKeys {
		delete(root, key)
	}
	root["type"] = json.RawMessage(`"object"`)
	encodedProperties, err := json.Marshal(properties)
	if err != nil {
		return nil, err
	}
	root["properties"] = encodedProperties
	delete(root, "required")
	if len(required) > 0 {
		encodedRequired, err := json.Marshal(required)
		if err != nil {
			return nil, err
		}
		root["required"] = encodedRequired
	}
	return json.Marshal(root)
}

func rawObject(raw json.RawMessage) map[string]json.RawMessage {
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return map[string]json.RawMessage{}
	}
	return fields
}

func stringEntries(raw json.RawMessage) []string {
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil
	}
	var out []string
	for _, entry := range entries {
		var s string
		if err := json.Unmarshal(entry, &s); err == nil && string(entry) != "null" {
			out = appendUnique(out, []string{s})
		}
	}
	return out
}

func appendUnique(dst, src []string) []string {
	for _, s := range src {
		if !slices.Contains(dst, s) {
			dst = append(dst, s)
		}
	}
	return dst
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
