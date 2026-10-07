package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strings"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
	"github.com/deLiseLINO/prism/internal/providers/openaierr"
	"github.com/deLiseLINO/prism/internal/providers/usagewire"
)

type sseEnvelope struct {
	Type        string          `json:"type"`
	Response    json.RawMessage `json:"response"`
	Item        json.RawMessage `json:"item"`
	OutputIndex *int            `json:"output_index"`
}
type streamedItem struct {
	index       int
	item        canon.Item
	finished    bool
	started     bool
	pending     []canon.Event
	arguments   *string
	customInput *string
	text        *string
	prefix      strings.Builder
}
type Decoder struct {
	sink      func(canon.Event) error
	progress  func()
	warnings  []string
	sawUsage  canon.Usage
	done      bool
	items     map[canon.ItemID]*streamedItem
	nextIndex int
}

func NewDecoder(sink func(canon.Event) error) *Decoder {
	return &Decoder{sink: sink, items: make(map[canon.ItemID]*streamedItem)}
}
func (d *Decoder) OnProgress(fn func()) { d.progress = fn }
func (d *Decoder) Warnings() []string   { return d.warnings }
func (d *Decoder) Usage() canon.Usage   { return d.sawUsage }
func (d *Decoder) Done() bool           { return d.done }

func (d *Decoder) Decode(r io.Reader) error {
	br := bufio.NewReader(r)
	var frame []byte
	for {
		line, err := br.ReadBytes('\n')
		trimmed := bytes.TrimRight(line, "\r\n")
		if len(trimmed) == 0 && len(frame) > 0 {
			if e := d.frame(frame); e != nil {
				return e
			}
			frame = frame[:0]
			if d.done {
				return nil
			}
		} else if bytes.HasPrefix(trimmed, []byte("data:")) {
			frame = append(frame, bytes.TrimPrefix(trimmed[5:], []byte(" "))...)
			frame = append(frame, '\n')
		}
		if err != nil {
			if err != io.EOF {
				return d.protocolError(fmt.Errorf("stream read: %w", err))
			}
			if len(frame) != 0 {
				return d.protocolError(errors.New("truncated SSE event"))
			}
			return nil
		}
	}
}

func (d *Decoder) frame(data []byte) error {
	payload := bytes.TrimSpace(data)
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return nil
	}
	var raw sseEnvelope
	if json.Unmarshal(payload, &raw) != nil {
		return d.protocolError(errors.New("malformed SSE JSON"))
	}
	switch raw.Type {
	case "response.output_item.added", "response.output_item.done":
		return d.outputItem(raw.Item, raw.Type == "response.output_item.added", raw.OutputIndex)
	case "response.output_text.done", "response.refusal.done", "response.function_call_arguments.done", "response.custom_tool_call_input.done":
		var e struct {
			ItemID    string  `json:"item_id"`
			Arguments *string `json:"arguments"`
			Input     *string `json:"input"`
			Text      *string `json:"text"`
			Refusal   *string `json:"refusal"`
		}
		if json.Unmarshal(payload, &e) != nil || e.ItemID == "" {
			return d.protocolError(errors.New("malformed final receipt"))
		}
		old := d.items[canon.ItemID(e.ItemID)]
		if old == nil || old.finished {
			return d.protocolError(errors.New("final receipt lacks an open output item"))
		}
		switch raw.Type {
		case "response.function_call_arguments.done":
			if _, ok := old.item.(canon.FunctionCall); !ok || e.Arguments == nil || !json.Valid([]byte(*e.Arguments)) {
				return d.protocolError(errors.New("invalid final tool arguments"))
			}
			if err := old.checkReceipt(old.arguments, e.Arguments); err != nil {
				return d.protocolError(err)
			}
			old.arguments = e.Arguments
		case "response.custom_tool_call_input.done":
			if _, ok := old.item.(canon.CustomToolCall); !ok || e.Input == nil {
				return d.protocolError(errors.New("invalid final custom input"))
			}
			if err := old.checkReceipt(old.customInput, e.Input); err != nil {
				return d.protocolError(err)
			}
			old.customInput = e.Input
		case "response.output_text.done", "response.refusal.done":
			text := e.Text
			if raw.Type == "response.refusal.done" {
				text = e.Refusal
			}
			if _, ok := old.item.(canon.Message); !ok || text == nil {
				return d.protocolError(errors.New("invalid final text"))
			}
			if err := old.checkReceipt(old.text, text); err != nil {
				return d.protocolError(err)
			}
			old.text = text
		}
		return nil
	case "response.completed", "response.incomplete":
		return d.terminal(raw.Response, raw.Type == "response.completed")
	case "response.failed", "error":
		return d.failed(payload, raw.Response)
	case "response.output_text.delta", "response.refusal.delta", "response.reasoning_summary_text.delta", "response.function_call_arguments.delta", "response.custom_tool_call_input.delta", "response.reasoning_text.delta":
		var e struct {
			ItemID string  `json:"item_id"`
			Delta  *string `json:"delta"`
		}
		if json.Unmarshal(payload, &e) != nil || e.Delta == nil {
			return d.protocolError(errors.New("malformed delta"))
		}
		if raw.Type == "response.reasoning_text.delta" {
			if *e.Delta != "" && d.progress != nil {
				d.progress()
			}
			return nil
		}
		if e.ItemID == "" {
			return d.protocolError(errors.New("delta missing item id"))
		}
		id := canon.ItemID(e.ItemID)
		switch raw.Type {
		case "response.output_text.delta", "response.refusal.delta":
			return d.itemEvent(id, canon.TextDelta{ItemID: id, Text: *e.Delta})
		case "response.reasoning_summary_text.delta":
			return d.itemEvent(id, canon.ReasoningDelta{ItemID: id, Text: *e.Delta})
		case "response.function_call_arguments.delta":
			return d.itemEvent(id, canon.ToolArgumentsDelta{ItemID: id, Bytes: []byte(*e.Delta)})
		default:
			return d.itemEvent(id, canon.CustomToolInputDelta{ItemID: id, Text: *e.Delta})
		}
	case "response.created", "response.in_progress", "response.queued":
		if len(raw.Response) == 0 {
			return nil
		}
		fields, err := wireObject(raw.Response)
		if err != nil {
			return d.protocolError(err)
		}
		usage, err := decodeUsage(fields["usage"], d.sawUsage)
		if err != nil {
			return d.protocolError(err)
		}
		d.sawUsage = usage
		return nil
	case "response.content_part.added", "response.content_part.done", "response.reasoning_summary_part.added", "response.reasoning_summary_part.done", "response.reasoning_text.done", "response.heartbeat":
		return nil
	default:
		d.warnings = append(d.warnings, "unknown_sse_type:"+raw.Type)
		return nil
	}
}

func (d *Decoder) outputItem(raw json.RawMessage, added bool, assigned *int) error {
	item, err := decodeItem(raw)
	if err != nil {
		return d.protocolError(err)
	}
	if item == nil {
		return d.protocolError(errors.New("unsupported output item"))
	}
	id := outputItemID(item)
	if id == "" {
		return d.protocolError(errors.New("output item missing id"))
	}
	old := d.items[id]
	if added {
		if old != nil {
			return d.protocolError(errors.New("duplicate output item id"))
		}
		index := len(d.items)
		if assigned != nil {
			index = *assigned
		}
		if index < 0 {
			return d.protocolError(errors.New("negative output index"))
		}
		for _, entry := range d.items {
			if entry.index == index {
				return d.protocolError(errors.New("duplicate output index"))
			}
		}
		d.items[id] = &streamedItem{index: index, item: item}
		return d.startPending()
	}
	if old != nil && (old.finished || reflect.TypeOf(old.item) != reflect.TypeOf(item)) {
		return d.protocolError(errors.New("conflicting output item done"))
	}
	if old != nil && assigned != nil && old.index != *assigned {
		return d.protocolError(errors.New("conflicting output index"))
	}
	if err := validateCustomInput(raw, old); err != nil {
		return d.protocolError(err)
	}
	if old != nil {
		item, err = old.finalItem(item)
		if err != nil {
			return d.protocolError(err)
		}
	}
	if call, ok := item.(canon.FunctionCall); ok && !json.Valid(call.Arguments) {
		return d.protocolError(errors.New("unfinished tool arguments"))
	}
	if old == nil {
		index := len(d.items)
		if assigned != nil {
			index = *assigned
		}
		old = &streamedItem{index: index, item: item}
		d.items[id] = old
	}
	old.item, old.finished = item, true
	if err := d.startPending(); err != nil {
		return err
	}
	return d.finishItem(item)
}

func (d *Decoder) finishItem(item canon.Item) error {
	if r, ok := item.(canon.ReasoningItem); ok && !r.State.IsEmpty() {
		if err := d.itemEvent(r.ID, canon.ItemStateAvailable{ItemID: r.ID, State: r.State}); err != nil {
			return err
		}
	}
	return d.itemEvent(outputItemID(item), canon.ItemFinished{Item: item})
}

func (it *streamedItem) finalItem(item canon.Item) (canon.Item, error) {
	switch value := item.(type) {
	case canon.FunctionCall:
		if it.arguments != nil && len(value.Arguments) > 0 && string(value.Arguments) != *it.arguments {
			return nil, errors.New("final arguments conflict with receipt")
		}
	case canon.CustomToolCall:
		if it.customInput != nil && value.Input != "" && value.Input != *it.customInput {
			return nil, errors.New("final input conflicts with receipt")
		}
	case canon.Message:
		if it.text != nil {
			var text strings.Builder
			for _, part := range value.Content {
				if part, ok := part.(canon.TextContent); ok {
					text.WriteString(part.Text)
				}
			}
			if text.Len() > 0 && text.String() != *it.text {
				return nil, errors.New("final text conflicts with receipt")
			}
		}
	}
	item = it.withReceipts(item)
	if err := it.reconcile(item); err != nil {
		return nil, err
	}
	return item, nil
}

func (it *streamedItem) withReceipts(item canon.Item) canon.Item {
	switch v := item.(type) {
	case canon.FunctionCall:
		if it.arguments != nil {
			v.Arguments = []byte(*it.arguments)
		}
		return v
	case canon.CustomToolCall:
		if it.customInput != nil {
			v.Input = *it.customInput
		}
		return v
	case canon.Message:
		if it.text != nil {
			v.Content = []canon.Content{canon.TextContent{Text: *it.text}}
		}
		return v
	}
	return item
}

func (d *Decoder) itemEvent(id canon.ItemID, ev canon.Event) error {
	it := d.items[id]
	if it == nil {
		return d.protocolError(errors.New("event lacks known output item"))
	}
	switch delta := ev.(type) {
	case canon.TextDelta:
		if _, ok := it.item.(canon.Message); !ok || it.finished {
			return d.protocolError(errors.New("text delta for closed or mismatched item"))
		}
		it.prefix.WriteString(delta.Text)
	case canon.ReasoningDelta:
		if _, ok := it.item.(canon.ReasoningItem); !ok || it.finished {
			return d.protocolError(errors.New("reasoning delta for closed or mismatched item"))
		}
		it.prefix.WriteString(delta.Text)
	case canon.ToolArgumentsDelta:
		if _, ok := it.item.(canon.FunctionCall); !ok || it.finished {
			return d.protocolError(errors.New("argument delta for closed or mismatched item"))
		}
		it.prefix.Write(delta.Bytes)
	case canon.CustomToolInputDelta:
		if _, ok := it.item.(canon.CustomToolCall); !ok || it.finished {
			return d.protocolError(errors.New("input delta for closed or mismatched item"))
		}
		it.prefix.WriteString(delta.Text)
	}
	if !it.started {
		it.pending = append(it.pending, ev)
		return nil
	}
	return d.emit(ev)
}

func (d *Decoder) startPending() error {
	for {
		var next *streamedItem
		for _, it := range d.items {
			if it.index == d.nextIndex {
				next = it
				break
			}
		}
		if next == nil {
			return nil
		}
		next.started = true
		d.nextIndex++
		if err := d.emit(canon.ItemStarted{Item: next.item}); err != nil {
			return err
		}
		for _, ev := range next.pending {
			if err := d.emit(ev); err != nil {
				return err
			}
		}
		next.pending = nil
	}
}

func (d *Decoder) terminal(raw json.RawMessage, completed bool) error {
	fields, err := wireObject(raw)
	if err != nil {
		return d.protocolError(fmt.Errorf("malformed terminal response: %w", err))
	}
	want := "completed"
	if !completed {
		want = "incomplete"
	}
	if status, supplied := fields["status"]; supplied {
		var value string
		if json.Unmarshal(status, &value) != nil || value != want {
			return d.protocolError(errors.New("terminal status conflicts with event"))
		}
	}
	usage, err := decodeUsage(fields["usage"], d.sawUsage)
	if err != nil {
		return d.protocolError(err)
	}
	if output, supplied := fields["output"]; supplied {
		var raws []json.RawMessage
		if json.Unmarshal(output, &raws) != nil || raws == nil {
			return d.protocolError(errors.New("terminal output must be an array"))
		}
		if len(raws) < len(d.items) {
			return d.protocolError(errors.New("terminal output omits emitted items"))
		}
		snapshot := make([]canon.Item, len(raws))
		seen := make(map[canon.ItemID]bool, len(raws))
		for i, raw := range raws {
			item, err := decodeItem(raw)
			if err != nil || item == nil {
				return d.protocolError(errors.New("malformed terminal output item"))
			}
			if old := d.items[outputItemID(item)]; old != nil && old.finished {
				if old.index != i || reflect.TypeOf(old.item) != reflect.TypeOf(item) || seen[outputItemID(item)] {
					return d.protocolError(errors.New("terminal output conflicts with emitted item"))
				}
				seen[outputItemID(item)] = true
				snapshot[i] = old.item
				continue
			}
			if err := validateCustomInput(raw, d.items[outputItemID(item)]); err != nil {
				return d.protocolError(err)
			}
			if old := d.items[outputItemID(item)]; old != nil {
				item, err = old.finalItem(item)
				if err != nil {
					return d.protocolError(err)
				}
			}
			id := outputItemID(item)
			if id == "" || seen[id] {
				return d.protocolError(errors.New("missing or duplicate terminal item id"))
			}
			seen[id] = true
			if call, ok := item.(canon.FunctionCall); ok && !json.Valid(call.Arguments) {
				return d.protocolError(errors.New("unfinished terminal tool arguments"))
			}
			if !completed {
				switch item.(type) {
				case canon.FunctionCall, canon.CustomToolCall, canon.LocalShellCall:
					f, _ := wireObject(raw)
					var status string
					if json.Unmarshal(f["status"], &status) != nil || status != "completed" {
						return d.protocolError(errors.New("incomplete terminal tool lacks completed status"))
					}
				}
			}
			if old := d.items[id]; old != nil {
				if old.index != i || reflect.TypeOf(old.item) != reflect.TypeOf(item) {
					return d.protocolError(errors.New("terminal output conflicts with emitted item"))
				}
				if err := old.reconcile(item); err != nil {
					return d.protocolError(err)
				}
			} else {
				for _, old := range d.items {
					if old.index == i {
						return d.protocolError(errors.New("terminal output conflicts with emitted order"))
					}
				}
			}
			snapshot[i] = item
		}
		for i, item := range snapshot {
			id := outputItemID(item)
			old := d.items[id]
			if old != nil && old.finished {
				continue
			}
			if old == nil {
				old = &streamedItem{index: i, item: item}
				d.items[id] = old
			}
			if err := d.startPending(); err != nil {
				return err
			}
			if err := d.finishItem(item); err != nil {
				return err
			}
			old.item, old.finished = item, true
		}
	} else {
		ordered := make([]*streamedItem, len(d.items))
		for _, item := range d.items {
			if item.index < 0 || item.index >= len(ordered) {
				return d.protocolError(errors.New("terminal output index gap"))
			}
			ordered[item.index] = item
		}
		for _, item := range ordered {
			if item == nil {
				return d.protocolError(errors.New("terminal output index gap"))
			}
			if item.finished {
				continue
			}
			if item.arguments == nil && item.customInput == nil && item.text == nil {
				return d.protocolError(errors.New("terminal lacks final output receipt"))
			}
			if _, err := item.finalItem(item.item); err != nil {
				return d.protocolError(err)
			}
		}
		for _, item := range ordered {
			if item.finished {
				continue
			}
			item.item = item.withReceipts(item.item)
			if err := d.finishItem(item.item); err != nil {
				return err
			}
			item.finished = true
		}
	}
	if d.nextIndex != len(d.items) {
		return d.protocolError(errors.New("terminal output index gap"))
	}
	d.sawUsage, d.done = usage, true
	if completed {
		return d.emit(canon.TurnFinished{Status: canon.Completed(), Usage: usage})
	}
	var details struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(fields["incomplete_details"], &details)
	reason := canon.IncompleteUpstreamStall
	switch details.Reason {
	case "max_output_tokens":
		reason = canon.IncompleteMaxOutputTokens
	case "content_filter":
		reason = canon.IncompleteContentFilter
	}
	return d.emit(canon.TurnFinished{Status: canon.Incomplete(reason), Usage: usage})
}

func (d *Decoder) failed(payload []byte, response json.RawMessage) error {
	if len(response) > 0 {
		fields, err := wireObject(response)
		if err != nil {
			return d.protocolError(err)
		}
		if status, supplied := fields["status"]; supplied {
			var value string
			if json.Unmarshal(status, &value) != nil || value != "failed" {
				return d.protocolError(errors.New("failed response status conflicts with event"))
			}
		}
		usage, err := decodeUsage(fields["usage"], d.sawUsage)
		if err != nil {
			return d.protocolError(err)
		}
		d.sawUsage = usage
	}
	parsed, ok := openaierr.Parse(payload)
	if !ok {
		return d.protocolError(errors.New("upstream failed without error details"))
	}
	d.done = true
	message := openaierr.Text(parsed)
	if message == "" {
		message = "codex: upstream stream error"
	}
	if err := d.emit(canon.TurnFailed{Failure: canon.Failure{Reason: canon.FailUnknown, Message: message, Provider: &parsed}, Usage: d.sawUsage}); err != nil {
		return err
	}
	return provider.RunError{Kind: provider.TerminalEmitted, Class: openaierr.ClassForInband(parsed, provider.ClassServer), Accepted: true, Cause: errors.New(message), Reported: &parsed}
}

func (d *Decoder) emit(ev canon.Event) error {
	if err := d.sink(ev); err != nil {
		return fmt.Errorf("codex sse: sink: %w", err)
	}
	return nil
}
func (d *Decoder) protocolError(cause error) error {
	cause = fmt.Errorf("codex sse: %w", cause)
	d.done = true
	if err := d.emit(canon.TurnFailed{Failure: canon.Failure{Reason: canon.FailUpstreamTransport, Message: cause.Error()}, Usage: d.sawUsage}); err != nil {
		return err
	}
	return provider.RunError{Kind: provider.TerminalEmitted, Class: provider.ClassTransport, Accepted: true, Cause: cause}
}
func wireObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, errors.New("expected object")
	}
	return fields, nil
}

func decodeUsage(raw json.RawMessage, previous canon.Usage) (canon.Usage, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return previous, nil
	}
	fields, err := wireObject(raw)
	if err != nil {
		return previous, fmt.Errorf("usage: %w", err)
	}
	next := previous
	for _, field := range []struct {
		name   string
		target *int64
	}{{"input_tokens", &next.InputTokens}, {"output_tokens", &next.OutputTokens}, {"total_tokens", &next.TotalTokens}} {
		if value, ok := fields[field.name]; ok {
			count, err := usagewire.Exact(string(bytes.TrimSpace(value)))
			if err != nil {
				return previous, fmt.Errorf("usage.%s: %w", field.name, err)
			}
			*field.target = count
		}
	}
	for _, detail := range []struct {
		name, count string
		target      *int64
	}{{"input_tokens_details", "cached_tokens", &next.CachedInputTokens}, {"output_tokens_details", "reasoning_tokens", &next.ReasoningTokens}} {
		if value, ok := fields[detail.name]; ok && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			f, err := wireObject(value)
			if err != nil {
				return previous, err
			}
			if value, ok := f[detail.count]; ok {
				count, err := usagewire.Exact(string(bytes.TrimSpace(value)))
				if err != nil {
					return previous, fmt.Errorf("usage.%s.%s: %w", detail.name, detail.count, err)
				}
				*detail.target = count
			}
		}
	}
	if next.InputTokens > math.MaxInt64-next.OutputTokens {
		return previous, errors.New("usage token sum exceeds int64")
	}
	if _, supplied := fields["total_tokens"]; !supplied {
		next.TotalTokens = next.InputTokens + next.OutputTokens
	}
	if next.TotalTokens != next.InputTokens+next.OutputTokens {
		return previous, errors.New("usage total does not equal input plus output")
	}
	if next.CachedInputTokens > next.InputTokens {
		return previous, errors.New("usage cached tokens exceed input")
	}
	if next.ReasoningTokens > next.OutputTokens {
		return previous, errors.New("usage reasoning tokens exceed output")
	}
	return next, nil
}

func outputItemID(item canon.Item) canon.ItemID {
	switch v := item.(type) {
	case canon.Message:
		return v.ID
	case canon.ReasoningItem:
		return v.ID
	case canon.FunctionCall:
		return v.ID
	case canon.CustomToolCall:
		return v.ID
	case canon.LocalShellCall:
		return v.ID
	}
	return ""
}
func decodeItem(raw json.RawMessage) (canon.Item, error) {
	fields, err := wireObject(raw)
	if err != nil {
		return nil, err
	}
	var v struct {
		Type      string `json:"type"`
		ID        string `json:"id"`
		Role      string `json:"role"`
		Phase     string `json:"phase"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
		Input     string `json:"input"`
		Encrypted string `json:"encrypted_content"`
		Content   []struct {
			Type    string  `json:"type"`
			Text    *string `json:"text"`
			Refusal *string `json:"refusal"`
		} `json:"content"`
		Summary []struct {
			Type string  `json:"type"`
			Text *string `json:"text"`
		} `json:"summary"`
		Action struct {
			Command []string `json:"command"`
		} `json:"action"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	if input, supplied := fields["input"]; supplied {
		var value *string
		if json.Unmarshal(input, &value) != nil || value == nil {
			return nil, errors.New("custom input must be a string")
		}
	}
	if v.Type == "function_call" || v.Type == "custom_tool_call" || v.Type == "local_shell_call" {
		if v.CallID == "" || v.Type != "local_shell_call" && v.Name == "" {
			return nil, errors.New("tool output lacks identity")
		}
	}
	for _, name := range []string{"content", "summary"} {
		if value, ok := fields[name]; ok && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, fmt.Errorf("%s must be an array", name)
		}
	}
	switch v.Type {
	case "message":
		var parts []canon.Content
		for _, p := range v.Content {
			var text *string
			switch p.Type {
			case "output_text":
				text = p.Text
			case "refusal":
				text = p.Refusal
			default:
				continue
			}
			if text == nil {
				return nil, errors.New("message text must be a string")
			}
			parts = append(parts, canon.TextContent{Text: *text})
		}
		return canon.Message{ID: canon.ItemID(v.ID), Role: roleFromWire(v.Role), Phase: canon.ParseMessagePhase(v.Phase), Content: parts}, nil
	case "reasoning":
		item := canon.ReasoningItem{ID: canon.ItemID(v.ID)}
		for _, p := range v.Content {
			if p.Type == "reasoning_text" {
				if p.Text == nil {
					return nil, errors.New("reasoning text must be a string")
				}
				item.Content += *p.Text
			}
		}
		for _, p := range v.Summary {
			if p.Text == nil {
				return nil, errors.New("reasoning summary must be a string")
			}
			item.Summary = append(item.Summary, canon.TextContent{Text: *p.Text})
		}
		if v.Encrypted != "" {
			item.State = reasoningState(v.Encrypted)
		}
		return item, nil
	case "function_call":
		return canon.FunctionCall{ID: canon.ItemID(v.ID), CallID: canon.CallID(v.CallID), Name: canon.ToolName(v.Name), Arguments: []byte(v.Arguments)}, nil
	case "custom_tool_call":
		return canon.CustomToolCall{ID: canon.ItemID(v.ID), CallID: canon.CallID(v.CallID), Name: canon.ToolName(v.Name), Input: v.Input}, nil
	case "local_shell_call":
		return canon.LocalShellCall{ID: canon.ItemID(v.ID), CallID: canon.CallID(v.CallID), Command: strings.Join(v.Action.Command, " ")}, nil
	default:
		return nil, nil
	}
}
func roleFromWire(role string) canon.Role {
	switch role {
	case "assistant":
		return canon.RoleAssistant
	case "system":
		return canon.RoleSystem
	case "developer":
		return canon.RoleDeveloper
	default:
		return canon.RoleUser
	}
}
func reasoningState(encrypted string) canon.OpaqueRef {
	return canon.OpaqueRef{Store: reasoningStoreNative, Key: encrypted}
}
