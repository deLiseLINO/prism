package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"prism/internal/canon"
	"prism/internal/provider"
	"prism/internal/reasonenv"
)

type sseFrame struct {
	event string
	data  []byte
}

func scanSSE(body io.Reader, handle func(sseFrame) error) error {
	reader := bufio.NewReader(body)
	var event string
	var data strings.Builder
	flush := func() error {
		if data.Len() == 0 && event == "" {
			return nil
		}
		frame := sseFrame{event: event, data: []byte(data.String())}
		event = ""
		data.Reset()
		return handle(frame)
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				line = strings.TrimRight(line, "\r\n")
				if line != "" {
					applySSELine(line, &event, &data)
				}
				return flush()
			}
			return err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		applySSELine(line, &event, &data)
	}
}

func applySSELine(line string, event *string, data *strings.Builder) {
	if value, ok := strings.CutPrefix(line, "event:"); ok {
		*event = strings.TrimSpace(value)
		return
	}
	if value, ok := strings.CutPrefix(line, "data:"); ok {
		if data.Len() > 0 {
			data.WriteByte('\n')
		}
		data.WriteString(strings.TrimPrefix(value, " "))
	}
}

type openBlock struct {
	kind      string
	itemID    canon.ItemID
	callID    canon.CallID
	name      canon.ToolName
	text      strings.Builder
	signature string
	args      strings.Builder
}

type streamState struct {
	sink      provider.Sink
	store     *stateStore
	log       *slog.Logger
	blocks    map[string]*openBlock
	usage     wireUsage
	stopSeen  bool
	stopValue string
	terminal  bool
}

type wireUsage struct {
	input     *int64
	output    *int64
	cacheRead *int64
	cacheWrE  *int64
}

func (s *streamState) emit(ev canon.Event) error {
	return s.sink.Emit(ev)
}
func (u *wireUsage) merge(next wireUsage) {
	if next.input != nil {
		u.input = next.input
	}
	if next.output != nil {
		u.output = next.output
	}
	if next.cacheRead != nil {
		u.cacheRead = next.cacheRead
	}
	if next.cacheWrE != nil {
		u.cacheWrE = next.cacheWrE
	}
}

func (u *wireUsage) canonUsage() canon.Usage {
	var out canon.Usage
	var read, write int64
	if u.input != nil {
		out.InputTokens = *u.input
	}
	if u.output != nil {
		out.OutputTokens = *u.output
	}
	if u.cacheRead != nil {
		read = *u.cacheRead
	}
	if u.cacheWrE != nil {
		write = *u.cacheWrE
	}
	out.InputTokens += read + write
	out.CachedInputTokens = read
	out.TotalTokens = out.InputTokens + out.OutputTokens
	return out
}

func (r *Runner) stream(body io.Reader, sink provider.Sink) error {
	state := &streamState{
		sink:   sink,
		store:  r.state,
		log:    r.log,
		blocks: make(map[string]*openBlock),
	}
	err := scanSSE(body, state.handle)
	if err != nil {
		var re *provider.RunError
		if errors.As(err, &re) {
			return re
		}
		return &provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassTransport, Cause: err}
	}
	if !state.terminal {
		return &provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassTransport, Cause: errNoTerminal}
	}
	return nil
}

func (s *streamState) handle(frame sseFrame) error {
	payload, err := decodeJSON(frame.data)
	if err != nil {
		s.log.Warn("anthropic: dropped unparseable SSE frame", slog.String("error", err.Error()))
		return nil
	}
	eventType := frame.event
	if eventType == "" {
		eventType, _ = payload["type"].(string)
	}
	switch eventType {
	case "message_start":
		return s.messageStart(payload)
	case "content_block_start":
		return s.contentBlockStart(payload)
	case "content_block_delta":
		return s.contentBlockDelta(payload)
	case "content_block_stop":
		return s.contentBlockStop(payload)
	case "message_delta":
		return s.messageDelta(payload)
	case "message_stop":
		return s.messageStop()
	case "error":
		return s.upstreamErrorEvent(payload)
	default:
		s.log.Warn("anthropic: unknown SSE event", slog.String("event", eventType))
		return nil
	}
}

func (s *streamState) messageStart(payload map[string]any) error {
	message, _ := payload["message"].(map[string]any)
	if message == nil {
		return nil
	}
	s.usage.merge(usageFromMap(message["usage"]))
	return nil
}

func usageFromMap(value any) wireUsage {
	m, ok := value.(map[string]any)
	if !ok {
		return wireUsage{}
	}
	return wireUsage{
		input:     int64Field(m, "input_tokens"),
		output:    int64Field(m, "output_tokens"),
		cacheRead: int64Field(m, "cache_read_input_tokens"),
		cacheWrE:  int64Field(m, "cache_creation_input_tokens"),
	}
}

func int64Field(m map[string]any, key string) *int64 {
	if v, ok := m[key].(float64); ok {
		n := int64(v)
		return &n
	}
	return nil
}

func (s *streamState) blockKey(payload map[string]any) string {
	if index, ok := payload["index"].(float64); ok {
		return fmt.Sprintf("%d", int(index))
	}
	return ""
}

func (s *streamState) contentBlockStart(payload map[string]any) error {
	block, _ := payload["content_block"].(map[string]any)
	if block == nil {
		return nil
	}
	key := s.blockKey(payload)
	blockType, _ := block["type"].(string)
	open := &openBlock{kind: blockType}
	switch blockType {
	case "text":
		open.itemID = canon.ItemID(fmt.Sprintf("block-%s", key))
		s.blocks[key] = open
		return s.emit(canon.ItemStarted{Item: canon.Message{ID: open.itemID, Role: canon.RoleAssistant}})
	case "thinking":
		open.itemID = canon.ItemID(fmt.Sprintf("block-%s", key))
		s.blocks[key] = open
		return s.emit(canon.ItemStarted{Item: canon.ReasoningItem{ID: open.itemID}})
	case "redacted_thinking":
		open.itemID = canon.ItemID(fmt.Sprintf("block-%s", key))
		data, _ := block["data"].(string)
		open.signature = reasonenv.EncodeRedacted([]string{data})
		s.blocks[key] = open
		return s.emit(canon.ItemStarted{Item: canon.ReasoningItem{ID: open.itemID, Signature: open.signature}})
	case "tool_use":
		id, _ := block["id"].(string)
		name, _ := block["name"].(string)
		if strings.TrimSpace(id) == "" {
			id = fmt.Sprintf("toolu_block-%s", key)
		}
		open.itemID = canon.ItemID(id)
		open.callID = canon.CallID(id)
		open.name = canon.ToolName(name)
		s.blocks[key] = open
		return s.emit(canon.ItemStarted{Item: canon.FunctionCall{ID: open.itemID, CallID: open.callID, Name: open.name}})
	default:
		s.log.Warn("anthropic: unknown content block type", slog.String("type", blockType))
		return nil
	}
}

func (s *streamState) openBlockFor(payload map[string]any) *openBlock {
	return s.blocks[s.blockKey(payload)]
}

func (s *streamState) contentBlockDelta(payload map[string]any) error {
	delta, _ := payload["delta"].(map[string]any)
	if delta == nil {
		return nil
	}
	deltaType, _ := delta["type"].(string)
	open := s.openBlockFor(payload)
	switch deltaType {
	case "text_delta":
		text, _ := delta["text"].(string)
		if open == nil {
			return nil
		}
		open.text.WriteString(text)
		return s.emit(canon.TextDelta{ItemID: open.itemID, Text: text})
	case "thinking_delta":
		text, _ := delta["thinking"].(string)
		if open == nil {
			return nil
		}
		open.text.WriteString(text)
		return s.emit(canon.ReasoningDelta{ItemID: open.itemID, Text: text})
	case "signature_delta":
		signature, _ := delta["signature"].(string)
		if open == nil || open.kind != "thinking" {
			s.log.Warn("anthropic: signature_delta outside thinking block")
			return nil
		}
		open.signature = signature
		return nil
	case "input_json_delta":
		partial, _ := delta["partial_json"].(string)
		if open == nil || open.kind != "tool_use" {
			return nil
		}
		open.args.WriteString(partial)
		return s.emit(canon.ToolArgumentsDelta{ItemID: open.itemID, Bytes: []byte(partial)})
	default:
		s.log.Warn("anthropic: unknown delta type", slog.String("type", deltaType))
		return nil
	}
}

func (s *streamState) contentBlockStop(payload map[string]any) error {
	open := s.openBlockFor(payload)
	if open == nil {
		return nil
	}
	delete(s.blocks, s.blockKey(payload))
	switch open.kind {
	case "text":
		return s.emit(canon.ItemFinished{Item: canon.Message{
			ID:      open.itemID,
			Role:    canon.RoleAssistant,
			Content: []canon.Content{canon.TextContent{Text: open.text.String()}},
		}})
	case "thinking":
		signature := open.signature
		if signature == "" {
			signature = reasonenv.Encode(open.text.String())
		}
		s.store.put(string(open.itemID), []byte(signature))
		item := canon.ReasoningItem{
			ID:        open.itemID,
			Content:   open.text.String(),
			Signature: signature,
			State:     canon.OpaqueRef{Store: stateStoreName, Key: string(open.itemID)},
		}
		if err := s.emit(canon.ItemStateAvailable{ItemID: open.itemID, State: item.State}); err != nil {
			return err
		}
		return s.emit(canon.ItemFinished{Item: item})
	case "redacted_thinking":
		s.store.put(string(open.itemID), []byte(open.signature))
		return s.emit(canon.ItemFinished{Item: canon.ReasoningItem{
			ID:        open.itemID,
			Signature: open.signature,
			State:     canon.OpaqueRef{Store: stateStoreName, Key: string(open.itemID)},
		}})
	case "tool_use":
		args := open.args.String()
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		if !json.Valid([]byte(args)) {
			return &provider.RunError{
				Kind:  provider.TerminalOmitted,
				Class: provider.ClassServer,
				Cause: fmt.Errorf("anthropic: tool_use %q stream sent malformed arguments", open.callID),
			}
		}
		return s.emit(canon.ItemFinished{Item: canon.FunctionCall{
			ID:        open.itemID,
			CallID:    open.callID,
			Name:      open.name,
			Arguments: []byte(args),
		}})
	default:
		return nil
	}
}

func (s *streamState) messageDelta(payload map[string]any) error {
	s.usage.merge(usageFromMap(payload["usage"]))
	delta, _ := payload["delta"].(map[string]any)
	if delta == nil {
		return nil
	}
	stopReason, ok := delta["stop_reason"].(string)
	if !ok {
		return nil
	}
	if s.stopSeen {
		s.log.Warn("anthropic: duplicate stop_reason", slog.String("stop_reason", stopReason))
	}
	s.stopSeen = true
	s.stopValue = stopReason
	return nil
}

func (s *streamState) mapStopReason(reason string) canon.Status {
	switch reason {
	case "", "end_turn", "stop_sequence", "tool_use":
		return canon.Completed()
	case "max_tokens":
		return canon.Incomplete(canon.IncompleteMaxOutputTokens)
	case "refusal", "content_filter":
		return canon.Incomplete(canon.IncompleteContentFilter)
	default:
		s.log.Warn("anthropic: unknown stop_reason", slog.String("stop_reason", reason))
		return canon.Completed()
	}
}

func (s *streamState) messageStop() error {
	if s.terminal {
		return errors.New("anthropic: duplicate message_stop")
	}
	s.terminal = true
	return s.emit(canon.TurnFinished{Status: s.mapStopReason(s.stopValue), Usage: s.usage.canonUsage()})
}

func (s *streamState) upstreamErrorEvent(payload map[string]any) error {
	errObj, _ := payload["error"].(map[string]any)
	message, _ := errObj["message"].(string)
	if message == "" {
		message = "anthropic: upstream stream error"
	}
	kind := provider.TerminalOmitted
	class := provider.ClassServer
	if typ, _ := errObj["type"].(string); typ == "rate_limit_error" {
		kind = provider.Retryable
		class = provider.ClassRateLimited
	} else if typ == "overloaded_error" {
		kind = provider.Retryable
	}
	return &provider.RunError{Kind: kind, Class: class, Cause: errors.New(message)}
}

func (r *Runner) CountTokens(ctx context.Context, req provider.CountTokensRequest) (provider.TokenCount, error) {
	wr, err := r.buildWireRequest(canon.Request{
		Model:        req.Target.Model,
		Instructions: req.Instructions,
		Input:        req.Input,
		Tools:        req.Tools,
	}, false)
	if err != nil {
		return provider.TokenCount{}, buildFailure(err)
	}
	wr.MaxTokens = 0
	wr.Stream = false
	body, err := json.Marshal(wr)
	if err != nil {
		return provider.TokenCount{}, buildFailure(fmt.Errorf("anthropic: encode count_tokens request: %w", err))
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, countTokensURL(r.baseURL(req.Target)), bytes.NewReader(body))
	if err != nil {
		return provider.TokenCount{}, buildFailure(err)
	}
	for _, h := range r.buildHeaders("application/json", req.Target.APIKeyRef, r.opts.Beta) {
		httpReq.Header.Add(h.name, h.value)
	}
	resp, err := r.http.Do(httpReq)
	if err != nil {
		return provider.TokenCount{}, &provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassTransport, Cause: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return provider.TokenCount{}, upstreamError(resp)
	}
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	if err != nil {
		return provider.TokenCount{}, &provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassTransport, Cause: err}
	}
	parsed, err := decodeJSON(payload)
	if err != nil {
		return provider.TokenCount{}, &provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassServer, Cause: err}
	}
	tokens, ok := parsed["input_tokens"].(float64)
	if !ok {
		return provider.TokenCount{}, &provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassServer, Cause: errors.New("anthropic: count_tokens response missing input_tokens")}
	}
	return provider.TokenCount{InputTokens: int64(tokens)}, nil
}
