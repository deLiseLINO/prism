package customchat

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
	"github.com/deLiseLINO/prism/internal/providers/openaierr"
)

var errTerminalDone = errors.New("customchat: terminal emitted")

type sseFrame struct {
	data string
}

func fieldValue(line, field string) (string, bool) {
	if !strings.HasPrefix(line, field) {
		return "", false
	}
	rest := line[len(field):]
	if rest == "" {
		return "", true
	}
	if !strings.HasPrefix(rest, ":") {
		return "", false
	}
	if strings.HasPrefix(rest, ": ") {
		return rest[2:], true
	}
	return rest[1:], true
}

func readFrames(r io.Reader, handle func(sseFrame) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxFrameLine)
	var data []string
	dispatch := func() error {
		if len(data) == 0 {
			return nil
		}
		frame := sseFrame{data: strings.Join(data, "\n")}
		data = nil
		return handle(frame)
	}
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		if strings.HasPrefix(line, ":") {
			continue
		}
		if value, ok := fieldValue(line, "data"); ok {
			data = append(data, value)
			continue
		}
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return dispatch()
}

type openTool struct {
	canonID    canon.ItemID
	callID     string
	upstreamID string
	index      int
	hasIndex   bool
	name       string
	args       strings.Builder
	started    bool
}

type streamer struct {
	sink   provider.Sink
	custom customTools

	respID  string
	msgID   canon.ItemID
	msgText strings.Builder
	msgOpen bool

	reasonText strings.Builder
	reasonOpen bool

	tools []*openTool
	usage canon.Usage

	status          canon.Status
	finished        bool
	finishReason    string
	terminalEmitted bool
}

func (r *Runner) runStream(body io.Reader, sink provider.Sink, custom customTools) error {
	st := &streamer{sink: sink, custom: custom}
	err := readFrames(body, st.handleFrame)
	if err != nil && !errors.Is(err, errTerminalDone) {
		var runErr provider.RunError
		if errors.As(err, &runErr) {
			return runErr
		}
		return runError(provider.TerminalOmitted, provider.ClassTransport, true, false, 0,
			fmt.Errorf("customchat: reading upstream stream: %w", err))
	}
	if err := st.closeTerminal(); err != nil && !errors.Is(err, errTerminalDone) {
		return err
	}
	if st.terminalEmitted {
		return nil
	}
	return runError(provider.TerminalOmitted, provider.ClassTransport, true, false, 0,
		errors.New("customchat: upstream stream ended before a terminal frame"))
}

func (s *streamer) closeTerminal() error {
	if s.finished && !s.terminalEmitted {
		return s.emitTerminal()
	}
	return nil
}

func (s *streamer) emit(ev canon.Event) error {
	if err := s.sink.Emit(ev); err != nil {
		return runError(provider.UnsafeReplay, provider.ClassTransport, true, false, 0, err)
	}
	return nil
}

func (s *streamer) emitTerminal() error {
	if s.terminalEmitted {
		return nil
	}
	s.terminalEmitted = true
	if err := s.emit(canon.TurnFinished{Status: s.status, Usage: s.usage}); err != nil {
		return err
	}
	return errTerminalDone
}

func (s *streamer) handleFrame(f sseFrame) error {
	if strings.TrimSpace(f.data) == "[DONE]" {
		if !s.finished {
			return s.protocolError("upstream stream closed with [DONE] before a finish reason")
		}
		return s.emitTerminal()
	}
	var payload map[string]any
	decoder := json.NewDecoder(strings.NewReader(f.data))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil || decoder.Decode(new(any)) != io.EOF {
		return s.protocolError("malformed upstream stream frame")
	}
	if _, ok := payload["error"]; ok {
		return s.failedTurn([]byte(f.data))
	}
	if s.terminalEmitted {
		return nil
	}
	if id, ok := payload["id"].(string); ok && id != "" {
		s.respID = id
	}
	usage, err := mergeChatUsage(payload["usage"], s.usage)
	if err != nil {
		return s.protocolError("invalid upstream usage: " + err.Error())
	}
	s.usage = usage
	choices, _ := payload["choices"].([]any)
	if len(choices) > 1 {
		return s.protocolError(fmt.Sprintf("upstream chunk carries %d choices; only one is representable", len(choices)))
	}
	if len(choices) == 0 {
		return nil
	}
	choice, ok := choices[0].(map[string]any)
	if !ok {
		return s.protocolError("upstream chunk choice is not an object")
	}
	if raw, present := choice["index"]; present {
		index, ok := raw.(json.Number)
		if !ok || index != "0" {
			return s.protocolError("upstream choice index must be integer zero")
		}
	}
	delta, _ := choice["delta"].(map[string]any)
	if s.finished {
		if deltaCarriesOutput(delta) {
			return s.protocolError("upstream emitted output after finish reason")
		}
		if reason, ok := choice["finish_reason"].(string); ok && reason != "" && reason != s.finishReason {
			return s.protocolError("upstream emitted a second finish reason")
		}
		return nil
	}
	if delta == nil {
		delta = map[string]any{}
	}
	if err := s.handleDelta(delta); err != nil {
		return err
	}
	if reason, ok := choice["finish_reason"].(string); ok && reason != "" {
		if s.finished {
			if reason == s.finishReason {
				return nil
			}
			return s.protocolError("upstream emitted a second finish reason")
		}
		s.finishReason = reason
		status, known := finishStatus(reason)
		if !known {
			return s.failUnknown(fmt.Sprintf("upstream finish reason %q is not representable", reason))
		}
		if err := s.finishItems(); err != nil {
			return err
		}
		s.status = status
		s.finished = true
	}
	return nil
}

func (s *streamer) handleDelta(delta map[string]any) error {
	if text, ok := delta["content"].(string); ok && text != "" {
		if err := s.ensureMessageOpen(); err != nil {
			return err
		}
		s.msgText.WriteString(text)
		if err := s.emit(canon.TextDelta{ItemID: s.msgID, Text: text}); err != nil {
			return err
		}
	}
	if text := reasoningDelta(delta); text != "" {
		if err := s.ensureReasoningOpen(); err != nil {
			return err
		}
		s.reasonText.WriteString(text)
		if err := s.emit(canon.ReasoningDelta{ItemID: s.reasoningID(), Text: text}); err != nil {
			return err
		}
	}
	if raw, present := delta["tool_calls"]; present {
		rawCalls, ok := raw.([]any)
		if !ok {
			return s.protocolError("upstream tool_calls delta is not an array")
		}
		for _, raw := range rawCalls {
			call, ok := raw.(map[string]any)
			if !ok {
				return s.protocolError("upstream tool call delta is not an object")
			}
			if err := s.handleToolCallDelta(call); err != nil {
				return err
			}
		}
	}
	return nil
}

// toolFor picks the open call a tool_calls delta belongs to. Gateways differ:
// the id names the call when present; otherwise the index does; a delta with
// neither continues the most recent call. A delta that reuses an index but
// carries a new id starts another call.
func (s *streamer) toolFor(call map[string]any) *openTool {
	id, _ := call["id"].(string)
	index, hasIndex := streamIndex(call["index"])
	if id != "" {
		for _, open := range s.tools {
			if open.upstreamID == id {
				return open
			}
		}
	}
	if hasIndex {
		for i := len(s.tools) - 1; i >= 0; i-- {
			open := s.tools[i]
			if open.hasIndex && open.index == index {
				if id != "" && open.upstreamID != "" && open.upstreamID != id {
					return nil
				}
				if id == "" && announcesNewCall(open, call) {
					return nil
				}
				return open
			}
		}
		return nil
	}
	if id != "" && len(s.tools) > 0 {
		open := s.tools[len(s.tools)-1]
		if !open.started && open.upstreamID == "" {
			return open
		}
	}
	if id == "" && len(s.tools) > 0 {
		open := s.tools[len(s.tools)-1]
		if announcesNewCall(open, call) {
			return nil
		}
		return open
	}
	return nil
}

// announcesNewCall reports whether an id-less delta starts another call: it
// names a function while the open call already has one and is either named
// differently or already holds a complete argument document.
func announcesNewCall(open *openTool, call map[string]any) bool {
	fn, _ := call["function"].(map[string]any)
	name, _ := fn["name"].(string)
	if name == "" || open.name == "" {
		return false
	}
	return name != open.name || json.Valid([]byte(open.args.String()))
}

// deltaCarriesOutput reports whether a delta holds text, thinking text or tool
// call data. Gateways send frames after the finish reason that keep keys such
// as role or an empty content, and those frames carry nothing to lose.
func deltaCarriesOutput(delta map[string]any) bool {
	if text, ok := delta["content"].(string); ok && text != "" {
		return true
	}
	if reasoningDelta(delta) != "" {
		return true
	}
	switch calls := delta["tool_calls"].(type) {
	case nil:
	case []any:
		return len(calls) > 0
	default:
		return true
	}
	return false
}

// Gateways name the thinking text differently; a chunk may carry several
// aliases of the same text, so only the first non-empty one counts.
func reasoningDelta(delta map[string]any) string {
	for _, field := range []string{"reasoning_content", "reasoning", "reasoning_text"} {
		if text, ok := delta[field].(string); ok && text != "" {
			return text
		}
	}
	return ""
}

func (s *streamer) handleToolCallDelta(call map[string]any) error {
	if typ, present := call["type"]; present && typ != "function" {
		return s.protocolError("upstream tool call type is not function")
	}
	if raw, supplied := call["index"]; supplied {
		if _, ok := streamIndex(raw); !ok {
			return s.protocolError("invalid tool call index")
		}
	}
	fn := map[string]any{}
	if raw, present := call["function"]; present {
		var ok bool
		fn, ok = raw.(map[string]any)
		if !ok {
			return s.protocolError("upstream tool call function is not an object")
		}
	}
	for _, fields := range []map[string]any{call, fn} {
		for _, key := range []string{"id", "name", "arguments"} {
			if value, present := fields[key]; present {
				if _, ok := value.(string); !ok {
					return s.protocolError("upstream tool call " + key + " is not a string")
				}
			}
		}
	}
	id, _ := call["id"].(string)
	name, _ := call["name"].(string)
	if n, _ := fn["name"].(string); n != "" {
		if name != "" && name != n {
			return s.protocolError("upstream tool call has conflicting names")
		}
		name = n
	}
	open := s.toolFor(call)
	if open == nil {
		open = &openTool{canonID: canon.ItemID(fmt.Sprintf("chat-tool-%d", len(s.tools))), upstreamID: id, callID: id, name: name}
		if index, ok := streamIndex(call["index"]); ok {
			open.index, open.hasIndex = index, true
		}
		s.tools = append(s.tools, open)
	} else if open.started && (id != "" && id != open.callID || name != "" && name != open.name) {
		return s.protocolError("upstream tool changed published identity")
	}
	if !open.started {
		if id != "" {
			open.callID, open.upstreamID = id, id
		}
		if name != "" {
			open.name = name
		}
	}
	args, _ := fn["arguments"].(string)
	open.args.WriteString(args)
	if open.started {
		if args != "" && !s.custom.has(open.name) {
			return s.emit(canon.ToolArgumentsDelta{ItemID: open.canonID, Bytes: []byte(args)})
		}
		return nil
	}
	return s.startTools(false)
}

func (s *streamer) startTools(finishing bool) error {
	for _, open := range s.tools {
		if open.started {
			continue
		}
		if open.name == "" || open.callID == "" && !finishing {
			break
		}
		if open.callID == "" {
			open.callID = mintCallID()
		}
		var item canon.Item = canon.FunctionCall{ID: open.canonID, CallID: canon.CallID(open.callID), Name: canon.ToolName(open.name)}
		if s.custom.has(open.name) {
			item = canon.CustomToolCall{ID: open.canonID, CallID: canon.CallID(open.callID), Name: canon.ToolName(open.name)}
		}
		if err := s.emit(canon.ItemStarted{Item: item}); err != nil {
			return err
		}
		open.started = true
		if open.args.Len() > 0 && !s.custom.has(open.name) {
			if err := s.emit(canon.ToolArgumentsDelta{ItemID: open.canonID, Bytes: []byte(open.args.String())}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *streamer) ensureMessageOpen() error {
	if s.msgOpen {
		return nil
	}
	s.msgOpen = true
	s.msgID = canon.ItemID(s.respID)
	if s.msgID == "" {
		s.msgID = "assistant"
	}
	return s.emit(canon.ItemStarted{Item: canon.Message{ID: s.msgID, Role: canon.RoleAssistant}})
}

func (s *streamer) reasoningID() canon.ItemID {
	if s.respID == "" {
		return "assistant-reasoning"
	}
	return canon.ItemID(s.respID + "-reasoning")
}

func (s *streamer) ensureReasoningOpen() error {
	if s.reasonOpen {
		return nil
	}
	s.reasonOpen = true
	return s.emit(canon.ItemStarted{Item: canon.ReasoningItem{ID: s.reasoningID()}})
}

func (s *streamer) finishItems() error {
	for i, open := range s.tools {
		if open.name == "" {
			return s.protocolError(fmt.Sprintf("upstream tool call %d finished without a function name", i))
		}
		if s.custom.has(open.name) {
			if _, err := unwrapCustomInput(open.args.String()); err != nil {
				return s.protocolError(err.Error())
			}
		} else if open.args.Len() > 0 && !json.Valid([]byte(open.args.String())) {
			return s.protocolError("upstream tool finished with malformed arguments")
		}
	}
	if err := s.startTools(true); err != nil {
		return err
	}
	if s.reasonOpen {
		if err := s.emit(canon.ItemFinished{Item: canon.ReasoningItem{ID: s.reasoningID(), Content: s.reasonText.String()}}); err != nil {
			return err
		}
		s.reasonOpen = false
	}
	if s.msgOpen {
		msg := canon.Message{ID: s.msgID, Role: canon.RoleAssistant}
		if s.msgText.Len() > 0 {
			msg.Content = []canon.Content{canon.TextContent{Text: s.msgText.String()}}
		}
		if err := s.emit(canon.ItemFinished{Item: msg}); err != nil {
			return err
		}
		s.msgOpen = false
	}
	for i, open := range s.tools {
		if open.name == "" {
			return s.protocolError(fmt.Sprintf("upstream tool call %d finished without a function name", i))
		}
		if s.custom.has(open.name) {
			input, _ := unwrapCustomInput(open.args.String())
			if input != "" {
				if err := s.emit(canon.CustomToolInputDelta{ItemID: open.canonID, Text: input}); err != nil {
					return err
				}
			}
			if err := s.emit(canon.ItemFinished{Item: canon.CustomToolCall{ID: open.canonID, CallID: canon.CallID(open.callID), Name: canon.ToolName(open.name), Input: input}}); err != nil {
				return err
			}
			continue
		}
		fc := canon.FunctionCall{
			ID:     open.canonID,
			CallID: canon.CallID(open.callID),
			Name:   canon.ToolName(open.name),
		}
		if open.args.Len() > 0 {
			fc.Arguments = []byte(open.args.String())
		}
		if err := s.emit(canon.ItemFinished{Item: fc}); err != nil {
			return err
		}
	}
	s.tools = nil
	return nil
}

func (s *streamer) failedTurn(raw []byte) error {
	if s.terminalEmitted {
		return s.protocolError("upstream emitted a second terminal event")
	}
	parsed, ok := openaierr.Parse(raw)
	if !ok {
		return s.protocolError("error frame carried no error value")
	}
	copied := parsed
	return s.emitFailed(canon.Failure{Reason: canon.FailUnknown, Message: openaierr.Text(parsed), Provider: &copied})
}

func (s *streamer) failUnknown(message string) error {
	if s.terminalEmitted {
		return s.protocolError("upstream emitted a second terminal event")
	}
	return s.emitFailed(canon.Failure{Reason: canon.FailUnknown, Message: message})
}

func (s *streamer) emitFailed(failure canon.Failure) error {
	s.terminalEmitted = true
	if err := s.emit(canon.TurnFailed{Failure: failure, Usage: s.usage}); err != nil {
		return err
	}
	cause := failure.Message
	if cause == "" {
		cause = "provider error"
	}
	class := provider.ClassServer
	if failure.Provider != nil {
		class = openaierr.ClassForInband(*failure.Provider, class)
	}
	return provider.RunError{Kind: provider.TerminalEmitted, Class: class, Accepted: true, Cause: errors.New(cause), Reported: failure.Provider}
}

func (s *streamer) protocolError(message string) error {
	if !s.terminalEmitted {
		s.terminalEmitted = true
		failure := canon.Failure{Reason: canon.FailUpstreamTransport, Message: "customchat: " + message}
		if err := s.emit(canon.TurnFailed{Failure: failure, Usage: s.usage}); err != nil {
			return err
		}
	}
	return runError(provider.TerminalEmitted, provider.ClassTransport, true, false, 0, errors.New("customchat: "+message))
}

func streamIndex(value any) (int, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	index, err := number.Int64()
	return int(index), err == nil && index >= 0 && index <= 1<<53-1
}

func finishStatus(reason string) (canon.Status, bool) {
	switch reason {
	case "stop", "tool_calls", "function_call":
		return canon.Completed(), true
	case "length":
		return canon.Incomplete(canon.IncompleteMaxOutputTokens), true
	case "content_filter":
		return canon.Incomplete(canon.IncompleteContentFilter), true
	default:
		return canon.Status{}, false
	}
}

// maxFrameLine bounds one SSE line. A terminal frame can repeat the whole
// output, so the limit sits well above a typical delta.
const maxFrameLine = 64 << 20
