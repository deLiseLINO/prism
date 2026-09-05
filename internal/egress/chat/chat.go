package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"prism/internal/canon"
	"prism/internal/provider"
	"prism/internal/routing"
)

const (
	chunkObject      = "chat.completion.chunk"
	completionObject = "chat.completion"
	functionType     = "function"
	assistantRole    = "assistant"
)

const (
	WarnCustomToolOmitted    = "custom_tool_unrepresentable"
	WarnItemUnrepresentable  = "item_unrepresentable"
	WarnFinishUnmapped       = "finish_reason_unmapped"
	WarnEventUnrepresentable = "event_unrepresentable"
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

type Warning struct {
	Code   string
	Detail string
}

type toolState struct {
	index     int
	callID    string
	name      string
	argsSeen  bool
	argsFinal string
	finished  bool
}

type Chat struct {
	w      io.Writer
	stream bool

	begun    bool
	terminal bool
	flushed  bool

	id      string
	model   string
	created int64

	commitState provider.CommitState

	tools   map[canon.ItemID]*toolState
	order   []canon.ItemID
	content strings.Builder
	usage   canon.Usage

	warned map[string]bool
	warns  []Warning
}

var _ Egress = (*Chat)(nil)

func New(w io.Writer, stream bool) *Chat {
	return &Chat{
		w:      w,
		stream: stream,
		tools:  make(map[canon.ItemID]*toolState),
		warned: make(map[string]bool),
	}
}

func (c *Chat) Begin(h ResponseHeader) error {
	if c.begun {
		return errors.New("egress/chat: begin called twice")
	}
	if h.ID == "" {
		return errors.New("egress/chat: empty response id")
	}
	c.begun = true
	c.id = h.ID
	c.model = string(h.Model)
	c.created = h.CreatedAt.Unix()
	if c.commitState == provider.NotStarted {
		c.commitState = provider.ResponseStarted
	}
	if !c.stream {
		return nil
	}
	return c.writeChunk(c.deltaChunk(delta{Role: assistantRole}))
}

func (c *Chat) Frame(ev canon.Event) error {
	if !c.begun {
		return errors.New("egress/chat: frame before begin")
	}
	if c.terminal {
		return errors.New("egress/chat: frame after terminal")
	}
	switch e := ev.(type) {
	case canon.ItemStarted:
		return c.itemStarted(e.Item)
	case canon.TextDelta:
		c.content.WriteString(e.Text)
		c.commit()
		return c.writeChunk(c.deltaChunk(delta{Content: &e.Text}))
	case canon.ReasoningDelta:
		c.commit()
		return c.writeChunk(c.deltaChunk(delta{ReasoningContent: &e.Text}))
	case canon.ToolArgumentsDelta:
		return c.toolArguments(e)
	case canon.CustomToolInputDelta:
		c.warnOnce(WarnCustomToolOmitted, fmt.Sprintf("custom tool input for item %q has no chat wire shape; omitted", e.ItemID))
		c.commit()
		return nil
	case canon.ItemStateAvailable:
		return nil
	case canon.ItemFinished:
		return c.itemFinished(e.Item)
	case canon.TurnFinished:
		return c.turnFinished(e)
	case canon.TurnFailed:
		return c.turnFailed(e)
	default:
		c.warnOnce(WarnEventUnrepresentable, fmt.Sprintf("event %T has no chat wire shape", ev))
		return nil
	}
}

func (c *Chat) Lifecycle() routing.ResponseLifecycle {
	return c
}

func (c *Chat) CommitState() provider.CommitState {
	return c.commitState
}

func (c *Chat) Flush() error {
	if c.flushed {
		return nil
	}
	c.flushed = true
	if !c.stream {
		return nil
	}
	if !c.terminal {
		return errors.New("egress/chat: flush before terminal event")
	}
	_, err := io.WriteString(c.w, "data: [DONE]\n\n")
	return err
}

func (c *Chat) Warnings() []Warning {
	out := make([]Warning, len(c.warns))
	copy(out, c.warns)
	return out
}

func (c *Chat) commit() {
	if c.commitState < provider.OutputCommitted {
		c.commitState = provider.OutputCommitted
	}
}

func (c *Chat) itemStarted(item canon.Item) error {
	switch it := item.(type) {
	case canon.FunctionCall:
		if _, ok := c.tools[it.ID]; ok {
			return fmt.Errorf("egress/chat: duplicate tool call item %q", it.ID)
		}
		st := &toolState{index: len(c.tools), callID: string(it.CallID), name: string(it.Name)}
		c.tools[it.ID] = st
		c.order = append(c.order, it.ID)
		id := string(it.CallID)
		ftyp := functionType
		name := string(it.Name)
		c.commit()
		return c.writeChunk(c.deltaChunk(delta{ToolCalls: []toolCallDelta{{
			Index: st.index, ID: &id, Type: &ftyp, Function: &funcDelta{Name: &name},
		}}}))
	case canon.CustomToolCall:
		c.warnOnce(WarnCustomToolOmitted, fmt.Sprintf("custom tool call %q has no chat wire shape", it.ID))
		return nil
	default:
		return nil
	}
}

func (c *Chat) toolArguments(e canon.ToolArgumentsDelta) error {
	st, ok := c.tools[e.ItemID]
	if !ok {
		return fmt.Errorf("egress/chat: tool arguments for unknown item %q", e.ItemID)
	}
	st.argsSeen = true
	args := string(e.Bytes)
	c.commit()
	return c.writeChunk(c.deltaChunk(delta{ToolCalls: []toolCallDelta{{
		Index: st.index, Function: &funcDelta{Arguments: &args},
	}}}))
}

func (c *Chat) itemFinished(item canon.Item) error {
	switch it := item.(type) {
	case canon.FunctionCall:
		st, ok := c.tools[it.ID]
		if !ok {
			return fmt.Errorf("egress/chat: tool call finish for unknown item %q", it.ID)
		}
		if st.finished {
			return fmt.Errorf("egress/chat: tool call item %q finished twice", it.ID)
		}
		st.finished = true
		st.name = string(it.Name)
		st.argsFinal = string(it.Arguments)
		if !st.argsSeen && len(it.Arguments) > 0 {
			args := st.argsFinal
			c.commit()
			if err := c.writeChunk(c.deltaChunk(delta{ToolCalls: []toolCallDelta{{
				Index: st.index, Function: &funcDelta{Arguments: &args},
			}}})); err != nil {
				return err
			}
		}
		return nil
	case canon.ReasoningItem:
		return nil
	case canon.CustomToolCall:
		c.warnOnce(WarnCustomToolOmitted, fmt.Sprintf("custom tool call %q has no chat wire shape", it.ID))
		return nil
	case canon.CustomToolOutput:
		c.warnOnce(WarnCustomToolOmitted, fmt.Sprintf("custom tool output for call %q has no chat wire shape", it.CallID))
		return nil
	case canon.LocalShellCall, canon.LocalShellOutput, canon.ToolSearchCall, canon.ToolSearchOutput, canon.CompactionMarker:
		c.warnOnce(WarnItemUnrepresentable, fmt.Sprintf("item %T has no chat wire shape", item))
		return nil
	default:
		return nil
	}
}

func (c *Chat) turnFinished(e canon.TurnFinished) error {
	c.usage = e.Usage
	fr, mapped := c.finishReason(e.Status)
	if !mapped {
		reason := "unknown"
		if r, ok := e.Status.Reason(); ok {
			reason = incompleteName(r)
		}
		c.warnOnce(WarnFinishUnmapped, fmt.Sprintf("terminal status %s has no chat finish_reason; mapped to %q", reason, fr))
	}
	if c.stream {
		u := usageWire(c.usage)
		if err := c.writeChunk(chunk{
			ID:      c.id,
			Object:  chunkObject,
			Created: c.created,
			Model:   c.model,
			Choices: []chunkChoice{{Index: 0, Delta: delta{}, FinishReason: &fr}},
			Usage:   &u,
		}); err != nil {
			return err
		}
		c.terminal = true
		return nil
	}
	c.terminal = true
	return c.writeJSON(completion{
		ID:      c.id,
		Object:  completionObject,
		Created: c.created,
		Model:   c.model,
		Choices: []choice{{
			Index:        0,
			Message:      c.messageWire(),
			FinishReason: fr,
		}},
		Usage: usageWire(c.usage),
	})
}

func (c *Chat) turnFailed(e canon.TurnFailed) error {
	c.terminal = true
	return c.writeJSON(errorEnvelope{Error: errorBody{
		Message: e.Failure.Message,
		Type:    errorType(e.Failure.Reason),
		Code:    failureCode(e.Failure.Reason),
	}})
}

func (c *Chat) messageWire() message {
	m := message{Role: assistantRole, Content: c.content.String()}
	for _, id := range c.order {
		st := c.tools[id]
		m.ToolCalls = append(m.ToolCalls, toolCallFull{
			ID:       st.callID,
			Type:     functionType,
			Function: funcFull{Name: st.name, Arguments: st.argsFinal},
		})
	}
	return m
}

func (c *Chat) finishReason(st canon.Status) (string, bool) {
	if st.Kind() == canon.StatusCompleted {
		if len(c.tools) > 0 {
			return "tool_calls", true
		}
		return "stop", true
	}
	if r, ok := st.Reason(); ok && r == canon.IncompleteMaxOutputTokens {
		return "length", true
	}
	return "stop", false
}

func (c *Chat) deltaChunk(d delta) chunk {
	return chunk{
		ID:      c.id,
		Object:  chunkObject,
		Created: c.created,
		Model:   c.model,
		Choices: []chunkChoice{{Index: 0, Delta: d}},
	}
}

func (c *Chat) writeChunk(ch chunk) error {
	if !c.stream {
		return nil
	}
	return c.writeJSON(ch)
}

func (c *Chat) writeJSON(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if !c.stream {
		_, err = c.w.Write(raw)
		return err
	}
	_, err = fmt.Fprintf(c.w, "data: %s\n\n", raw)
	return err
}

func (c *Chat) warnOnce(code, detail string) {
	if c.warned[code] {
		return
	}
	c.warned[code] = true
	c.warns = append(c.warns, Warning{Code: code, Detail: detail})
}

func usageWire(u canon.Usage) usage {
	return usage{
		PromptTokens:            u.InputTokens,
		CompletionTokens:        u.OutputTokens,
		TotalTokens:             u.TotalTokens,
		PromptTokensDetails:     promptTokenDetails{CachedTokens: u.CachedInputTokens},
		CompletionTokensDetails: completionTokenDetails{ReasoningTokens: u.ReasoningTokens},
	}
}

func incompleteName(r canon.IncompleteReason) string {
	switch r {
	case canon.IncompleteMaxOutputTokens:
		return "max_output_tokens"
	case canon.IncompleteContentFilter:
		return "content_filter"
	case canon.IncompleteUpstreamStall:
		return "upstream_stall"
	case canon.IncompleteAdapterEOF:
		return "adapter_eof"
	case canon.IncompleteClientDisconnected:
		return "client_disconnected"
	case canon.IncompleteBufferLimit:
		return "buffer_limit"
	default:
		return "unknown"
	}
}

func failureCode(r canon.FailureReason) string {
	switch r {
	case canon.FailUnauthorized:
		return "unauthorized"
	case canon.FailForbidden:
		return "forbidden"
	case canon.FailRateLimited:
		return "rate_limited"
	case canon.FailQuotaExhausted:
		return "quota_exhausted"
	case canon.FailServerOverloaded:
		return "server_overloaded"
	case canon.FailContextLength:
		return "context_length"
	case canon.FailInvalidRequest:
		return "invalid_request"
	case canon.FailOriginRejected:
		return "origin_rejected"
	case canon.FailCyberPolicy:
		return "cyber_policy"
	case canon.FailToolUndeclared:
		return "tool_undeclared"
	case canon.FailToolArgsMalformed:
		return "tool_args_malformed"
	case canon.FailUpstreamTransport:
		return "upstream_transport"
	case canon.FailNotFound:
		return "not_found"
	case canon.FailTimeout:
		return "timeout"
	default:
		return "unknown"
	}
}

func errorType(r canon.FailureReason) string {
	switch r {
	case canon.FailUnauthorized, canon.FailForbidden:
		return "authentication_error"
	case canon.FailRateLimited, canon.FailQuotaExhausted:
		return "rate_limit_error"
	case canon.FailInvalidRequest, canon.FailContextLength, canon.FailToolUndeclared, canon.FailToolArgsMalformed:
		return "invalid_request_error"
	default:
		return "api_error"
	}
}
