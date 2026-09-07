package customchat

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"prism/internal/canon"
	"prism/internal/provider"
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
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
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
	canonID canon.ItemID
	callID  string
	name    string
	args    strings.Builder
}

type streamer struct {
	sink provider.Sink

	respID  string
	msgID   canon.ItemID
	msgText strings.Builder
	msgOpen bool

	reasonText strings.Builder
	reasonOpen bool

	tools   map[int]*openTool
	toolSeq []int
	usage   canon.Usage

	status          canon.Status
	finished        bool
	finishReason    string
	terminalEmitted bool
}

func (r *Runner) runStream(body io.Reader, sink provider.Sink) error {
	st := &streamer{sink: sink, tools: make(map[int]*openTool)}
	err := readFrames(body, st.handleFrame)
	if err != nil && !errors.Is(err, errTerminalDone) {
		var runErr provider.RunError
		if errors.As(err, &runErr) {
			return runErr
		}
		return runError(provider.TerminalOmitted, provider.ClassTransport, true, false, 0,
			fmt.Errorf("customchat: reading upstream stream: %w", err))
	}
	if err := st.closeTerminal(); err != nil {
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
	if err := json.Unmarshal([]byte(f.data), &payload); err != nil {
		return runError(provider.TerminalOmitted, provider.ClassTransport, true, false, 0,
			fmt.Errorf("customchat: malformed upstream stream frame: %w", err))
	}
	if errObj, ok := payload["error"].(map[string]any); ok {
		return s.failedTurn(errorMessage(errObj))
	}
	if s.terminalEmitted {
		return nil
	}
	if id, ok := payload["id"].(string); ok && id != "" {
		s.respID = id
	}
	if usage, ok := usageFromChat(payload["usage"]); ok {
		s.usage = usage
	}
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
	if index, ok := choice["index"].(float64); ok && int(index) != 0 {
		return s.protocolError(fmt.Sprintf("upstream choice index %d is not representable", int(index)))
	}
	delta, _ := choice["delta"].(map[string]any)
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
			return s.failedTurn(fmt.Sprintf("upstream finish reason %q is not representable", reason))
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
		s.ensureMessageOpen()
		s.msgText.WriteString(text)
		if err := s.emit(canon.TextDelta{ItemID: s.msgID, Text: text}); err != nil {
			return err
		}
	}
	if text, ok := delta["reasoning_content"].(string); ok && text != "" {
		s.ensureReasoningOpen()
		s.reasonText.WriteString(text)
		if err := s.emit(canon.ReasoningDelta{ItemID: s.reasoningID(), Text: text}); err != nil {
			return err
		}
	}
	if rawCalls, ok := delta["tool_calls"].([]any); ok {
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

func (s *streamer) handleToolCallDelta(call map[string]any) error {
	rawIndex, ok := call["index"].(float64)
	if !ok {
		return s.protocolError("upstream tool call delta is missing index")
	}
	index := int(rawIndex)
	open, exists := s.tools[index]
	if !exists {
		id, _ := call["id"].(string)
		name, _ := call["name"].(string)
		if fn, ok := call["function"].(map[string]any); ok {
			if n, ok := fn["name"].(string); ok && n != "" && name == "" {
				name = n
			}
		}
		callID := id
		if callID == "" {
			callID = mintCallID()
		}
		open = &openTool{canonID: canon.ItemID(callID), callID: callID, name: name}
		s.tools[index] = open
		s.toolSeq = append(s.toolSeq, index)
		if err := s.emit(canon.ItemStarted{Item: canon.FunctionCall{
			ID:     canon.ItemID(callID),
			CallID: canon.CallID(callID),
			Name:   canon.ToolName(name),
		}}); err != nil {
			return err
		}
	}
	fn, _ := call["function"].(map[string]any)
	if fn != nil {
		if name, ok := fn["name"].(string); ok && name != "" {
			open.name = name
		}
		if args, ok := fn["arguments"].(string); ok && args != "" {
			open.args.WriteString(args)
			if err := s.emit(canon.ToolArgumentsDelta{ItemID: open.canonID, Bytes: []byte(args)}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *streamer) ensureMessageOpen() {
	if s.msgOpen {
		return
	}
	s.msgOpen = true
	s.msgID = canon.ItemID(s.respID)
	if s.msgID == "" {
		s.msgID = "assistant"
	}
	_ = s.emit(canon.ItemStarted{Item: canon.Message{ID: s.msgID, Role: canon.RoleAssistant}})
}

func (s *streamer) reasoningID() canon.ItemID {
	if s.respID == "" {
		return "assistant-reasoning"
	}
	return canon.ItemID(s.respID + "-reasoning")
}

func (s *streamer) ensureReasoningOpen() {
	if s.reasonOpen {
		return
	}
	s.reasonOpen = true
	_ = s.emit(canon.ItemStarted{Item: canon.ReasoningItem{ID: s.reasoningID()}})
}

func (s *streamer) finishItems() error {
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
	for _, index := range s.toolSeq {
		open := s.tools[index]
		if open.name == "" {
			return s.protocolError(fmt.Sprintf("upstream tool call %d finished without a function name", index))
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
	s.tools = make(map[int]*openTool)
	s.toolSeq = nil
	return nil
}

func (s *streamer) failedTurn(message string) error {
	if s.terminalEmitted {
		return s.protocolError("upstream emitted a second terminal event")
	}
	s.terminalEmitted = true
	if err := s.emit(canon.TurnFailed{Failure: canon.Failure{Reason: canon.FailUnknown, Message: message}}); err != nil {
		return err
	}
	return runError(provider.TerminalEmitted, provider.ClassServer, true, false, 0, errors.New(message))
}

func (s *streamer) protocolError(message string) error {
	return runError(provider.TerminalOmitted, provider.ClassTransport, true, false, 0, errors.New("customchat: "+message))
}

func errorMessage(errObj map[string]any) string {
	if message, ok := errObj["message"].(string); ok && message != "" {
		return message
	}
	return "upstream request failed"
}

func usageFromChat(v any) (canon.Usage, bool) {
	u, ok := v.(map[string]any)
	if !ok {
		return canon.Usage{}, false
	}
	usage := canon.Usage{
		InputTokens:  numberOf(u["prompt_tokens"]),
		OutputTokens: numberOf(u["completion_tokens"]),
		TotalTokens:  numberOf(u["total_tokens"]),
	}
	if d, ok := u["prompt_tokens_details"].(map[string]any); ok {
		usage.CachedInputTokens = numberOf(d["cached_tokens"])
	}
	if d, ok := u["completion_tokens_details"].(map[string]any); ok {
		usage.ReasoningTokens = numberOf(d["reasoning_tokens"])
	}
	return usage, true
}

func numberOf(v any) int64 {
	f, ok := v.(float64)
	if !ok {
		return 0
	}
	return int64(f)
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
