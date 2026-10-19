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
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
	"github.com/deLiseLINO/prism/internal/providers/usagewire"
	"github.com/deLiseLINO/prism/internal/reasonenv"
)

var errTerminalDone = errors.New("anthropic: terminal emitted")

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
				if data.Len() != 0 || event != "" {
					return errors.New("anthropic: truncated SSE event")
				}
				return nil
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
	custom    bool
}

type streamState struct {
	sink      provider.Sink
	custom    customTools
	store     *stateStore
	log       *slog.Logger
	msgID     string
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
func (u *wireUsage) merge(next wireUsage) error {
	merged := *u
	if next.input != nil {
		merged.input = next.input
	}
	if next.output != nil {
		merged.output = next.output
	}
	if next.cacheRead != nil {
		merged.cacheRead = next.cacheRead
	}
	if next.cacheWrE != nil {
		merged.cacheWrE = next.cacheWrE
	}
	var total int64
	for _, count := range []*int64{merged.input, merged.cacheRead, merged.cacheWrE, merged.output} {
		if count == nil {
			continue
		}
		if *count < 0 || *count > math.MaxInt64-total {
			return errors.New("token sum exceeds int64")
		}
		total += *count
	}
	*u = merged
	return nil
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
	out.CacheWriteInputTokens = write
	out.TotalTokens = out.InputTokens + out.OutputTokens
	return out
}

func (r *Runner) stream(body io.Reader, sink provider.Sink, custom customTools) error {
	state := &streamState{
		custom: custom,
		sink:   sink,
		store:  r.state,
		log:    r.log,
		blocks: make(map[string]*openBlock),
	}
	err := scanSSE(body, state.handle)
	if err != nil && !errors.Is(err, errTerminalDone) {
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
	if len(frame.data) == 0 {
		return nil
	}
	payload, err := decodeStreamJSON(frame.data)
	if err != nil {
		return s.protocolFailure(fmt.Errorf("anthropic: malformed SSE JSON: %w", err))
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
	if err := s.mergeUsage(message["usage"], false); err != nil {
		return err
	}
	if id, _ := message["id"].(string); id != "" {
		s.msgID = id
	}
	return nil
}

func decodeStreamJSON(data []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	if payload == nil {
		return nil, errors.New("expected JSON object")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, errors.New("trailing JSON data")
	}
	return payload, nil
}

func (s *streamState) protocolFailure(cause error) error {
	s.terminal = true
	if err := s.emit(canon.TurnFailed{Failure: canon.Failure{Reason: canon.FailUpstreamTransport, Message: cause.Error()}, Usage: s.usage.canonUsage()}); err != nil {
		return err
	}
	return &provider.RunError{Kind: provider.TerminalEmitted, Class: provider.ClassTransport, Accepted: true, Cause: cause}
}

func (s *streamState) mergeUsage(value any, delta bool) error {
	next, err := usageFromMap(value, delta)
	if err == nil {
		err = s.usage.merge(next)
	}
	if err != nil {
		return s.protocolFailure(fmt.Errorf("anthropic: invalid upstream usage: %w", err))
	}
	return nil
}

func usageFromMap(value any, delta bool) (wireUsage, error) {
	if value == nil {
		return wireUsage{}, nil
	}
	m, ok := value.(map[string]any)
	if !ok {
		return wireUsage{}, errors.New("usage must be an object or null")
	}
	var out wireUsage
	for _, field := range []struct {
		key      string
		count    **int64
		nullable bool
	}{
		{"input_tokens", &out.input, delta}, {"output_tokens", &out.output, false},
		{"cache_read_input_tokens", &out.cacheRead, true}, {"cache_creation_input_tokens", &out.cacheWrE, true},
	} {
		raw, present := m[field.key]
		if !present || raw == nil && field.nullable {
			continue
		}
		count, err := usagewire.Number(raw)
		if err != nil {
			return wireUsage{}, fmt.Errorf("%s: %w", field.key, err)
		}
		*field.count = &count
	}
	return out, nil
}

func (s *streamState) blockKey(payload map[string]any) string {
	if index, err := usagewire.Number(payload["index"]); err == nil {
		return strconv.FormatInt(index, 10)
	}
	return ""
}

var streamSeq atomic.Uint64

func (s *streamState) blockItemID(key string) canon.ItemID {
	if s.msgID == "" {
		s.msgID = fmt.Sprintf("stream-%d", streamSeq.Add(1))
	}
	return canon.ItemID(fmt.Sprintf("%s-block-%s", s.msgID, key))
}

func (s *streamState) contentBlockStart(payload map[string]any) error {
	block, _ := payload["content_block"].(map[string]any)
	if block == nil {
		return s.protocolFailure(errors.New("anthropic: content_block_start missing block"))
	}
	key := s.blockKey(payload)
	if key == "" || s.blocks[key] != nil {
		return s.protocolFailure(errors.New("anthropic: missing or duplicate block index"))
	}
	blockType, _ := block["type"].(string)
	open := &openBlock{kind: blockType}
	switch blockType {
	case "text":
		open.itemID = s.blockItemID(key)
		s.blocks[key] = open
		return s.emit(canon.ItemStarted{Item: canon.Message{ID: open.itemID, Role: canon.RoleAssistant}})
	case "thinking":
		open.itemID = s.blockItemID(key)
		s.blocks[key] = open
		return s.emit(canon.ItemStarted{Item: canon.ReasoningItem{ID: open.itemID}})
	case "redacted_thinking":
		open.itemID = s.blockItemID(key)
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
		if s.custom.has(open.name) {
			open.custom = true
			return s.emit(canon.ItemStarted{Item: canon.CustomToolCall{ID: open.itemID, CallID: open.callID, Name: open.name}})
		}
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
		return s.protocolFailure(errors.New("anthropic: content_block_delta missing delta"))
	}
	deltaType, _ := delta["type"].(string)
	open := s.openBlockFor(payload)
	switch deltaType {
	case "text_delta":
		text, ok := delta["text"].(string)
		if !ok || open == nil || open.kind != "text" {
			return s.protocolFailure(errors.New("anthropic: invalid text delta"))
		}
		open.text.WriteString(text)
		return s.emit(canon.TextDelta{ItemID: open.itemID, Text: text})
	case "thinking_delta":
		text, ok := delta["thinking"].(string)
		if !ok || open == nil || open.kind != "thinking" {
			return s.protocolFailure(errors.New("anthropic: invalid thinking delta"))
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
		partial, ok := delta["partial_json"].(string)
		if !ok || open == nil || open.kind != "tool_use" {
			return s.protocolFailure(errors.New("anthropic: invalid tool arguments delta"))
		}
		open.args.WriteString(partial)
		if open.custom {
			return nil
		}
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
		if open.custom {
			return s.finishCustomCall(open)
		}
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

var (
	freeformFallbackKeys = map[canon.ToolName][]string{
		"exec":        {"code", "script", "js", "javascript", "command", "cmd", "content"},
		"apply_patch": {"patch", "content"},
	}
	outerCodeFence       = regexp.MustCompile("(?s)^```[^\\r\\n]*\\r?\\n(.*?)\\r?\\n```$")
	patchEnvelope        = regexp.MustCompile(`(?s)^(\*\*\* Begin Patch(?: \*\*\*)?)(\r?\n)(.*)(\r?\n)(\*\*\* End Patch(?: \*\*\*)?)(\r?\n)?$`)
	patchOperationLine   = regexp.MustCompile(`(?m)^\*\*\* (?:Add|Update|Delete) File: [^\r\n]+$`)
	freeformFenceTargets = map[canon.ToolName]struct{}{"exec": {}, "apply_patch": {}}
)

func stripOuterCodeFence(text string, tool canon.ToolName) string {
	if _, ok := freeformFenceTargets[tool]; !ok {
		return text
	}
	if m := outerCodeFence.FindStringSubmatch(strings.TrimSpace(text)); m != nil {
		return m[1]
	}
	return text
}

func normalizePatchDelimiters(text string) string {
	m := patchEnvelope.FindStringSubmatch(text)
	if m == nil || !patchOperationLine.MatchString(m[3]) {
		return text
	}
	if m[1] == "*** Begin Patch" && m[5] == "*** End Patch" {
		return text
	}
	return "*** Begin Patch" + m[2] + m[3] + m[4] + "*** End Patch" + m[6]
}

func singleStringAlternate(fields map[string]json.RawMessage, tool canon.ToolName) (string, bool) {
	var found string
	count := 0
	for _, key := range freeformFallbackKeys[tool] {
		raw, ok := fields[key]
		if !ok {
			continue
		}
		var value string
		if json.Unmarshal(raw, &value) != nil {
			continue
		}
		found = value
		count++
	}
	return found, count == 1
}

func unwrapFreeformInput(buffered string, tool canon.ToolName) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(buffered), &fields) != nil || fields == nil {
		return stripOuterCodeFence(buffered, tool)
	}
	if raw, ok := fields["input"]; ok {
		var value string
		if json.Unmarshal(raw, &value) != nil {
			return buffered
		}
		return stripOuterCodeFence(value, tool)
	}
	if value, ok := singleStringAlternate(fields, tool); ok {
		return stripOuterCodeFence(value, tool)
	}
	return stripOuterCodeFence(buffered, tool)
}

func unwrapCustomInput(buffered string, tool canon.ToolName) string {
	input := unwrapFreeformInput(buffered, tool)
	if tool == "apply_patch" {
		return normalizePatchDelimiters(input)
	}
	return input
}

func (s *streamState) finishCustomCall(open *openBlock) error {
	input := unwrapCustomInput(open.args.String(), open.name)
	if input != "" {
		if err := s.emit(canon.CustomToolInputDelta{ItemID: open.itemID, Text: input}); err != nil {
			return err
		}
	}
	return s.emit(canon.ItemFinished{Item: canon.CustomToolCall{
		ID:     open.itemID,
		CallID: open.callID,
		Name:   open.name,
		Input:  input,
	}})
}

func (s *streamState) messageDelta(payload map[string]any) error {
	if err := s.mergeUsage(payload["usage"], true); err != nil {
		return err
	}
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
	case "max_tokens", "model_context_window_exceeded":
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
	if len(s.blocks) != 0 {
		return s.protocolFailure(errors.New("anthropic: message_stop with unfinished content blocks"))
	}
	if err := s.emit(canon.TurnFinished{Status: s.mapStopReason(s.stopValue), Usage: s.usage.canonUsage()}); err != nil {
		return err
	}
	return errTerminalDone
}

func (s *streamState) upstreamErrorEvent(payload map[string]any) error {
	errObj, _ := payload["error"].(map[string]any)
	message, _ := errObj["message"].(string)
	if message == "" {
		message = "anthropic: upstream stream error"
	}
	typ, _ := errObj["type"].(string)
	re := &provider.RunError{Kind: provider.TerminalOmitted, Class: streamErrorClass(typ), Cause: errors.New(message)}
	if re.Class == provider.ClassRateLimited {
		re.Kind = provider.Retryable
	} else if typ == "overloaded_error" {
		re.Kind = provider.Retryable
	}
	if raw, err := json.Marshal(errObj); err == nil && errObj != nil {
		re.Reported = &canon.ProviderError{Error: raw}
	}
	s.terminal = true
	if err := s.emit(canon.TurnFailed{Failure: canon.Failure{Reason: canon.FailUnknown, Message: message, Provider: re.Reported}, Usage: s.usage.canonUsage()}); err != nil {
		return err
	}
	re.Kind, re.Accepted = provider.TerminalEmitted, true
	return re
}

// streamErrorClass maps the error type of a mid-stream error event, which has
// no HTTP status of its own, onto the class the same condition gets from a
// status code.
func streamErrorClass(typ string) provider.ErrorClass {
	switch typ {
	case "rate_limit_error":
		return provider.ClassRateLimited
	case "invalid_request_error":
		return provider.ClassInvalidRequest
	case "authentication_error":
		return provider.ClassUnauthorized
	case "permission_error":
		return provider.ClassForbidden
	case "not_found_error":
		return provider.ClassNotFound
	case "request_too_large":
		return provider.ClassContextLength
	case "timeout_error":
		return provider.ClassTimeout
	default:
		return provider.ClassServer
	}
}

func (r *Runner) CountTokens(ctx context.Context, req provider.CountTokensRequest) (provider.TokenCount, error) {
	wr, err := r.buildWireRequest(canon.Request{
		Model:        req.Target.Model,
		Instructions: req.Instructions,
		Input:        req.Input,
		Tools:        req.Tools,
	}, req.Target.MaxOutputTokens, false)
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
	parsed, err := decodeStreamJSON(payload)
	if err != nil {
		return provider.TokenCount{}, &provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassServer, Cause: err}
	}
	tokens, err := usagewire.Number(parsed["input_tokens"])
	if err != nil {
		return provider.TokenCount{}, &provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassServer, Cause: fmt.Errorf("anthropic: invalid count_tokens input_tokens: %w", err)}
	}
	return provider.TokenCount{InputTokens: tokens}, nil
}
