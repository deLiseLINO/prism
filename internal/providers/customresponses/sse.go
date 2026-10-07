package customresponses

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
	"github.com/deLiseLINO/prism/internal/providers/usagewire"
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
	sc.Buffer(make([]byte, 0, 64*1024), maxFrameLine)
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
	kind     string
	text     strings.Builder
	item     canon.Item
	finished canon.Item
	index    int
	args     strings.Builder
}

type streamer struct {
	sink      provider.Sink
	open      map[canon.ItemID]*openOut
	pending   map[int][]sseFrame
	nextIndex int
	usage     canon.Usage
	failed    bool
}

func (r *Runner) runStream(body io.Reader, sink provider.Sink) error {
	st := &streamer{sink: sink, open: make(map[canon.ItemID]*openOut), pending: make(map[int][]sseFrame)}
	err := readFrames(body, st.handleFrame)
	if err != nil {
		if errors.Is(err, errTerminalDone) {
			return nil
		}
		var runErr provider.RunError
		if errors.As(err, &runErr) {
			if runErr.Kind == provider.TerminalOmitted {
				message := runErr.Error()
				if runErr.Cause != nil {
					message = runErr.Cause.Error()
				}
				return st.protocolError(message)
			}
			return runErr
		}
		return st.protocolError(err.Error())
	}
	return st.protocolError("upstream stream ended before a terminal frame")
}

func (s *streamer) emit(ev canon.Event) error {
	if err := s.sink.Emit(ev); err != nil {
		return runError(provider.UnsafeReplay, provider.ClassTransport, true, false, 0, err)
	}
	return nil
}

func (s *streamer) handleFrame(f sseFrame) error {
	var payload map[string]any
	decoder := json.NewDecoder(strings.NewReader(f.data))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil || decoder.Decode(new(any)) != io.EOF {
		return s.protocolError("malformed upstream stream frame")
	}
	name, _ := payload["type"].(string)
	if name == "" {
		name = f.event
	}
	if response, ok := payload["response"].(map[string]any); ok {
		usage, err := decodeResponseUsage(response, s.usage)
		if err != nil {
			return s.protocolError("invalid upstream usage")
		}
		s.usage = usage
	}
	if raw, supplied := payload["output_index"]; supplied {
		index, err := usagewire.Number(raw)
		if err != nil || index > 1<<20 {
			return s.protocolError("invalid output index")
		}
		if int(index) > s.nextIndex || int(index) == s.nextIndex && name != "response.output_item.added" && name != "response.output_item.done" {
			s.pending[int(index)] = append(s.pending[int(index)], f)
			return nil
		}
		if int(index) == s.nextIndex {
			if err := s.handlePayload(payload, name); err != nil {
				return err
			}
			frames := s.pending[s.nextIndex]
			delete(s.pending, s.nextIndex)
			for _, frame := range frames {
				var queued map[string]any
				decoder := json.NewDecoder(strings.NewReader(frame.data))
				decoder.UseNumber()
				if err := decoder.Decode(&queued); err != nil {
					return err
				}
				queuedName, _ := queued["type"].(string)
				if queuedName == "" {
					queuedName = frame.event
				}
				if err := s.handlePayload(queued, queuedName); err != nil {
					return err
				}
			}
			s.nextIndex++
			for {
				frames := s.pending[s.nextIndex]
				if len(frames) == 0 {
					break
				}
				delete(s.pending, s.nextIndex)
				before := s.nextIndex
				for _, frame := range frames {
					if err := s.handleFrame(frame); err != nil {
						return err
					}
				}
				if before == s.nextIndex {
					break
				}
			}
			return nil
		}
	}
	if (name == "response.completed" || name == "response.incomplete") && len(s.pending) != 0 {
		return s.protocolError("terminal preceded missing output index")
	}
	return s.handlePayload(payload, name)
}

func (s *streamer) handlePayload(payload map[string]any, name string) error {
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
		if err := validateItemFields(item); err != nil {
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
		if _, exists := s.open[canon.ItemID(id)]; exists {
			return s.protocolError("duplicate output item id")
		}
		s.open[canon.ItemID(id)] = &openOut{kind: kind, item: canonItem, index: len(s.open)}
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
		if err := validateFinishedItem(item); err != nil {
			return err
		}
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
		if open.finished != nil || open.kind != kind {
			return s.protocolError("conflicting output item done")
		}
		if err := reconcileItem(final, open); err != nil {
			return err
		}
		open.finished = final
		return s.emit(canon.ItemFinished{Item: final})
	case "response.completed":
		response, ok := payload["response"].(map[string]any)
		if !ok {
			return s.protocolError("completed response must be an object")
		}
		if err := s.reconcileTerminal(response, "completed"); err != nil {
			return err
		}
		if err := s.emit(canon.TurnFinished{Status: canon.Completed(), Usage: s.usage}); err != nil {
			return err
		}
		return errTerminalDone
	case "response.incomplete":
		response, ok := payload["response"].(map[string]any)
		if !ok {
			return s.protocolError("incomplete response must be an object")
		}
		if err := s.reconcileTerminal(response, "incomplete"); err != nil {
			return err
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
			return s.failUnknown(fmt.Sprintf("upstream stream ended early (%s)", reason))
		}
		if err := s.emit(canon.TurnFinished{Status: status, Usage: s.usage}); err != nil {
			return err
		}
		return errTerminalDone
	case "response.failed", "error":
		raw, _ := json.Marshal(payload)
		return s.failedTurn(raw, s.usage)
	default:
		return nil
	}
}

func (s *streamer) failedTurn(raw []byte, usage canon.Usage) error {
	parsed, ok := openaierr.Parse(raw)
	if !ok {
		return s.protocolError("failed event carried no error value")
	}
	copied := parsed
	return s.emitFailed(canon.Failure{Reason: canon.FailUnknown, Message: openaierr.Text(parsed), Provider: &copied}, usage)
}

func (s *streamer) failUnknown(message string) error {
	return s.emitFailed(canon.Failure{Reason: canon.FailUnknown, Message: message}, s.usage)
}

func (s *streamer) emitFailed(failure canon.Failure, usage canon.Usage) error {
	s.failed = true
	if err := s.emit(canon.TurnFailed{Failure: failure, Usage: usage}); err != nil {
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
	failure := canon.Failure{Reason: canon.FailUpstreamTransport, Message: "customresponses: " + message}
	if !s.failed {
		s.failed = true
		if err := s.sink.Emit(canon.TurnFailed{Failure: failure, Usage: s.usage}); err != nil {
			return runError(provider.UnsafeReplay, provider.ClassTransport, true, false, 0, err)
		}
	}
	return runError(provider.TerminalOmitted, provider.ClassTransport, true, false, 0, errors.New(failure.Message))
}

func (s *streamer) ensureOpen(id canon.ItemID, kind string) error {
	if open, live := s.open[id]; live {
		if open.finished != nil || open.kind != kind {
			return s.protocolError("event for closed or mismatched item")
		}
		return nil
	}
	s.open[id] = &openOut{kind: kind, index: len(s.open)}
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
	s.open[id].item = item
	return s.emit(canon.ItemStarted{Item: item})
}

func (s *streamer) reconcileTerminal(response map[string]any, status string) error {
	if len(response) == 0 {
		return s.protocolError("terminal response lacks completion payload")
	}
	if value, supplied := response["status"]; supplied && value != status {
		return s.protocolError("terminal status conflicts with event")
	}
	raw, supplied := response["output"]
	if !supplied {
		for _, open := range s.open {
			if open.finished == nil {
				return s.protocolError("terminal omitted unfinished output")
			}
		}
		return nil
	}
	values, ok := raw.([]any)
	if !ok {
		return s.protocolError("terminal output must be an array")
	}
	if len(values) == 0 {
		for _, open := range s.open {
			if open.finished == nil {
				return s.protocolError("terminal omitted unfinished output")
			}
		}
		return nil
	}
	items := make([]canon.Item, len(values))
	seen := make(map[canon.ItemID]bool, len(values))
	for index, value := range values {
		wire, ok := value.(map[string]any)
		if !ok {
			return s.protocolError("terminal output item must be an object")
		}
		if id, _ := wire["id"].(string); id != "" {
			if open := s.open[canon.ItemID(id)]; open != nil && open.finished != nil {
				kind, err := itemKind(wire)
				if err != nil || kind != open.kind || open.index != index || seen[canon.ItemID(id)] {
					return s.protocolError("terminal conflicts with emitted output")
				}
				seen[canon.ItemID(id)] = true
				items[index] = open.finished
				continue
			}
		}
		if err := validateFinishedItem(wire); err != nil {
			return err
		}
		if status == "incomplete" && (wire["type"] == "function_call" || wire["type"] == "custom_tool_call" || wire["type"] == "local_shell_call") && wire["status"] != "completed" {
			return s.protocolError("incomplete terminal tool lacks completed status")
		}
		item, id, err := itemFromWire(wire)
		if err != nil {
			return err
		}
		if id == "" || seen[id] {
			return s.protocolError("terminal item id missing or duplicated")
		}
		seen[id] = true
		if open := s.open[id]; open != nil {
			if open.index != index {
				return s.protocolError("terminal conflicts with emitted output")
			}
			if err := reconcileItem(item, open); err != nil {
				return err
			}
		} else if index < len(s.open) {
			return s.protocolError("terminal conflicts with emitted order")
		}
		items[index] = item
	}
	for id := range s.open {
		if !seen[id] {
			return s.protocolError("terminal omits emitted output")
		}
	}
	for index, item := range items {
		wire := values[index].(map[string]any)
		id := canon.ItemID(wire["id"].(string))
		open := s.open[id]
		if open != nil && open.finished != nil {
			continue
		}
		if open == nil {
			kind, _ := itemKind(wire)
			open = &openOut{kind: kind, index: index, item: item}
			s.open[id] = open
			if err := s.emit(canon.ItemStarted{Item: item}); err != nil {
				return err
			}
		}
		if err := s.emit(canon.ItemFinished{Item: item}); err != nil {
			return err
		}
		open.finished = item
	}
	return nil
}

func reconcileItem(item canon.Item, open *openOut) error {
	var final, seen string
	switch item := item.(type) {
	case canon.Message:
		for _, part := range item.Content {
			if text, ok := part.(canon.TextContent); ok {
				final += text.Text
			}
		}
		seen = open.text.String()
	case canon.ReasoningItem:
		final, seen = item.Content, open.text.String()
		if final == "" {
			for _, part := range item.Summary {
				final += part.Text
			}
		}
	case canon.FunctionCall:
		final, seen = string(item.Arguments), open.args.String()
		if old, ok := open.item.(canon.FunctionCall); !ok || old.CallID != "" && old.CallID != item.CallID || old.Name != "" && old.Name != item.Name {
			return malformedWire("final function identity conflicts with introduction")
		}
	case canon.CustomToolCall:
		final, seen = item.Input, open.text.String()
		if old, ok := open.item.(canon.CustomToolCall); !ok || old.CallID != "" && old.CallID != item.CallID || old.Name != "" && old.Name != item.Name {
			return malformedWire("final custom identity conflicts with introduction")
		}
	}
	if !strings.HasPrefix(final, seen) {
		return malformedWire("final item conflicts with emitted deltas")
	}
	return nil
}

func malformedWire(message string) error {
	return runError(provider.TerminalOmitted, provider.ClassTransport, true, false, 0, errors.New("customresponses: "+message))
}

func validateFinishedItem(item map[string]any) error {
	if _, err := itemKind(item); err != nil {
		return err
	}
	if err := validateItemFields(item); err != nil {
		return err
	}
	switch item["type"] {
	case "function_call", "custom_tool_call":
		if item["name"] == nil || item["name"] == "" || item["call_id"] == nil || item["call_id"] == "" {
			return malformedWire("finished tool lacks identity")
		}
		if item["type"] == "function_call" {
			args, _ := item["arguments"].(string)
			if !json.Valid([]byte(args)) {
				return malformedWire("finished function has malformed arguments")
			}
		} else if _, ok := item["input"].(string); !ok {
			return malformedWire("finished custom tool lacks string input")
		}
	}
	return nil
}

func validateItemFields(item map[string]any) error {
	for _, field := range []string{"id", "name", "call_id", "arguments", "input", "encrypted_content"} {
		if value, present := item[field]; present && value != nil {
			if _, ok := value.(string); !ok {
				return malformedWire(field + " must be a string")
			}
		}
	}
	for _, field := range []string{"content", "summary"} {
		if value, supplied := item[field]; supplied {
			parts, ok := value.([]any)
			if !ok {
				return malformedWire(field + " must be an array")
			}
			for _, value := range parts {
				part, ok := value.(map[string]any)
				if !ok {
					return malformedWire(field + " part must be an object")
				}
				if typ := part["type"]; typ == "output_text" || typ == "reasoning_text" || typ == "summary_text" {
					if _, ok := part["text"].(string); !ok {
						return malformedWire(field + " text must be a string")
					}
				}
				if part["type"] == "refusal" {
					if _, ok := part["refusal"].(string); !ok {
						return malformedWire("refusal must be a string")
					}
				}
			}
		}
	}
	return nil
}

func deltaOf(payload map[string]any) (canon.ItemID, string, error) {
	id, _ := payload["item_id"].(string)
	if id == "" {
		return "", "", runError(provider.TerminalOmitted, provider.ClassTransport, true, false, 0, errors.New("customresponses: delta event missing item_id"))
	}
	delta, ok := payload["delta"].(string)
	if !ok {
		return "", "", malformedWire("delta must be a string")
	}
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
			if !ok {
				continue
			}
			if part["type"] == "refusal" {
				text, _ := part["refusal"].(string)
				content = append(content, canon.TextContent{Text: text})
				continue
			}
			if part["type"] != "output_text" {
				continue
			}
			text, _ := part["text"].(string)
			content = append(content, canon.TextContent{Text: text})
		}
		if len(content) == 0 && state != nil && state.text.Len() > 0 {
			content = append(content, canon.TextContent{Text: state.text.String()})
		}
		phase, _ := item["phase"].(string)
		return canon.Message{ID: canon.ItemID(id), Role: role, Phase: canon.ParseMessagePhase(phase), Content: content}, canon.ItemID(id), nil
	case "reasoning":
		ri := canon.ReasoningItem{ID: canon.ItemID(id)}
		if encrypted, ok := item["encrypted_content"].(string); ok && encrypted != "" {
			ri.State = canon.OpaqueRef{Store: canon.StoreWire, Key: encrypted}
		}
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
		if ri.Content == "" {
			for _, part := range ri.Summary {
				ri.Content += part.Text
			}
		}
		if ri.Content == "" && state != nil {
			ri.Content = state.text.String()
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
		if input, ok := item["input"].(string); ok {
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

// maxFrameLine bounds one SSE line. A terminal frame can repeat the whole
// output, so the limit sits well above a typical delta.
const maxFrameLine = 64 << 20
