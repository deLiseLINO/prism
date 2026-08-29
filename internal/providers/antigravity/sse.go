package antigravity

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"prism/internal/canon"
)

const maxSSEFrameBytes = 100 * 1024 * 1024

type streamFrame struct {
	Error    *frameError    `json:"error"`
	Response *frameResponse `json:"response"`
}

type frameError struct {
	Message string `json:"message"`
}

type frameResponse struct {
	Candidates    []frameCandidate `json:"candidates"`
	UsageMetadata *usageMetadata   `json:"usageMetadata"`
}

type frameCandidate struct {
	Content      *frameContent `json:"content"`
	FinishReason string        `json:"finishReason"`
}

type frameContent struct {
	Parts []responsePart `json:"parts"`
	Role  string         `json:"role"`
}

type responsePart struct {
	Text                  string                 `json:"text"`
	Thought               bool                   `json:"thought"`
	ThoughtSignature      string                 `json:"thoughtSignature"`
	ThoughtSignatureSnake string                 `json:"thought_signature"`
	InlineData            *frameInlineData       `json:"inlineData"`
	FunctionCall          *frameFunctionCall     `json:"functionCall"`
	FunctionResponse      *frameFunctionResponse `json:"functionResponse"`
	ExtraContent          *frameExtraContent     `json:"extra_content"`
}

type frameInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type frameFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
	ID   string          `json:"id"`
}

type frameFunctionResponse struct {
	Name string `json:"name"`
	ID   string `json:"id"`
}

type frameExtraContent struct {
	Google *struct {
		ThoughtSignature string `json:"thought_signature"`
	} `json:"google"`
}

type usageMetadata struct {
	PromptTokenCount        int64 `json:"promptTokenCount"`
	CandidatesTokenCount    int64 `json:"candidatesTokenCount"`
	CachedContentTokenCount int64 `json:"cachedContentTokenCount"`
	ThoughtsTokenCount      int64 `json:"thoughtsTokenCount"`
}

type streamDecoder struct {
	emit             func(canon.Event) error
	messageID        canon.ItemID
	seq              int
	messageOpen      bool
	pendingSig       string
	usage            canon.Usage
	sawFrame         bool
	sawTerminal      bool
	finishReason     string
	toolCallsStarted int
	finished         bool
}

func DecodeStream(r io.Reader, emit func(canon.Event) error) error {
	d := &streamDecoder{emit: emit, messageID: canon.ItemID("assistant-0")}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxSSEFrameBytes)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(line[len("data:"):])
		if payload == "" {
			continue
		}
		if err := d.frame([]byte(payload)); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return d.fail(canon.FailUpstreamTransport, "antigravity: stream read failed: "+err.Error())
	}
	return d.finish()
}

func (d *streamDecoder) fail(reason canon.FailureReason, message string) error {
	d.finished = true
	return d.emit(canon.TurnFailed{
		Failure: canon.Failure{Reason: reason, Message: message},
		Usage:   d.usage,
	})
}

func (d *streamDecoder) frame(payload []byte) error {
	var chunk streamFrame
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return d.fail(canon.FailOriginRejected, "antigravity: malformed upstream SSE data frame")
	}
	if chunk.Error != nil {
		return d.fail(canon.FailOriginRejected, "antigravity: upstream error: "+chunk.Error.Message)
	}
	if chunk.Response == nil {
		return nil
	}
	d.sawFrame = true
	root := chunk.Response
	if root.UsageMetadata != nil {
		d.usage.InputTokens = root.UsageMetadata.PromptTokenCount
		d.usage.OutputTokens = root.UsageMetadata.CandidatesTokenCount
		d.usage.CachedInputTokens = root.UsageMetadata.CachedContentTokenCount
		d.usage.ReasoningTokens = root.UsageMetadata.ThoughtsTokenCount
		d.usage.TotalTokens = root.UsageMetadata.PromptTokenCount + root.UsageMetadata.CandidatesTokenCount
		d.sawTerminal = true
	}
	if len(root.Candidates) == 0 {
		return nil
	}
	candidate := root.Candidates[0]
	if candidate.FinishReason != "" {
		d.finishReason = candidate.FinishReason
		d.sawTerminal = true
	}
	if candidate.Content == nil {
		return nil
	}
	for _, part := range candidate.Content.Parts {
		if err := d.part(part); err != nil {
			return err
		}
	}
	return nil
}

func (d *streamDecoder) part(part responsePart) error {
	sig := part.ThoughtSignature
	if sig == "" {
		sig = part.ThoughtSignatureSnake
	}
	if sig == "" && part.ExtraContent != nil && part.ExtraContent.Google != nil {
		sig = part.ExtraContent.Google.ThoughtSignature
	}
	if part.Thought && sig != "" && likelyRealSignature(sig) {
		d.pendingSig = sig
	}
	if part.Text != "" {
		if err := d.textDelta(part.Thought, part.Text); err != nil {
			return err
		}
	}
	if part.FunctionCall != nil {
		if part.FunctionCall.Name == "" {
			return d.fail(canon.FailToolArgsMalformed, "antigravity: functionCall part without a name")
		}
		if err := d.functionCall(part); err != nil {
			return err
		}
	}
	return nil
}

func (d *streamDecoder) textDelta(thought bool, text string) error {
	if !d.messageOpen {
		d.messageOpen = true
		if err := d.emit(canon.ItemStarted{Item: canon.Message{ID: d.messageID, Role: canon.RoleAssistant}}); err != nil {
			return err
		}
	}
	if thought {
		return d.emit(canon.ReasoningDelta{ItemID: d.messageID, Text: text})
	}
	return d.emit(canon.TextDelta{ItemID: d.messageID, Text: text})
}

func (d *streamDecoder) functionCall(part responsePart) error {
	d.seq++
	id := canon.ItemID(fmt.Sprintf("call-%d", d.seq))
	callID := canon.CallID(part.FunctionCall.ID)
	if callID == "" {
		callID = canon.CallID(id)
	}
	args := part.FunctionCall.Args
	if len(args) == 0 {
		args = []byte("{}")
	}
	state := canon.OpaqueRef{}
	sig := part.ThoughtSignature
	if sig == "" {
		sig = part.ThoughtSignatureSnake
	}
	if sig == "" {
		sig = d.pendingSig
	}
	if likelyRealSignature(sig) {
		state = canon.OpaqueRef{Store: signatureStore, Key: sig}
	}
	d.pendingSig = ""
	d.toolCallsStarted++
	if err := d.emit(canon.ItemStarted{Item: canon.FunctionCall{
		ID:        id,
		CallID:    callID,
		Name:      canon.ToolName(part.FunctionCall.Name),
		Arguments: args,
		State:     state,
	}}); err != nil {
		return err
	}
	if err := d.emit(canon.ToolArgumentsDelta{ItemID: id, Bytes: args}); err != nil {
		return err
	}
	return d.emit(canon.ItemFinished{Item: canon.FunctionCall{
		ID:        id,
		CallID:    callID,
		Name:      canon.ToolName(part.FunctionCall.Name),
		Arguments: args,
		State:     state,
	}})
}

func (d *streamDecoder) finish() error {
	if d.finished {
		return nil
	}
	d.finished = true
	if !d.sawFrame || !d.sawTerminal {
		return d.fail(canon.FailUpstreamTransport, "antigravity: upstream stream ended without a terminal signal")
	}
	truncated := d.finishReason == "MAX_TOKENS" || d.finishReason == "MALFORMED_FUNCTION_CALL"
	if truncated && (d.toolCallsStarted > 0 || d.finishReason == "MALFORMED_FUNCTION_CALL") {
		return d.fail(canon.FailUpstreamTransport,
			"antigravity: response truncated upstream before the turn completed ("+d.finishReason+")")
	}
	switch d.finishReason {
	case "MAX_TOKENS":
		return d.emit(canon.TurnFinished{Status: canon.Incomplete(canon.IncompleteMaxOutputTokens), Usage: d.usage})
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII":
		return d.emit(canon.TurnFinished{Status: canon.Incomplete(canon.IncompleteContentFilter), Usage: d.usage})
	default:
		return d.emit(canon.TurnFinished{Status: canon.Completed(), Usage: d.usage})
	}
}
