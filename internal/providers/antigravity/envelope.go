package antigravity

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"prism/internal/canon"
)

type envelope struct {
	Model       string        `json:"model"`
	UserAgent   string        `json:"userAgent"`
	RequestType string        `json:"requestType"`
	Project     string        `json:"project"`
	RequestID   string        `json:"requestId"`
	Request     geminiRequest `json:"request"`
}

type geminiRequest struct {
	Contents          []geminiContent         `json:"contents,omitempty"`
	SystemInstruction *geminiContent          `json:"systemInstruction,omitempty"`
	Tools             []geminiTool            `json:"tools,omitempty"`
	ToolConfig        *geminiToolConfig       `json:"toolConfig,omitempty"`
	GenerationConfig  *geminiGenerationConfig `json:"generationConfig,omitempty"`
	SessionID         string                  `json:"sessionId,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	Thought          bool                    `json:"thought,omitempty"`
	InlineData       *geminiInlineData       `json:"inline_data,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
	ThoughtSignature string                  `json:"thoughtSignature,omitempty"`
}

type geminiInlineData struct {
	MimeType string `json:"mime_type"`
	Data     string `json:"data"`
}

type geminiFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
	ID   string          `json:"id,omitempty"`
}

type geminiFunctionResponse struct {
	Name     string `json:"name"`
	Response struct {
		Result string `json:"result"`
	} `json:"response"`
	ID string `json:"id,omitempty"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFunctionDeclaration `json:"functionDeclarations"`
}

type geminiFunctionDeclaration struct {
	Name                 string          `json:"name"`
	Description          string          `json:"description,omitempty"`
	Parameters           json.RawMessage `json:"parameters,omitempty"`
	ParametersJSONSchema json.RawMessage `json:"parametersJsonSchema,omitempty"`
}

type geminiToolConfig struct {
	FunctionCallingConfig geminiFunctionCallingConfig `json:"functionCallingConfig"`
}

type geminiFunctionCallingConfig struct {
	Mode                 string   `json:"mode"`
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}

type geminiGenerationConfig struct {
	MaxOutputTokens int             `json:"maxOutputTokens,omitempty"`
	Temperature     *float64        `json:"temperature,omitempty"`
	TopP            *float64        `json:"topP,omitempty"`
	StopSequences   []string        `json:"stopSequences,omitempty"`
	ThinkingConfig  json.RawMessage `json:"thinkingConfig,omitempty"`
}

type wireCall struct {
	name string
	id   string
}

type envelopeBuilder struct {
	contents   []geminiContent
	claude     bool
	pendingSig string
}

func BuildEnvelope(req canon.Request, project, requestID, sessionID string) ([]byte, error) {
	b := &envelopeBuilder{claude: isClaudeModel(string(req.Model))}
	system, err := b.systemInstruction(req.Instructions)
	if err != nil {
		return nil, err
	}
	if err := b.buildContents(req.Input); err != nil {
		return nil, err
	}
	tools, toolConfig, err := b.tools(req.Tools, req.ToolChoice)
	if err != nil {
		return nil, err
	}
	present := presenceSnapshot()
	f := familyForLogical(string(req.Model), present)
	ef := resolvedEffort(req, f)
	wire := resolveWireModel(string(req.Model), ef, present)
	body := geminiRequest{
		Contents:          b.contents,
		SystemInstruction: system,
		Tools:             tools,
		ToolConfig:        toolConfig,
		GenerationConfig:  generationConfig(req, f, ef, wire),
		SessionID:         sessionID,
	}
	if b.claude && req.ToolChoice != nil {
		switch req.ToolChoice.(type) {
		case canon.ToolNone:
			body.Tools = nil
			body.ToolConfig = nil
		default:
			body.ToolConfig = &geminiToolConfig{FunctionCallingConfig: geminiFunctionCallingConfig{Mode: "VALIDATED"}}
		}
	}
	sanitizeSignatures(body.Contents)
	if b.claude && len(body.Contents) > 0 && body.Contents[len(body.Contents)-1].Role == "model" {
		body.Contents = append(body.Contents, geminiContent{Role: "user", Parts: []geminiPart{{Text: continueNudge}}})
	}
	env := envelope{
		Model:       wire,
		UserAgent:   EnvelopeUserAgent,
		RequestType: RequestType,
		Project:     project,
		RequestID:   requestID,
		Request:     body,
	}
	return marshalCompact(env)
}

func (g *geminiGenerationConfig) empty() bool {
	return g.MaxOutputTokens == 0 && g.Temperature == nil && g.TopP == nil && len(g.StopSequences) == 0 && len(g.ThinkingConfig) == 0
}

func isClaudeModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "claude")
}

func marshalCompact(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func (b *envelopeBuilder) systemInstruction(instructions []canon.Content) (*geminiContent, error) {
	var texts []string
	for _, c := range instructions {
		switch v := c.(type) {
		case canon.TextContent:
			if v.Text != "" {
				texts = append(texts, v.Text)
			}
		default:
			return nil, invalidRequestf("system instruction content %T is not representable on the antigravity wire", c)
		}
	}
	if len(texts) == 0 {
		return nil, nil
	}
	return &geminiContent{Parts: []geminiPart{{Text: strings.Join(texts, "\n\n")}}}, nil
}

func (b *envelopeBuilder) lastModelIndex() int {
	for i := len(b.contents) - 1; i >= 0; i-- {
		if b.contents[i].Role == "model" {
			return i
		}
	}
	return -1
}

func (b *envelopeBuilder) buildContents(items []canon.Item) error {
	for i := 0; i < len(items); {
		switch item := items[i].(type) {
		case canon.Message:
			c, err := messageContent(item)
			if err != nil {
				return err
			}
			if c != nil {
				b.contents = append(b.contents, *c)
			}
			i++
		case canon.ReasoningItem:
			if err := b.reasoning(item); err != nil {
				return err
			}
			i++
		case canon.FunctionCall:
			n, err := b.functionCalls(items[i:])
			if err != nil {
				return err
			}
			i += n
		case canon.FunctionOutput:
			b.orphanOutput(item)
			i++
		default:
			return invalidRequestf("canon item %T is not representable on the antigravity wire", item)
		}
	}
	for i := range b.contents {
		if b.contents[i].Role == "user" && len(b.contents[i].Parts) == 0 {
			b.contents[i].Parts = []geminiPart{{Text: emptyPlaceholder}}
		}
	}
	return nil
}

func messageContent(msg canon.Message) (*geminiContent, error) {
	role := "user"
	switch msg.Role {
	case canon.RoleAssistant:
		role = "model"
	case canon.RoleUser, canon.RoleSystem, canon.RoleDeveloper:
	default:
		return nil, invalidRequestf("canon message role %d is not representable on the antigravity wire", msg.Role)
	}
	var parts []geminiPart
	for _, c := range msg.Content {
		switch v := c.(type) {
		case canon.TextContent:
			if v.Text != "" {
				parts = append(parts, geminiPart{Text: v.Text})
			}
		case canon.ImageContent:
			parts = append(parts, geminiPart{InlineData: &geminiInlineData{
				MimeType: v.MIMEType,
				Data:     base64.StdEncoding.EncodeToString(v.Data),
			}})
		default:
			return nil, invalidRequestf("message content %T is not representable on the antigravity wire", c)
		}
	}
	if len(parts) == 0 {
		if role == "user" {
			return &geminiContent{Role: role, Parts: []geminiPart{{Text: emptyPlaceholder}}}, nil
		}
		return nil, nil
	}
	return &geminiContent{Role: role, Parts: parts}, nil
}

func (b *envelopeBuilder) reasoning(item canon.ReasoningItem) error {
	if item.Content == "" && len(item.Summary) == 0 {
		return nil
	}
	parts := []geminiPart{}
	if item.Content != "" {
		parts = append(parts, geminiPart{Text: item.Content, Thought: true})
	}
	if likelyRealSignature(item.Signature) {
		for i := range parts {
			parts[i].ThoughtSignature = item.Signature
		}
		b.pendingSig = item.Signature
	}
	if idx := b.lastModelIndex(); idx >= 0 {
		b.contents[idx].Parts = append(b.contents[idx].Parts, parts...)
		return nil
	}
	b.contents = append(b.contents, geminiContent{Role: "model", Parts: parts})
	return nil
}

func (b *envelopeBuilder) functionCalls(items []canon.Item) (int, error) {
	var calls []canon.FunctionCall
	n := 0
	for _, item := range items {
		call, ok := item.(canon.FunctionCall)
		if !ok {
			break
		}
		calls = append(calls, call)
		n++
	}
	sig := b.pendingSig
	b.pendingSig = ""
	parts := make([]geminiPart, 0, len(calls))
	wireCalls := make([]wireCall, 0, len(calls))
	for _, call := range calls {
		name, err := wireToolName(call.Name)
		if err != nil {
			return 0, err
		}
		part := geminiPart{FunctionCall: &geminiFunctionCall{
			Name: name,
			Args: json.RawMessage(call.Arguments),
			ID:   string(call.CallID),
		}}
		callSig := sig
		if call.State.Store == signatureStore && likelyRealSignature(call.State.Key) {
			callSig = call.State.Key
		}
		if callSig == "" {
			callSig = signatureSentinel
		}
		part.ThoughtSignature = callSig
		parts = append(parts, part)
		wireCalls = append(wireCalls, wireCall{name: name, id: string(call.CallID)})
	}
	b.contents = append(b.contents, geminiContent{Role: "model", Parts: parts})

	var responseParts []geminiPart
	consumed := 0
	rest := items[n:]
	outputs := make([]canon.FunctionOutput, 0, len(rest))
	for _, item := range rest {
		out, ok := item.(canon.FunctionOutput)
		if !ok {
			break
		}
		outputs = append(outputs, out)
		consumed++
	}
	matched := make(map[string]bool, len(wireCalls))
	for _, out := range outputs {
		wire := b.popCall(wireCalls, matched, string(out.CallID))
		result, err := outputResultText(out)
		if err != nil {
			return 0, err
		}
		if wire == nil {
			label := string(out.CallID)
			responseParts = append(responseParts, geminiPart{Text: orphantToolResultPrefix + label + "]\n" + result})
			continue
		}
		responseParts = append(responseParts, geminiPart{FunctionResponse: &geminiFunctionResponse{
			Name: wire.name,
			ID:   wire.id,
		}})
		responseParts[len(responseParts)-1].FunctionResponse.Response.Result = result
		for _, c := range out.Output {
			if img, ok := c.(canon.ImageContent); ok {
				responseParts = append(responseParts, geminiPart{InlineData: &geminiInlineData{
					MimeType: img.MIMEType,
					Data:     base64.StdEncoding.EncodeToString(img.Data),
				}})
			}
		}
	}
	for _, wire := range wireCalls {
		if matched[wire.id] {
			continue
		}
		responseParts = append(responseParts, geminiPart{FunctionResponse: &geminiFunctionResponse{
			Name: wire.name,
			ID:   wire.id,
		}})
		responseParts[len(responseParts)-1].FunctionResponse.Response.Result = missingToolResultText
	}
	b.contents = append(b.contents, geminiContent{Role: "user", Parts: responseParts})
	return n + consumed, nil
}

func (b *envelopeBuilder) popCall(calls []wireCall, matched map[string]bool, callID string) *wireCall {
	if callID == "" {
		return nil
	}
	for i := range calls {
		if calls[i].id == callID && !matched[callID] {
			matched[callID] = true
			return &calls[i]
		}
	}
	return nil
}

func (b *envelopeBuilder) orphanOutput(out canon.FunctionOutput) {
	result, _ := outputResultText(out)
	label := string(out.CallID)
	b.contents = append(b.contents, geminiContent{Role: "user", Parts: []geminiPart{{
		Text: orphantToolResultPrefix + label + "]\n" + result,
	}}})
}

func outputResultText(out canon.FunctionOutput) (string, error) {
	var texts []string
	for _, c := range out.Output {
		switch v := c.(type) {
		case canon.TextContent:
			if v.Text != "" {
				texts = append(texts, v.Text)
			}
		case canon.ImageContent:
		default:
			return "", invalidRequestf("tool output content %T is not representable on the antigravity wire", c)
		}
	}
	if len(texts) == 0 {
		return emptyToolOutputPlaceholder, nil
	}
	return strings.Join(texts, "\n"), nil
}

func wireToolName(name canon.ToolName) (string, error) {
	if name == "" {
		return "", invalidRequestf("tool call name is empty")
	}
	return string(name), nil
}

func (b *envelopeBuilder) tools(tools []canon.Tool, choice canon.ToolChoice) ([]geminiTool, *geminiToolConfig, error) {
	if len(tools) == 0 {
		return nil, nil, nil
	}
	var decls []geminiFunctionDeclaration
	for _, t := range tools {
		switch v := t.(type) {
		case canon.FunctionTool:
			decls = append(decls, geminiFunctionDeclaration{
				Name:        string(v.Name),
				Description: v.Description,
				Parameters:  sanitizeToolParameters(v.Parameters),
			})
		default:
			return nil, nil, invalidRequestf("tool definition %T is not representable on the antigravity wire", t)
		}
	}
	if len(decls) == 0 {
		return nil, nil, nil
	}
	var config *geminiToolConfig
	switch c := choice.(type) {
	case nil, canon.ToolAuto:
	default:
		switch v := c.(type) {
		case canon.ToolNone:
			config = &geminiToolConfig{FunctionCallingConfig: geminiFunctionCallingConfig{Mode: "NONE"}}
		case canon.ToolRequired:
			config = &geminiToolConfig{FunctionCallingConfig: geminiFunctionCallingConfig{Mode: "ANY"}}
		case canon.ToolNamed:
			config = &geminiToolConfig{FunctionCallingConfig: geminiFunctionCallingConfig{
				Mode:                 "ANY",
				AllowedFunctionNames: []string{string(v.Name)},
			}}
		default:
			return nil, nil, invalidRequestf("tool choice %T is not representable on the antigravity wire", choice)
		}
	}
	return []geminiTool{{FunctionDeclarations: decls}}, config, nil
}

func generationConfig(req canon.Request, f *family, ef effort, wire string) *geminiGenerationConfig {
	gc := &geminiGenerationConfig{
		Temperature:   req.Sampling.Temperature,
		TopP:          req.Sampling.TopP,
		StopSequences: req.Sampling.Stop,
	}
	if req.MaxOutputTokens > 0 {
		gc.MaxOutputTokens = req.MaxOutputTokens
	}
	if budget, ok := budgetThinking(f, ef); ok {
		maxTokens, wireBudget := accommodateBudget(req.MaxOutputTokens, budget, wireOutputTokenCap(wire))
		gc.MaxOutputTokens = maxTokens
		gc.ThinkingConfig = json.RawMessage(`{"includeThoughts":true,"thinkingBudget":` + strconv.Itoa(wireBudget) + `}`)
	} else {
		gc.ThinkingConfig = thinkingConfig(f, ef)
	}
	if gc.empty() {
		return nil
	}
	return gc
}

// budgetThinking reports the thinking budget for a budget-family effort: the
// one thinking mode whose tokens share the maxOutputTokens slot with output.
func budgetThinking(f *family, ef effort) (int, bool) {
	if f == nil || f.thinking != "budget" || ef == "" || ef == effortOff {
		return 0, false
	}
	return googleThinkingBudget(ef, f), true
}

// minOutputTokens is the output floor the budget clamp keeps when a wire
// ceiling leaves no room for the full budget.
const minOutputTokens = 1024

// accommodateBudget resolves the maxOutputTokens and thinkingBudget pair
// that keeps a budget request valid on the upstream. The backend maps
// thinkingBudget onto anthropic thinking.budget_tokens and maxOutputTokens
// onto max_tokens, then rejects any request whose budget eats the whole
// token slot. The caller's cap is desired output, so the budget rides on top
// of it, bounded by the wire's ceiling; with no caller cap the flat unknown
// ceiling stands in, which is the fixed cap the real IDE pins. A ceiling too
// small for the budget shrinks the budget instead of the output floor.
func accommodateBudget(callerCap, budget, ceiling int) (maxTokens, wireBudget int) {
	maxTokens = outputCapWhenUnknown
	if callerCap > 0 {
		maxTokens = callerCap + budget
	}
	maxTokens = min(maxTokens, ceiling)
	if maxTokens <= budget {
		return maxTokens, max(0, maxTokens-minOutputTokens)
	}
	return maxTokens, budget
}

// thinkingConfig emits the family-aware thinkingConfig for a request, or nil
// when no config is called for (the request is non-family, carries no effort,
// or carries no reasoning and no suppression surface). Omitting thinkingConfig
// on family models would re-apply the per-id baked server default, so off is
// emitted explicitly whenever the family can suppress. An unset effort on a
// non-requiresEffort family stays unset too: resolvedEffort only clamps
// requiresEffort families, whose endpoints reject omitted thinking outright.
func thinkingConfig(f *family, ef effort) json.RawMessage {
	if f == nil || ef == "" {
		return nil
	}
	if ef == effortOff {
		if !f.suppressWhenOff {
			return nil
		}
		if f.thinking == "google-level" {
			return json.RawMessage(`{"includeThoughts":false,"thinkingLevel":"MINIMAL"}`)
		}
		return json.RawMessage(`{"includeThoughts":false,"thinkingBudget":0}`)
	}
	if f.thinking == "google-level" {
		return json.RawMessage(`{"includeThoughts":true,"thinkingLevel":"` + googleThinkingLevel(ef, f) + `"}`)
	}
	return nil
}

// resolvedEffort maps the request effort onto the antigravity effort domain.
// An unset effort on a requiresEffort family clamps to the family's lowest
// supported effort — those endpoints reject omitted thinking outright, and
// the clamp keeps the wire request valid without inventing a default the
// caller never chose. prism carries no effort surface for non-family models,
// so this never rewrites a bare id's request.
func resolvedEffort(req canon.Request, f *family) effort {
	switch req.Reasoning.Effort {
	case canon.EffortMinimal:
		return effortMinimal
	case canon.EffortLow:
		return effortLow
	case canon.EffortMedium:
		return effortMedium
	case canon.EffortHigh:
		return effortHigh
	case canon.EffortXHigh:
		return effortXHigh
	case canon.EffortMax:
		return effortMax
	case canon.EffortOff:
		return effortOff
	}
	if f != nil && f.requiresEffort && len(f.efforts) > 0 {
		return f.efforts[0]
	}
	return ""
}

// googleThinkingLevel maps an effort to Google's thinkingLevel enum. A
// collapsed family that routes minimal onto the same wire id as low must emit
// LOW — the -low SKUs reject MINIMAL.
func googleThinkingLevel(ef effort, f *family) string {
	if ef == effortMinimal && f.routing[effortMinimal] == f.routing[effortLow] {
		return "LOW"
	}
	switch ef {
	case effortMinimal:
		return "MINIMAL"
	case effortLow:
		return "LOW"
	case effortMedium:
		return "MEDIUM"
	default:
		return "HIGH"
	}
}

// googleThinkingBudget resolves the budget-ladder token count for an effort,
// with the family's own effortBudgets overriding the generic ladder.
func googleThinkingBudget(ef effort, f *family) int {
	if budget, ok := f.effortBudgets[ef]; ok {
		return budget
	}
	switch ef {
	case effortMinimal:
		return 1024
	case effortLow:
		return 4096
	case effortMedium:
		return 8192
	case effortHigh:
		return 16384
	case effortXHigh:
		return 24575
	default:
		return 32768
	}
}

func sanitizeSignatures(contents []geminiContent) {
	for i := range contents {
		isModel := contents[i].Role == "model"
		kept := contents[i].Parts[:0]
		for _, part := range contents[i].Parts {
			if !isModel {
				part.ThoughtSignature = ""
				kept = append(kept, part)
				continue
			}
			if part.Thought && !likelyRealSignature(part.ThoughtSignature) {
				continue
			}
			kept = append(kept, part)
		}
		contents[i].Parts = kept
	}
}

func invalidRequestf(format string, args ...any) error {
	return fmt.Errorf("antigravity: "+format, args...)
}
