package messages

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"prism/internal/canon"
	"prism/internal/provider"
	"prism/internal/routing"
)

type ResponseHeader struct {
	ID        string
	Model     canon.ModelID
	CreatedAt time.Time
}

type Egress interface {
	Begin(h ResponseHeader) error
	Frame(ev canon.Event) error
	Lifecycle() routing.ResponseLifecycle
	Flush() error
}

func New(w io.Writer, stream bool) Egress {
	return &streamEncoder{w: w, streaming: stream, blocks: map[canon.ItemID]*openBlock{}}
}

type blockKind uint8

const (
	blockText blockKind = iota + 1
	blockThinking
	blockToolUse
)

type openBlock struct {
	index int
	kind  blockKind
}

type streamEncoder struct {
	w         io.Writer
	streaming bool
	begun     bool
	flushed   bool
	commit    provider.CommitState
	blocks    map[canon.ItemID]*openBlock
	next      int
	toolUse   bool
	terminal  canon.Event

	id      string
	model   string
	content []any
	usage   usageWire
}

func (e *streamEncoder) Begin(h ResponseHeader) error {
	if e.begun {
		return &FrameError{Reason: ReasonAlreadyBegun, Event: "message_start"}
	}
	e.begun = true
	e.id = h.ID
	e.model = string(h.Model)
	e.commit = provider.ResponseStarted
	if !e.streaming {
		return nil
	}
	return e.write("message_start", messageStartWire{
		Type: "message_start",
		Message: messageWire{
			ID:      h.ID,
			Type:    "message",
			Role:    "assistant",
			Model:   string(h.Model),
			Content: []any{},
			Usage:   usageWire{},
		},
	})
}

func (e *streamEncoder) Frame(ev canon.Event) error {
	if !e.begun {
		return &FrameError{Reason: ReasonNotBegun, Event: eventName(ev)}
	}
	if e.terminal != nil {
		return &FrameError{Reason: ReasonAfterTerminal, Event: eventName(ev)}
	}
	switch tev := ev.(type) {
	case canon.TurnFinished:
		e.terminal = tev
		return nil
	case canon.TurnFailed:
		e.terminal = tev
		return nil
	case canon.ItemStarted:
		return e.itemStarted(tev.Item)
	case canon.ItemFinished:
		return e.itemFinished(tev.Item)
	case canon.TextDelta:
		b, err := e.blockFor(tev.ItemID, "text_delta")
		if err != nil {
			return err
		}
		if b.kind != blockText {
			return &FrameError{Reason: ReasonBlockMismatch, Event: "text_delta", ItemID: tev.ItemID}
		}
		return e.write("content_block_delta", blockDeltaWire{
			Type:  "content_block_delta",
			Index: b.index,
			Delta: textDeltaWire{Type: "text_delta", Text: tev.Text},
		})
	case canon.ReasoningDelta:
		b, err := e.blockFor(tev.ItemID, "thinking_delta")
		if err != nil {
			return err
		}
		if b.kind != blockThinking {
			return &FrameError{Reason: ReasonBlockMismatch, Event: "thinking_delta", ItemID: tev.ItemID}
		}
		return e.write("content_block_delta", blockDeltaWire{
			Type:  "content_block_delta",
			Index: b.index,
			Delta: thinkingDeltaWire{Type: "thinking_delta", Thinking: tev.Text},
		})
	case canon.ToolArgumentsDelta:
		b, err := e.blockFor(tev.ItemID, "input_json_delta")
		if err != nil {
			return err
		}
		if b.kind != blockToolUse {
			return &FrameError{Reason: ReasonBlockMismatch, Event: "input_json_delta", ItemID: tev.ItemID}
		}
		return e.write("content_block_delta", blockDeltaWire{
			Type:  "content_block_delta",
			Index: b.index,
			Delta: inputJSONDeltaWire{Type: "input_json_delta", PartialJSON: string(tev.Bytes)},
		})
	case canon.CustomToolInputDelta:
		return &FrameError{Reason: ReasonUnsupportedItem, Event: "custom_tool_input_delta", ItemID: tev.ItemID}
	case canon.ItemStateAvailable:
		return nil
	default:
		return &FrameError{Reason: ReasonUnsupportedItem, Event: eventName(ev)}
	}
}

func (e *streamEncoder) Lifecycle() routing.ResponseLifecycle { return e }

func (e *streamEncoder) CommitState() provider.CommitState { return e.commit }

func (e *streamEncoder) Flush() error {
	if !e.begun {
		return &FrameError{Reason: ReasonNotBegun, Event: "message_delta"}
	}
	if e.flushed {
		return &FrameError{Reason: ReasonAlreadyFlushed, Event: "message_delta"}
	}
	if e.terminal == nil {
		return &FrameError{Reason: ReasonNoTerminal, Event: "message_delta"}
	}
	if len(e.blocks) > 0 {
		return &FrameError{Reason: ReasonOpenBlocks, Event: "message_delta"}
	}
	e.flushed = true
	switch t := e.terminal.(type) {
	case canon.TurnFinished:
		e.usage = usageWire{
			InputTokens:          t.Usage.InputTokens,
			CacheReadInputTokens: t.Usage.CachedInputTokens,
			OutputTokens:         t.Usage.OutputTokens,
		}
		if !e.streaming {
			stop := stopReason(t.Status, e.toolUse)
			return e.writeJSON(messageWire{
				ID:         e.id,
				Type:       "message",
				Role:       "assistant",
				Model:      e.model,
				Content:    e.content,
				StopReason: &stop,
				Usage:      e.usage,
			})
		}
		if err := e.write("message_delta", messageDeltaWire{
			Type: "message_delta",
			Delta: messageDeltaBody{
				StopReason: stopReason(t.Status, e.toolUse),
			},
			Usage: e.usage,
		}); err != nil {
			return err
		}
		return e.write("message_stop", terminalWire{Type: "message_stop"})
	case canon.TurnFailed:
		if !e.streaming {
			return e.writeJSON(errorWire{
				Type: "error",
				Error: errorBody{
					Type:    failureErrorType(t.Failure.Reason),
					Message: t.Failure.Message,
				},
			})
		}
		return e.write("error", errorWire{
			Type: "error",
			Error: errorBody{
				Type:    failureErrorType(t.Failure.Reason),
				Message: t.Failure.Message,
			},
		})
	}
	return &FrameError{Reason: ReasonNoTerminal, Event: "message_delta"}
}

func (e *streamEncoder) itemStarted(item canon.Item) error {
	var (
		id    canon.ItemID
		kind  blockKind
		start any
	)
	switch it := item.(type) {
	case canon.Message:
		id, kind, start = it.ID, blockText, textBlockWire{Type: "text", Text: ""}
	case canon.ReasoningItem:
		id, kind, start = it.ID, blockThinking, thinkingBlockWire{Type: "thinking", Thinking: "", Signature: ""}
	case canon.FunctionCall:
		id, kind, start = it.ID, blockToolUse, toolUseBlockWire{
			Type:  "tool_use",
			ID:    string(it.CallID),
			Name:  string(it.Name),
			Input: map[string]any{},
		}
	default:
		return &FrameError{Reason: ReasonUnsupportedItem, Event: "content_block_start"}
	}
	index := e.next
	e.next++
	e.blocks[id] = &openBlock{index: index, kind: kind}
	if kind == blockToolUse {
		e.toolUse = true
	}
	e.commit = provider.OutputCommitted
	return e.write("content_block_start", blockStartWire{
		Type:         "content_block_start",
		Index:        index,
		ContentBlock: start,
	})
}

func (e *streamEncoder) itemFinished(item canon.Item) error {
	id := itemIDOf(item)
	b, ok := e.blocks[id]
	if !ok {
		return &FrameError{Reason: ReasonUnknownItem, Event: "content_block_stop", ItemID: id}
	}
	if !e.streaming {
		e.content = append(e.content, finishedBlockWire(item))
	}
	if b.kind == blockThinking {
		if r, isReasoning := item.(canon.ReasoningItem); isReasoning && r.Signature != "" {
			if err := e.write("content_block_delta", blockDeltaWire{
				Type:  "content_block_delta",
				Index: b.index,
				Delta: signatureDeltaWire{Type: "signature_delta", Signature: r.Signature},
			}); err != nil {
				return err
			}
		}
	}
	delete(e.blocks, id)
	return e.write("content_block_stop", blockStopWire{Type: "content_block_stop", Index: b.index})
}

func finishedBlockWire(item canon.Item) any {
	switch it := item.(type) {
	case canon.Message:
		var sb strings.Builder
		for _, c := range it.Content {
			if text, ok := c.(canon.TextContent); ok {
				sb.WriteString(text.Text)
			}
		}
		return textBlockWire{Type: "text", Text: sb.String()}
	case canon.ReasoningItem:
		return thinkingBlockWire{Type: "thinking", Thinking: it.Content, Signature: it.Signature}
	case canon.FunctionCall:
		input := map[string]any{}
		if len(it.Arguments) > 0 {
			if err := json.Unmarshal(it.Arguments, &input); err != nil {
				input = map[string]any{}
			}
		}
		return toolUseBlockWire{Type: "tool_use", ID: string(it.CallID), Name: string(it.Name), Input: input}
	}
	return textBlockWire{Type: "text", Text: ""}
}

func (e *streamEncoder) blockFor(id canon.ItemID, event string) (*openBlock, error) {
	b, ok := e.blocks[id]
	if !ok {
		return nil, &FrameError{Reason: ReasonUnknownItem, Event: event, ItemID: id}
	}
	return b, nil
}
func (e *streamEncoder) write(name string, payload any) error {
	if !e.streaming {
		return nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("messages egress: encode %s: %w", name, err)
	}
	if _, err := fmt.Fprintf(e.w, "event: %s\ndata: %s\n\n", name, data); err != nil {
		return fmt.Errorf("messages egress: write %s: %w", name, err)
	}
	return nil
}

func (e *streamEncoder) writeJSON(payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("messages egress: encode message: %w", err)
	}
	if _, err := fmt.Fprintf(e.w, "%s\n", data); err != nil {
		return fmt.Errorf("messages egress: write message: %w", err)
	}
	return nil
}

func stopReason(s canon.Status, toolUse bool) string {
	if s.Kind() == canon.StatusIncomplete {
		if reason, _ := s.Reason(); reason == canon.IncompleteMaxOutputTokens {
			return "max_tokens"
		}
		return "end_turn"
	}
	if toolUse {
		return "tool_use"
	}
	return "end_turn"
}

func failureErrorType(r canon.FailureReason) string {
	switch r {
	case canon.FailUnauthorized:
		return "authentication_error"
	case canon.FailForbidden, canon.FailCyberPolicy:
		return "permission_error"
	case canon.FailRateLimited:
		return "rate_limit_error"
	case canon.FailQuotaExhausted:
		return "billing_error"
	case canon.FailServerOverloaded:
		return "overloaded_error"
	case canon.FailNotFound:
		return "not_found_error"
	case canon.FailTimeout:
		return "timeout_error"
	case canon.FailContextLength, canon.FailInvalidRequest, canon.FailToolUndeclared, canon.FailToolArgsMalformed:
		return "invalid_request_error"
	case canon.FailOriginRejected, canon.FailUpstreamTransport, canon.FailUnknown:
		return "api_error"
	}
	return "api_error"
}

func itemIDOf(item canon.Item) canon.ItemID {
	switch it := item.(type) {
	case canon.Message:
		return it.ID
	case canon.ReasoningItem:
		return it.ID
	case canon.FunctionCall:
		return it.ID
	case canon.FunctionOutput:
		return it.ID
	case canon.CustomToolCall:
		return it.ID
	case canon.CustomToolOutput:
		return it.ID
	case canon.LocalShellCall:
		return it.ID
	case canon.LocalShellOutput:
		return it.ID
	case canon.ToolSearchCall:
		return it.ID
	case canon.ToolSearchOutput:
		return it.ID
	case canon.CompactionMarker:
		return it.ID
	}
	return ""
}

func eventName(ev canon.Event) string {
	return fmt.Sprintf("%T", ev)
}

type messageStartWire struct {
	Type    string      `json:"type"`
	Message messageWire `json:"message"`
}

type messageWire struct {
	ID           string    `json:"id"`
	Type         string    `json:"type"`
	Role         string    `json:"role"`
	Model        string    `json:"model"`
	Content      []any     `json:"content"`
	StopReason   *string   `json:"stop_reason"`
	StopSequence *int      `json:"stop_sequence"`
	Usage        usageWire `json:"usage"`
}

type blockStartWire struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock any    `json:"content_block"`
}

type blockDeltaWire struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Delta any    `json:"delta"`
}

type blockStopWire struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
}

type messageDeltaWire struct {
	Type  string           `json:"type"`
	Delta messageDeltaBody `json:"delta"`
	Usage usageWire        `json:"usage"`
}

type messageDeltaBody struct {
	StopReason   string `json:"stop_reason"`
	StopSequence *int   `json:"stop_sequence"`
}

type terminalWire struct {
	Type string `json:"type"`
}

type errorWire struct {
	Type  string    `json:"type"`
	Error errorBody `json:"error"`
}

type errorBody struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type usageWire struct {
	InputTokens              int64 `json:"input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
}

type textBlockWire struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type thinkingBlockWire struct {
	Type      string `json:"type"`
	Thinking  string `json:"thinking"`
	Signature string `json:"signature"`
}

type toolUseBlockWire struct {
	Type  string         `json:"type"`
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

type textDeltaWire struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type thinkingDeltaWire struct {
	Type     string `json:"type"`
	Thinking string `json:"thinking"`
}

type signatureDeltaWire struct {
	Type      string `json:"type"`
	Signature string `json:"signature"`
}

type inputJSONDeltaWire struct {
	Type        string `json:"type"`
	PartialJSON string `json:"partial_json"`
}

type FrameReason uint8

const (
	ReasonNotBegun FrameReason = iota + 1
	ReasonAlreadyBegun
	ReasonAfterTerminal
	ReasonUnknownItem
	ReasonBlockMismatch
	ReasonUnsupportedItem
	ReasonOpenBlocks
	ReasonNoTerminal
	ReasonAlreadyFlushed
)

func (r FrameReason) String() string {
	switch r {
	case ReasonNotBegun:
		return "not_begun"
	case ReasonAlreadyBegun:
		return "already_begun"
	case ReasonAfterTerminal:
		return "after_terminal"
	case ReasonUnknownItem:
		return "unknown_item"
	case ReasonBlockMismatch:
		return "block_mismatch"
	case ReasonUnsupportedItem:
		return "unsupported_item"
	case ReasonOpenBlocks:
		return "open_blocks"
	case ReasonNoTerminal:
		return "no_terminal"
	case ReasonAlreadyFlushed:
		return "already_flushed"
	}
	return fmt.Sprintf("frame_reason(%d)", uint8(r))
}

type FrameError struct {
	Reason FrameReason
	Event  string
	ItemID canon.ItemID
	Err    error
}

func (e *FrameError) Error() string {
	msg := fmt.Sprintf("messages egress: %s", e.Reason)
	if e.Event != "" {
		msg += fmt.Sprintf(" (event %s)", e.Event)
	}
	if e.ItemID != "" {
		msg += fmt.Sprintf(" (item %s)", e.ItemID)
	}
	if e.Err != nil {
		return msg + ": " + e.Err.Error()
	}
	return msg
}

func (e *FrameError) Unwrap() error { return e.Err }
