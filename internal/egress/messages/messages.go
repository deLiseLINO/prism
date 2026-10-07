package messages

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
	"github.com/deLiseLINO/prism/internal/reasonenv"
	"github.com/deLiseLINO/prism/internal/routing"
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
	ResponseFailure(canon.Event) (canon.Failure, bool)
	Flush() error
	Close()
}

func New(w io.Writer, stream bool) Egress {
	return &streamEncoder{w: w, streaming: stream, content: []any{}, blocks: map[canon.ItemID]*openBlock{}, stopPing: make(chan struct{})}
}

type blockKind uint8

const (
	blockText blockKind = iota + 1
	blockThinking
	blockToolUse
)

var (
	pingEvery     = 5 * time.Second
	pingIdleAfter = 20 * time.Second
)

type openBlock struct {
	index  int
	kind   blockKind
	text   strings.Builder
	args   strings.Builder
	custom bool
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

	id       string
	model    string
	content  []any
	usage    usageWire
	finished map[int]any

	writeMu   sync.Mutex
	lastWrite time.Time
	stopPing  chan struct{}
	pingDone  chan struct{}
	closeOnce sync.Once
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
	if err := e.write("message_start", messageStartWire{
		Type: "message_start",
		Message: messageWire{
			ID:      h.ID,
			Type:    "message",
			Role:    "assistant",
			Model:   string(h.Model),
			Content: []any{},
			Usage:   usageWire{},
		},
	}); err != nil {
		return err
	}
	e.pingDone = make(chan struct{})
	go e.pingLoop()
	return nil
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
		b.text.WriteString(tev.Text)
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
		b.text.WriteString(tev.Text)
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
		b.args.Write(tev.Bytes)
		return e.write("content_block_delta", blockDeltaWire{
			Type:  "content_block_delta",
			Index: b.index,
			Delta: inputJSONDeltaWire{Type: "input_json_delta", PartialJSON: string(tev.Bytes)},
		})
	case canon.CustomToolInputDelta:
		b, err := e.blockFor(tev.ItemID, "custom_tool_input_delta")
		if err != nil {
			return err
		}
		if !b.custom {
			return &FrameError{Reason: ReasonBlockMismatch, Event: "custom_tool_input_delta", ItemID: tev.ItemID}
		}
		b.text.WriteString(tev.Text)
		return nil
	case canon.ItemStateAvailable:
		return nil
	default:
		return &FrameError{Reason: ReasonUnsupportedItem, Event: eventName(ev)}
	}
}

func (e *streamEncoder) Lifecycle() routing.ResponseLifecycle { return e }

func (e *streamEncoder) CommitState() provider.CommitState { return e.commit }

func (e *streamEncoder) ResponseFailure(terminal canon.Event) (canon.Failure, bool) {
	switch t := terminal.(type) {
	case canon.TurnFailed:
		return t.Failure, true
	case canon.TurnFinished:
		if reason, incomplete := t.Status.Reason(); incomplete {
			unsafe := reason != canon.IncompleteMaxOutputTokens && reason != canon.IncompleteContentFilter
			for _, block := range e.blocks {
				unsafe = unsafe || block.kind != blockText
			}
			if unsafe {
				return canon.Failure{Reason: canon.FailUpstreamTransport, Message: "upstream response interrupted before completion"}, true
			}
		}
	}
	return canon.Failure{}, false
}

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
	e.flushed = true
	e.Close()
	if failure, failed := e.ResponseFailure(e.terminal); failed {
		if err := e.closeOpenBlocks(); err != nil {
			return err
		}
		wire := messagesErrorWire(failure)
		if !e.streaming {
			return e.writeJSON(wire)
		}
		return e.write("error", wire)
	}
	if err := e.closeOpenBlocks(); err != nil {
		return err
	}
	switch t := e.terminal.(type) {
	case canon.TurnFinished:
		e.usage = usageWire{
			// Canon input counts cached tokens; the Messages wire reports them
			// separately, and clients add cache_read back to input_tokens.
			InputTokens:              max(t.Usage.InputTokens-t.Usage.CachedInputTokens-t.Usage.CacheWriteInputTokens, 0),
			CacheReadInputTokens:     t.Usage.CachedInputTokens,
			CacheCreationInputTokens: t.Usage.CacheWriteInputTokens,
			OutputTokens:             t.Usage.OutputTokens,
		}
		if !e.streaming {
			stop := stopReason(t.Status, e.toolUse)
			for index := range e.next {
				if block, ok := e.finished[index]; ok {
					e.content = append(e.content, block)
				}
			}
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
		wire := messagesErrorWire(t.Failure)
		if !e.streaming {
			return e.writeJSON(wire)
		}
		return e.write("error", wire)
	}
	return &FrameError{Reason: ReasonNoTerminal, Event: "message_delta"}
}

// closeOpenBlocks ends the blocks a truncated turn left open, in index order,
// so the terminal stop or error that follows is preceded by a well-formed
// stream (or, without streaming, by a complete body).
func (e *streamEncoder) closeOpenBlocks() error {
	open := make([]*openBlock, 0, len(e.blocks))
	for _, b := range e.blocks {
		open = append(open, b)
	}
	sort.Slice(open, func(i, j int) bool { return open[i].index < open[j].index })
	clear(e.blocks)
	for _, b := range open {
		if !e.streaming {
			if e.finished == nil {
				e.finished = make(map[int]any)
			}
			e.finished[b.index] = partialBlockWire(b)
			continue
		}
		if err := e.write("content_block_stop", blockStopWire{Type: "content_block_stop", Index: b.index}); err != nil {
			return err
		}
	}
	return nil
}

func partialBlockWire(b *openBlock) any {
	switch b.kind {
	case blockThinking:
		return thinkingBlockWire{Type: "thinking", Thinking: b.text.String()}
	case blockToolUse:
		return toolUseBlockWire{Type: "tool_use", Input: json.RawMessage(`{}`)}
	}
	return textBlockWire{Type: "text", Text: b.text.String()}
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
		if env, ok := reasonenv.Decode(it.Signature); ok && len(env.Red) > 0 {
			id, kind, start = it.ID, blockThinking, redactedBlockWire{Type: "redacted_thinking", Data: env.Red[0]}
			break
		}
		id, kind, start = it.ID, blockThinking, thinkingBlockWire{Type: "thinking", Thinking: "", Signature: ""}
	case canon.FunctionCall:
		id, kind, start = it.ID, blockToolUse, toolUseBlockWire{
			Type:  "tool_use",
			ID:    string(it.CallID),
			Name:  string(it.Name),
			Input: json.RawMessage(`{}`),
		}
	case canon.CustomToolCall:
		if err := e.itemStarted(canon.FunctionCall{ID: it.ID, CallID: it.CallID, Name: it.Name}); err != nil {
			return err
		}
		e.blocks[it.ID].custom = true
		return nil
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
	if call, ok := item.(canon.CustomToolCall); ok {
		if !b.custom || !strings.HasPrefix(call.Input, b.text.String()) {
			return fmt.Errorf("messages egress: custom tool completion conflicts with deltas")
		}
		args, err := json.Marshal(struct {
			Input string `json:"input"`
		}{Input: call.Input})
		if err != nil {
			return err
		}
		item = canon.FunctionCall{ID: call.ID, CallID: call.CallID, Name: call.Name, Arguments: args}
	}
	if call, ok := item.(canon.FunctionCall); ok && len(call.Arguments) > 0 && !json.Valid(call.Arguments) {
		return fmt.Errorf("messages egress: malformed final tool arguments")
	}
	if !e.streaming {
		if e.finished == nil {
			e.finished = make(map[int]any)
		}
		e.finished[b.index] = finishedBlockWire(item)
	}
	if err := e.reconcileFinalContent(b, item); err != nil {
		return err
	}
	if b.kind == blockThinking {
		if r, isReasoning := item.(canon.ReasoningItem); isReasoning && r.Signature != "" {
			if env, isEnv := reasonenv.Decode(r.Signature); !isEnv || len(env.Red) == 0 {
				if err := e.write("content_block_delta", blockDeltaWire{
					Type:  "content_block_delta",
					Index: b.index,
					Delta: signatureDeltaWire{Type: "signature_delta", Signature: r.Signature},
				}); err != nil {
					return err
				}
			}
		}
	}
	delete(e.blocks, id)
	return e.write("content_block_stop", blockStopWire{Type: "content_block_stop", Index: b.index})
}

func (e *streamEncoder) reconcileFinalContent(b *openBlock, item canon.Item) error {
	if !e.streaming {
		return nil
	}
	var delta any
	switch it := item.(type) {
	case canon.Message:
		var sb strings.Builder
		for _, c := range it.Content {
			if t, ok := c.(canon.TextContent); ok {
				sb.WriteString(t.Text)
			}
		}
		if sb.Len() > 0 {
			final, seen := sb.String(), b.text.String()
			if !strings.HasPrefix(final, seen) {
				return fmt.Errorf("messages egress: final text conflicts with deltas")
			}
			if len(final) > len(seen) {
				delta = textDeltaWire{Type: "text_delta", Text: final[len(seen):]}
			}
		}
	case canon.ReasoningItem:
		if _, redacted := redactedData(it); !redacted && it.Content != "" {
			final, seen := it.Content, b.text.String()
			if !strings.HasPrefix(final, seen) {
				return fmt.Errorf("messages egress: final reasoning conflicts with deltas")
			}
			if len(final) > len(seen) {
				delta = thinkingDeltaWire{Type: "thinking_delta", Thinking: final[len(seen):]}
			}
		}
	case canon.FunctionCall:
		seen := b.args.String()
		final := string(it.Arguments)
		if !strings.HasPrefix(final, seen) {
			return fmt.Errorf("messages egress: final arguments conflict with deltas")
		}
		if len(final) > len(seen) {
			delta = inputJSONDeltaWire{Type: "input_json_delta", PartialJSON: final[len(seen):]}
		}
	}
	if delta == nil {
		return nil
	}
	return e.write("content_block_delta", blockDeltaWire{Type: "content_block_delta", Index: b.index, Delta: delta})
}

func redactedData(r canon.ReasoningItem) (string, bool) {
	if env, ok := reasonenv.Decode(r.Signature); ok && len(env.Red) > 0 {
		return env.Red[0], true
	}
	return "", false
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
		if env, ok := reasonenv.Decode(it.Signature); ok && len(env.Red) > 0 {
			return redactedBlockWire{Type: "redacted_thinking", Data: env.Red[0]}
		}
		return thinkingBlockWire{Type: "thinking", Thinking: it.Content, Signature: it.Signature}
	case canon.FunctionCall:
		input := json.RawMessage(`{}`)
		if json.Valid(it.Arguments) {
			input = json.RawMessage(it.Arguments)
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
	e.writeMu.Lock()
	_, werr := fmt.Fprintf(e.w, "event: %s\ndata: %s\n\n", name, data)
	if werr == nil {
		if f, ok := e.w.(http.Flusher); ok {
			f.Flush()
		}
		e.lastWrite = time.Now()
	}
	e.writeMu.Unlock()
	if werr != nil {
		return fmt.Errorf("messages egress: write %s: %w", name, werr)
	}
	return nil
}

func (e *streamEncoder) writeFrame(line string) {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	if _, err := fmt.Fprint(e.w, line); err != nil {
		return
	}
	if f, ok := e.w.(http.Flusher); ok {
		f.Flush()
	}
	e.lastWrite = time.Now()
}

func (e *streamEncoder) Close() {
	e.closeOnce.Do(func() { close(e.stopPing) })
	if e.pingDone != nil {
		<-e.pingDone
	}
}

// pingLoop keeps the client wire warm during silent upstream phases so a
// client idle timeout can never fire while the daemon is alive. The first
// ping waits until a real frame has been written, so a pre-stream failure
// is delivered without noise.
func (e *streamEncoder) pingLoop() {
	defer close(e.pingDone)
	ticker := time.NewTicker(pingEvery)
	defer ticker.Stop()
	for {
		select {
		case <-e.stopPing:
			return
		case <-ticker.C:
			e.writeMu.Lock()
			empty := e.lastWrite.IsZero()
			idle := time.Since(e.lastWrite) >= pingIdleAfter
			e.writeMu.Unlock()
			if empty || !idle {
				continue
			}
			e.writeFrame("event: ping\ndata: {\"type\": \"ping\"}\n\n")
		}
	}
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

func messagesErrorWire(f canon.Failure) any {
	if !f.HasProvider() || len(f.Provider.Error) == 0 {
		return errorWire{Type: "error", Error: errorBody{Type: failureErrorType(f.Reason), Message: f.Message}}
	}
	raw := bytes.TrimSpace(f.Provider.Error)
	if len(raw) > 0 && raw[0] == '{' {
		return map[string]any{"type": "error", "error": json.RawMessage(raw)}
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return errorWire{Type: "error", Error: errorBody{Message: s}}
	}
	return errorWire{Type: "error", Error: errorBody{Message: f.Message}}
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
	Type    string `json:"type,omitempty"`
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

type redactedBlockWire struct {
	Type string `json:"type"`
	Data string `json:"data"`
}

type toolUseBlockWire struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
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
