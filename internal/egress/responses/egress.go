package responses

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"prism/internal/canon"
	"prism/internal/egress"
	"prism/internal/execution"
	"prism/internal/provider"
	"prism/internal/routing"
)

var _ egress.Egress = (*Egress)(nil)

const (
	silenceThreshold = 2 * time.Second
	pollInterval     = 100 * time.Millisecond
)

type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

type lifecycle struct{ e *Egress }

func (l *lifecycle) CommitState() provider.CommitState {
	l.e.mu.Lock()
	defer l.e.mu.Unlock()
	return l.e.commitState
}

type terminalFrame struct {
	name  string
	data  map[string]any
	event canon.Event
}

type openItem struct {
	index    int
	kind     string
	text     string
	partOpen bool
}

type Egress struct {
	mu      sync.Mutex
	w       io.Writer
	flusher http.Flusher
	clock   Clock
	client  execution.Client
	self    *lifecycle

	header      egress.ResponseHeader
	begun       bool
	terminal    *terminalFrame
	flushed     bool
	writeErr    error
	commitState provider.CommitState
	seq         int64
	lastWrite   time.Time

	items   map[canon.ItemID]*openItem
	nextOut int
	output  []any

	done      chan struct{}
	closeOnce sync.Once
}

func New(w io.Writer, f execution.Facts) *Egress {
	return NewWithClock(w, f, realClock{})
}

func NewWithClock(w io.Writer, f execution.Facts, c Clock) *Egress {
	e := &Egress{
		w:         w,
		clock:     c,
		client:    f.Client,
		self:      &lifecycle{},
		items:     make(map[canon.ItemID]*openItem),
		lastWrite: c.Now(),
		done:      make(chan struct{}),
	}
	e.self.e = e
	if fw, ok := w.(http.Flusher); ok {
		e.flusher = fw
	}
	go e.livenessLoop()
	return e
}

func (e *Egress) Lifecycle() routing.ResponseLifecycle {
	return e.self
}

func (e *Egress) Close() {
	e.closeOnce.Do(func() { close(e.done) })
}

func (e *Egress) livenessLoop() {
	timer := e.clock.After(pollInterval)
	for {
		select {
		case <-e.done:
			return
		case <-timer:
			e.mu.Lock()
			active := e.begun && e.terminal == nil && !e.flushed
			idle := e.clock.Now().Sub(e.lastWrite)
			e.mu.Unlock()
			if active && idle >= silenceThreshold {
				e.sendHeartbeat()
			}
			timer = e.clock.After(pollInterval)
		}
	}
}

func (e *Egress) sendHeartbeat() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.writeErr != nil {
		return
	}
	if e.client == execution.ClientGrok {
		_ = e.writeRawLocked(": keep-alive\n\n")
		return
	}
	_ = e.writeEventLocked("response.heartbeat", map[string]any{})
}

func (e *Egress) Begin(h egress.ResponseHeader) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.writeErr != nil {
		return e.writeErr
	}
	if e.flushed {
		return errors.New("egress/responses: begin after flush")
	}
	if e.begun {
		return errors.New("egress/responses: begin called twice")
	}
	e.header = h
	e.begun = true
	e.commitState = provider.ResponseStarted
	created := map[string]any{"response": e.snapshotLocked("in_progress", nil, nil)}
	if err := e.writeEventLocked("response.created", created); err != nil {
		return err
	}
	inProgress := map[string]any{"response": e.snapshotLocked("in_progress", nil, nil)}
	return e.writeEventLocked("response.in_progress", inProgress)
}

func (e *Egress) Frame(ev canon.Event) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.writeErr != nil {
		return e.writeErr
	}
	if !e.begun {
		return errors.New("egress/responses: frame before begin")
	}
	if e.flushed {
		return errors.New("egress/responses: frame after flush")
	}
	switch t := ev.(type) {
	case canon.TurnFinished:
		return e.turnFinishedLocked(t)
	case canon.TurnFailed:
		return e.turnFailedLocked(t)
	case canon.ItemStarted:
		return e.itemStartedLocked(t)
	case canon.TextDelta:
		return e.textDeltaLocked(t)
	case canon.ReasoningDelta:
		return e.reasoningDeltaLocked(t)
	case canon.ToolArgumentsDelta:
		return e.toolArgumentsDeltaLocked(t)
	case canon.CustomToolInputDelta:
		return e.customToolInputDeltaLocked(t)
	case canon.ItemFinished:
		return e.itemFinishedLocked(t)
	case canon.ItemStateAvailable:
		return nil
	default:
		return fmt.Errorf("egress/responses: unsupported event %T", ev)
	}
}

func (e *Egress) Flush() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.writeErr != nil {
		return e.writeErr
	}
	if e.flushed {
		return errors.New("egress/responses: flush called twice")
	}
	if e.terminal == nil {
		return errors.New("egress/responses: flush without terminal")
	}
	if err := e.writeEventLocked(e.terminal.name, e.terminal.data); err != nil {
		return err
	}
	if err := e.writeRawLocked("data: [DONE]\n\n"); err != nil {
		return err
	}
	if e.flusher != nil {
		e.flusher.Flush()
	}
	e.flushed = true
	e.Close()
	return nil
}

func (e *Egress) turnFinishedLocked(t canon.TurnFinished) error {
	if e.terminal != nil {
		return e.duplicateTerminalLocked(t)
	}
	status := "completed"
	name := "response.completed"
	var extra map[string]any
	if t.Status.Kind() == canon.StatusIncomplete {
		status = "incomplete"
		name = "response.incomplete"
		reason, _ := t.Status.Reason()
		extra = map[string]any{"incomplete_details": map[string]any{"reason": incompleteReasonWire(reason)}}
	}
	data := map[string]any{"response": e.snapshotLocked(status, e.output, usageWire(t.Usage))}
	for k, v := range extra {
		data["response"].(map[string]any)[k] = v
	}
	e.terminal = &terminalFrame{name: name, data: data, event: t}
	return nil
}

func (e *Egress) turnFailedLocked(t canon.TurnFailed) error {
	if e.terminal != nil {
		return e.duplicateTerminalLocked(t)
	}
	snap := e.snapshotLocked("failed", e.output, usageWire(t.Usage))
	snap["error"] = map[string]any{
		"code":    failureReasonWire(t.Failure.Reason),
		"message": t.Failure.Message,
	}
	e.terminal = &terminalFrame{name: "response.failed", data: map[string]any{"response": snap}, event: t}
	return nil
}

func (e *Egress) duplicateTerminalLocked(ev canon.Event) error {
	if e.terminal.event == ev {
		return nil
	}
	return errors.New("egress/responses: conflicting terminal event")
}

func (e *Egress) itemStartedLocked(t canon.ItemStarted) error {
	id, kind, err := itemIdentity(t.Item)
	if err != nil {
		return err
	}
	if _, ok := e.items[id]; ok {
		return fmt.Errorf("egress/responses: duplicate item start %q", id)
	}
	it := &openItem{index: e.nextOut, kind: kind}
	e.items[id] = it
	e.nextOut++
	e.commitOutputLocked()
	wire, err := openItemWire(t.Item, kind, "in_progress")
	if err != nil {
		return err
	}
	return e.writeEventLocked("response.output_item.added", map[string]any{
		"output_index": it.index,
		"item":         wire,
	})
}

func (e *Egress) textDeltaLocked(t canon.TextDelta) error {
	it, err := e.openItemLocked(t.ItemID, "message")
	if err != nil {
		return err
	}
	if !it.partOpen {
		if err := e.writeEventLocked("response.content_part.added", map[string]any{
			"item_id":       t.ItemID,
			"output_index":  it.index,
			"content_index": 0,
			"part":          map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
		}); err != nil {
			return err
		}
		it.partOpen = true
	}
	it.text += t.Text
	e.commitOutputLocked()
	return e.writeEventLocked("response.output_text.delta", map[string]any{
		"item_id":       t.ItemID,
		"output_index":  it.index,
		"content_index": 0,
		"delta":         t.Text,
	})
}

func (e *Egress) reasoningDeltaLocked(t canon.ReasoningDelta) error {
	it, err := e.openItemLocked(t.ItemID, "reasoning")
	if err != nil {
		return err
	}
	it.text += t.Text
	e.commitOutputLocked()
	return e.writeEventLocked("response.reasoning_text.delta", map[string]any{
		"item_id":      t.ItemID,
		"output_index": it.index,
		"delta":        t.Text,
	})
}

func (e *Egress) toolArgumentsDeltaLocked(t canon.ToolArgumentsDelta) error {
	it, err := e.openItemLocked(t.ItemID, "function_call")
	if err != nil {
		return err
	}
	it.text += string(t.Bytes)
	e.commitOutputLocked()
	return e.writeEventLocked("response.function_call_arguments.delta", map[string]any{
		"item_id":      t.ItemID,
		"output_index": it.index,
		"delta":        string(t.Bytes),
	})
}

func (e *Egress) customToolInputDeltaLocked(t canon.CustomToolInputDelta) error {
	it, err := e.openItemLocked(t.ItemID, "custom_tool_call")
	if err != nil {
		return err
	}
	it.text += t.Text
	e.commitOutputLocked()
	return e.writeEventLocked("response.custom_tool_call_input.delta", map[string]any{
		"item_id":      t.ItemID,
		"output_index": it.index,
		"delta":        t.Text,
	})
}

func (e *Egress) itemFinishedLocked(t canon.ItemFinished) error {
	id, kind, err := itemIdentity(t.Item)
	if err != nil {
		return err
	}
	it, ok := e.items[id]
	if !ok {
		return fmt.Errorf("egress/responses: finish for unknown item %q", id)
	}
	if it.kind != kind {
		return fmt.Errorf("egress/responses: item %q kind mismatch", id)
	}
	switch kind {
	case "message":
		if it.partOpen {
			if err := e.writeEventLocked("response.output_text.done", map[string]any{
				"item_id":       id,
				"output_index":  it.index,
				"content_index": 0,
				"text":          it.text,
			}); err != nil {
				return err
			}
			if err := e.writeEventLocked("response.content_part.done", map[string]any{
				"item_id":       id,
				"output_index":  it.index,
				"content_index": 0,
				"part":          map[string]any{"type": "output_text", "text": it.text, "annotations": []any{}},
			}); err != nil {
				return err
			}
		}
	case "reasoning":
		if it.text != "" {
			if err := e.writeEventLocked("response.reasoning_text.done", map[string]any{
				"item_id":      id,
				"output_index": it.index,
				"text":         it.text,
			}); err != nil {
				return err
			}
		}
	case "function_call":
		if err := e.writeEventLocked("response.function_call_arguments.done", map[string]any{
			"item_id":      id,
			"output_index": it.index,
			"arguments":    it.text,
		}); err != nil {
			return err
		}
	case "custom_tool_call":
		if err := e.writeEventLocked("response.custom_tool_call_input.done", map[string]any{
			"item_id":      id,
			"output_index": it.index,
			"input":        it.text,
		}); err != nil {
			return err
		}
	}
	wire, err := openItemWire(t.Item, kind, "completed")
	if err != nil {
		return err
	}
	if err := e.writeEventLocked("response.output_item.done", map[string]any{
		"output_index": it.index,
		"item":         wire,
	}); err != nil {
		return err
	}
	e.output = append(e.output, wire)
	delete(e.items, id)
	return nil
}

func (e *Egress) openItemLocked(id canon.ItemID, kind string) (*openItem, error) {
	it, ok := e.items[id]
	if !ok {
		return nil, fmt.Errorf("egress/responses: delta for unknown item %q", id)
	}
	if it.kind != kind {
		return nil, fmt.Errorf("egress/responses: item %q kind mismatch", id)
	}
	return it, nil
}

func (e *Egress) commitOutputLocked() {
	if e.commitState < provider.OutputCommitted {
		e.commitState = provider.OutputCommitted
	}
}

func (e *Egress) snapshotLocked(status string, output []any, usage any) map[string]any {
	if output == nil {
		output = []any{}
	}
	return map[string]any{
		"id":         e.header.ID,
		"object":     "response",
		"created_at": e.header.CreatedAt.Unix(),
		"status":     status,
		"model":      string(e.header.Model),
		"output":     output,
		"usage":      usage,
	}
}

func (e *Egress) writeEventLocked(name string, data map[string]any) error {
	data["type"] = name
	data["sequence_number"] = e.seq
	e.seq++
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("egress/responses: marshal %s: %w", name, err)
	}
	return e.writeRawLocked("event: " + name + "\ndata: " + string(raw) + "\n\n")
}

func (e *Egress) writeRawLocked(s string) error {
	if _, err := io.WriteString(e.w, s); err != nil {
		e.writeErr = fmt.Errorf("egress/responses: write: %w", err)
		return e.writeErr
	}
	e.lastWrite = e.clock.Now()
	return nil
}

func itemIdentity(item canon.Item) (canon.ItemID, string, error) {
	switch t := item.(type) {
	case canon.Message:
		return t.ID, "message", nil
	case canon.ReasoningItem:
		return t.ID, "reasoning", nil
	case canon.FunctionCall:
		return t.ID, "function_call", nil
	case canon.CustomToolCall:
		return t.ID, "custom_tool_call", nil
	case canon.LocalShellCall:
		return t.ID, "local_shell_call", nil
	default:
		return "", "", fmt.Errorf("egress/responses: unsupported output item %T", item)
	}
}

func openItemWire(item canon.Item, kind, status string) (map[string]any, error) {
	wire := map[string]any{"type": kind, "status": status}
	switch t := item.(type) {
	case canon.Message:
		wire["id"] = t.ID
		wire["role"] = roleWire(t.Role)
		parts := []any{}
		for _, c := range t.Content {
			text, ok := c.(canon.TextContent)
			if !ok {
				return nil, fmt.Errorf("egress/responses: unsupported message content %T", c)
			}
			parts = append(parts, map[string]any{"type": "output_text", "text": text.Text, "annotations": []any{}})
		}
		wire["content"] = parts
	case canon.ReasoningItem:
		wire["id"] = t.ID
		summary := []any{}
		for _, s := range t.Summary {
			summary = append(summary, map[string]any{"type": "summary_text", "text": s.Text})
		}
		wire["summary"] = summary
		if t.Content != "" {
			wire["content"] = []any{map[string]any{"type": "reasoning_text", "text": t.Content}}
		}
	case canon.FunctionCall:
		wire["id"] = t.ID
		wire["call_id"] = t.CallID
		wire["name"] = t.Name
		wire["arguments"] = string(t.Arguments)
	case canon.CustomToolCall:
		wire["id"] = t.ID
		wire["call_id"] = t.CallID
		wire["name"] = t.Name
		wire["input"] = t.Input
	case canon.LocalShellCall:
		wire["id"] = t.ID
		wire["call_id"] = t.CallID
		wire["action"] = map[string]any{"type": "exec", "command": []string{t.Command}}
	default:
		return nil, fmt.Errorf("egress/responses: unsupported output item %T", item)
	}
	return wire, nil
}

func usageWire(u canon.Usage) map[string]any {
	return map[string]any{
		"input_tokens":          u.InputTokens,
		"input_tokens_details":  map[string]any{"cached_tokens": u.CachedInputTokens},
		"output_tokens":         u.OutputTokens,
		"output_tokens_details": map[string]any{"reasoning_tokens": u.ReasoningTokens},
		"total_tokens":          u.TotalTokens,
	}
}

func roleWire(r canon.Role) string {
	switch r {
	case canon.RoleUser:
		return "user"
	case canon.RoleSystem:
		return "system"
	default:
		return "assistant"
	}
}

func incompleteReasonWire(r canon.IncompleteReason) string {
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

func failureReasonWire(r canon.FailureReason) string {
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
		return "context_length_exceeded"
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
