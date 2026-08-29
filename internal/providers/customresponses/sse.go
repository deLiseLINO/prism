package customresponses

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

var errTerminalDone = errors.New("customresponses: terminal emitted")

type sseFrame struct {
	event string
	data  string
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
	var event string
	var data []string
	dispatch := func() error {
		if len(data) == 0 {
			event = ""
			return nil
		}
		frame := sseFrame{event: event, data: strings.Join(data, "\n")}
		event = ""
		data = nil
		return handle(frame)
	}
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		if strings.HasPrefix(line, ":") {
			continue
		}
		if value, ok := fieldValue(line, "event"); ok {
			event = value
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

type openOut struct {
	kind string
	text strings.Builder
	args strings.Builder
}

type streamer struct {
	sink provider.Sink
	open map[canon.ItemID]*openOut
}

func (r *Runner) runStream(body io.Reader, sink provider.Sink) error {
	st := &streamer{sink: sink, open: make(map[canon.ItemID]*openOut)}
	err := readFrames(body, st.handleFrame)
	if err != nil {
		if errors.Is(err, errTerminalDone) {
			return nil
		}
		var runErr provider.RunError
		if errors.As(err, &runErr) {
			return runErr
		}
		return runError(provider.TerminalOmitted, provider.ClassTransport, true, false, 0, err)
	}
	return runError(provider.TerminalOmitted, provider.ClassTransport, true, false, 0, errors.New("customresponses: upstream stream ended before a terminal frame"))
}

func (s *streamer) emit(ev canon.Event) error {
	if err := s.sink.Emit(ev); err != nil {
		return runError(provider.UnsafeReplay, provider.ClassTransport, true, false, 0, err)
	}
	return nil
}

func (s *streamer) handleFrame(f sseFrame) error {
	var payload map[string]any
	if err := json.Unmarshal([]byte(f.data), &payload); err != nil {
		return nil
	}
	name, _ := payload["type"].(string)
	if name == "" {
		name = f.event
	}
	switch name {
	case "response.created", "response.in_progress", "response.heartbeat", "":
		return nil
	case "response.output_item.added":
		item, ok := payload["item"].(map[string]any)
		if !ok {
			return s.protocolError("output_item.added without item")
		}
		kind, err := itemKind(item)
		if err != nil {
			return err
		}
		id, _ := item["id"].(string)
		if id == "" {
			return s.protocolError("output_item.added without id")
		}
		canonItem, _, err := itemFromWire(item)
		if err != nil {
			return err
		}
		s.open[canon.ItemID(id)] = &openOut{kind: kind}
		return s.emit(canon.ItemStarted{Item: canonItem})
	case "response.output_text.delta":
		id, delta, err := deltaOf(payload)
		if err != nil {
			return err
		}
		if err := s.ensureOpen(id, "message"); err != nil {
			return err
		}
		s.open[id].text.WriteString(delta)
		return s.emit(canon.TextDelta{ItemID: id, Text: delta})
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		id, delta, err := deltaOf(payload)
		if err != nil {
			return err
		}
		if err := s.ensureOpen(id, "reasoning"); err != nil {
			return err
		}
		s.open[id].text.WriteString(delta)
		return s.emit(canon.ReasoningDelta{ItemID: id, Text: delta})
	case "response.function_call_arguments.delta":
		id, delta, err := deltaOf(payload)
		if err != nil {
			return err
		}
		if err := s.ensureOpen(id, "function_call"); err != nil {
			return err
		}
		s.open[id].args.WriteString(delta)
		return s.emit(canon.ToolArgumentsDelta{ItemID: id, Bytes: []byte(delta)})
	case "response.custom_tool_call_input.delta":
		id, delta, err := deltaOf(payload)
		if err != nil {
			return err
		}
		if err := s.ensureOpen(id, "custom_tool_call"); err != nil {
			return err
		}
		s.open[id].text.WriteString(delta)
		return s.emit(canon.CustomToolInputDelta{ItemID: id, Text: delta})
	case "response.output_item.done":
		item, ok := payload["item"].(map[string]any)
		if !ok {
			return s.protocolError("output_item.done without item")
		}
		kind, err := itemKind(item)
		if err != nil {
			return err
		}
		id, _ := item["id"].(string)
		if id == "" {
			return s.protocolError("output_item.done without id")
		}
		var open *openOut
		if live, ok := s.open[canon.ItemID(id)]; ok {
			open = live
		} else {
			if err := s.ensureOpen(canon.ItemID(id), kind); err != nil {
				return err
			}
			open = s.open[canon.ItemID(id)]
		}
		final, _, err := itemFromWire(item, open)
		if err != nil {
			return err
		}
		delete(s.open, canon.ItemID(id))
		return s.emit(canon.ItemFinished{Item: final})
	case "response.completed":
		response, _ := payload["response"].(map[string]any)
		if response == nil {
			response = map[string]any{}
		}
		if err := s.emit(canon.TurnFinished{Status: canon.Completed(), Usage: usageFrom(response)}); err != nil {
			return err
		}
		return errTerminalDone
	case "response.incomplete":
		response, _ := payload["response"].(map[string]any)
		if response == nil {
			response = map[string]any{}
		}
		details, _ := response["incomplete_details"].(map[string]any)
		reason, _ := details["reason"].(string)
		var status canon.Status
		switch reason {
		case "max_output_tokens":
			status = canon.Incomplete(canon.IncompleteMaxOutputTokens)
		case "content_filter":
			status = canon.Incomplete(canon.IncompleteContentFilter)
		default:
			return s.failedTurn(fmt.Sprintf("upstream stream ended early (%s)", reason))
		}
		if err := s.emit(canon.TurnFinished{Status: status, Usage: usageFrom(response)}); err != nil {
			return err
		}
		return errTerminalDone
	case "response.failed", "error":
		message := errorMessage(payload)
		return s.failedTurn(message)
	default:
		return nil
	}
}

func errorMessage(payload map[string]any) string {
	if response, ok := payload["response"].(map[string]any); ok {
		if e, ok := response["error"].(map[string]any); ok {
			if message, ok := e["message"].(string); ok && message != "" {
				return message
			}
		}
	}
	if e, ok := payload["error"].(map[string]any); ok {
		if message, ok := e["message"].(string); ok && message != "" {
			return message
		}
	}
	return "upstream request failed"
}

func (s *streamer) failedTurn(message string) error {
	if err := s.emit(canon.TurnFailed{Failure: canon.Failure{Reason: canon.FailUnknown, Message: message}}); err != nil {
		return err
	}
	return runError(provider.TerminalEmitted, provider.ClassServer, true, false, 0, errors.New(message))
}

func (s *streamer) protocolError(message string) error {
	return runError(provider.TerminalOmitted, provider.ClassTransport, true, false, 0, errors.New("customresponses: "+message))
}

func (s *streamer) ensureOpen(id canon.ItemID, kind string) error {
	if _, live := s.open[id]; live {
		return nil
	}
	s.open[id] = &openOut{kind: kind}
	var item canon.Item
	switch kind {
	case "message":
		item = canon.Message{ID: id, Role: canon.RoleAssistant}
	case "reasoning":
		item = canon.ReasoningItem{ID: id}
	case "function_call":
		item = canon.FunctionCall{ID: id}
	case "custom_tool_call":
		item = canon.CustomToolCall{ID: id}
	case "local_shell_call":
		item = canon.LocalShellCall{ID: id}
	default:
		return s.protocolError("cannot open item kind " + kind)
	}
	return s.emit(canon.ItemStarted{Item: item})
}

func deltaOf(payload map[string]any) (canon.ItemID, string, error) {
	id, _ := payload["item_id"].(string)
	if id == "" {
		return "", "", runError(provider.TerminalOmitted, provider.ClassTransport, true, false, 0, errors.New("customresponses: delta event missing item_id"))
	}
	delta, _ := payload["delta"].(string)
	return canon.ItemID(id), delta, nil
}

func itemKind(item map[string]any) (string, error) {
	kind, _ := item["type"].(string)
	switch kind {
	case "message", "reasoning", "function_call", "custom_tool_call", "local_shell_call":
		return kind, nil
	default:
		return "", runError(provider.TerminalOmitted, provider.ClassTransport, true, false, 0, fmt.Errorf("customresponses: upstream output item type %q cannot be represented in canon", kind))
	}
}

func itemFromWire(item map[string]any, open ...*openOut) (canon.Item, canon.ItemID, error) {
	var state *openOut
	if len(open) > 0 {
		state = open[0]
	}
	kind, _ := item["type"].(string)
	id, _ := item["id"].(string)
	switch kind {
	case "message":
		role := canon.RoleAssistant
		switch item["role"] {
		case "user":
			role = canon.RoleUser
		case "system":
			role = canon.RoleSystem
		}
		var content []canon.Content
		parts, _ := item["content"].([]any)
		for _, p := range parts {
			part, ok := p.(map[string]any)
			if !ok || part["type"] != "output_text" {
				continue
			}
			text, _ := part["text"].(string)
			content = append(content, canon.TextContent{Text: text})
		}
		if len(content) == 0 && state != nil && state.text.Len() > 0 {
			content = append(content, canon.TextContent{Text: state.text.String()})
		}
		return canon.Message{ID: canon.ItemID(id), Role: role, Content: content}, canon.ItemID(id), nil
	case "reasoning":
		ri := canon.ReasoningItem{ID: canon.ItemID(id)}
		if content, ok := item["content"].([]any); ok {
			for _, p := range content {
				part, ok := p.(map[string]any)
				if !ok || part["type"] != "reasoning_text" {
					continue
				}
				text, _ := part["text"].(string)
				ri.Content += text
			}
		}
		if ri.Content == "" && state != nil && state.text.Len() > 0 {
			ri.Content = state.text.String()
		}
		if summary, ok := item["summary"].([]any); ok {
			for _, p := range summary {
				part, ok := p.(map[string]any)
				if !ok || part["type"] != "summary_text" {
					continue
				}
				text, _ := part["text"].(string)
				ri.Summary = append(ri.Summary, canon.TextContent{Text: text})
			}
		}
		return ri, canon.ItemID(id), nil
	case "function_call":
		fc := canon.FunctionCall{ID: canon.ItemID(id)}
		if callID, ok := item["call_id"].(string); ok {
			fc.CallID = canon.CallID(callID)
		}
		if name, ok := item["name"].(string); ok {
			fc.Name = canon.ToolName(name)
		}
		if args, ok := item["arguments"].(string); ok && args != "" {
			fc.Arguments = []byte(args)
		} else if state != nil {
			fc.Arguments = []byte(state.args.String())
		}
		return fc, canon.ItemID(id), nil
	case "custom_tool_call":
		ct := canon.CustomToolCall{ID: canon.ItemID(id)}
		if callID, ok := item["call_id"].(string); ok {
			ct.CallID = canon.CallID(callID)
		}
		if name, ok := item["name"].(string); ok {
			ct.Name = canon.ToolName(name)
		}
		if input, ok := item["input"].(string); ok && input != "" {
			ct.Input = input
		} else if state != nil {
			ct.Input = state.text.String()
		}
		return ct, canon.ItemID(id), nil
	case "local_shell_call":
		ls := canon.LocalShellCall{ID: canon.ItemID(id)}
		if callID, ok := item["call_id"].(string); ok {
			ls.CallID = canon.CallID(callID)
		}
		if action, ok := item["action"].(map[string]any); ok {
			if command, ok := action["command"].([]any); ok && len(command) > 0 {
				parts := make([]string, 0, len(command))
				for _, c := range command {
					if s, ok := c.(string); ok {
						parts = append(parts, s)
					}
				}
				ls.Command = strings.Join(parts, " ")
			}
		}
		return ls, canon.ItemID(id), nil
	default:
		return nil, "", runError(provider.TerminalOmitted, provider.ClassTransport, true, false, 0, fmt.Errorf("customresponses: upstream output item type %q cannot be represented in canon", kind))
	}
}
