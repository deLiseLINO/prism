package codex

import (
	"bufio"
	"bytes"

	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
	"github.com/deLiseLINO/prism/internal/providers/openaierr"
	"github.com/deLiseLINO/prism/internal/providers/usagewire"
)

type sseEnvelope struct {
	Type     string          `json:"type"`
	Response json.RawMessage `json:"response"`
	Item     json.RawMessage `json:"item"`
	Message  string          `json:"message"`
	Code     string          `json:"code"`
}

type Decoder struct {
	sink     func(canon.Event) error
	progress func()
	warnings []string
	sawUsage canon.Usage
	done     bool
}

func NewDecoder(sink func(canon.Event) error) *Decoder {
	return &Decoder{sink: sink}
}

// OnProgress sets a callback for nonempty raw reasoning text, which has no
// canon event.
func (d *Decoder) OnProgress(fn func()) { d.progress = fn }

func (d *Decoder) Warnings() []string {
	return d.warnings
}

func (d *Decoder) Usage() canon.Usage {
	return d.sawUsage
}

func (d *Decoder) Done() bool {
	return d.done
}

func (d *Decoder) Decode(r io.Reader) error {
	br := bufio.NewReader(r)
	var frame []byte
	flush := func() error {
		if len(frame) == 0 {
			return nil
		}
		err := d.frame(frame)
		frame = frame[:0]
		return err
	}
	for {
		line, err := br.ReadBytes('\n')
		trimmed := bytes.TrimRight(line, "\r\n")
		if len(trimmed) == 0 {
			if fErr := flush(); fErr != nil {
				return fErr
			}
		} else if bytes.HasPrefix(trimmed, []byte("data:")) {
			payload := bytes.TrimLeft(trimmed[len("data:"):], " ")
			frame = append(frame, payload...)
			frame = append(frame, '\n')
		}
		if err != nil {
			if err == io.EOF {
				return flush()
			}
			return fmt.Errorf("codex sse: %w", err)
		}
	}
}

func (d *Decoder) frame(data []byte) error {
	payload := strings.TrimSpace(string(data))
	if payload == "" || payload == "[DONE]" {
		return nil
	}
	var raw sseEnvelope
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		d.warn("malformed_sse_payload")
		return nil
	}
	switch raw.Type {
	case "response.created", "response.in_progress", "response.queued":
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw.Response, &fields); err != nil {
			return d.protocolError(err)
		}
		usage, err := decodeUsage(fields["usage"], d.sawUsage)
		if err != nil {
			return d.protocolError(err)
		}
		d.sawUsage = usage
		return nil
	case "response.content_part.added", "response.content_part.done",
		"response.reasoning_summary_part.added", "response.reasoning_summary_part.done",
		"response.output_text.done", "response.function_call_arguments.done",
		"response.custom_tool_call_input.done",
		"response.reasoning_text.done", "response.heartbeat":
		return nil
	case "response.reasoning_text.delta":
		var e struct {
			Delta string `json:"delta"`
		}
		if json.Unmarshal([]byte(payload), &e) == nil && e.Delta != "" && d.progress != nil {
			d.progress()
		}
		return nil
	case "response.output_item.added":
		return d.outputItem(raw, true)
	case "response.output_item.done":
		return d.outputItem(raw, false)
	case "response.output_text.delta":
		var e struct {
			ItemID string `json:"item_id"`
			Delta  string `json:"delta"`
		}
		if err := json.Unmarshal([]byte(payload), &e); err != nil {
			d.warn("malformed_text_delta")
			return nil
		}
		return d.emit(canon.TextDelta{ItemID: canon.ItemID(e.ItemID), Text: e.Delta})
	case "response.reasoning_summary_text.delta":
		var e struct {
			ItemID string `json:"item_id"`
			Delta  string `json:"delta"`
		}
		if err := json.Unmarshal([]byte(payload), &e); err != nil {
			d.warn("malformed_reasoning_delta")
			return nil
		}
		return d.emit(canon.ReasoningDelta{ItemID: canon.ItemID(e.ItemID), Text: e.Delta})
	case "response.function_call_arguments.delta":
		var e struct {
			ItemID string `json:"item_id"`
			Delta  string `json:"delta"`
		}
		if err := json.Unmarshal([]byte(payload), &e); err != nil {
			d.warn("malformed_arguments_delta")
			return nil
		}
		return d.emit(canon.ToolArgumentsDelta{ItemID: canon.ItemID(e.ItemID), Bytes: []byte(e.Delta)})
	case "response.custom_tool_call_input.delta":
		var e struct {
			ItemID string `json:"item_id"`
			Delta  string `json:"delta"`
		}
		if err := json.Unmarshal([]byte(payload), &e); err != nil {
			d.warn("malformed_custom_input_delta")
			return nil
		}
		return d.emit(canon.CustomToolInputDelta{ItemID: canon.ItemID(e.ItemID), Text: e.Delta})
	case "response.completed":
		return d.terminal(raw.Response, true)
	case "response.incomplete":
		return d.terminal(raw.Response, false)
	case "response.failed", "error":
		return d.failed([]byte(payload))
	default:
		d.warn("unknown_sse_type:" + raw.Type)
		return nil
	}
}

func (d *Decoder) outputItem(raw sseEnvelope, added bool) error {
	item, err := decodeItem(raw.Item)
	if err != nil {
		d.warn("malformed_output_item")
		return nil
	}
	if item == nil {
		return nil
	}
	if added {
		return d.emit(canon.ItemStarted{Item: item})
	}
	if r, ok := item.(canon.ReasoningItem); ok && !r.State.IsEmpty() {
		if err := d.emit(canon.ItemStateAvailable{ItemID: r.ID, State: r.State}); err != nil {
			return err
		}
	}
	return d.emit(canon.ItemFinished{Item: item})
}

func (d *Decoder) terminal(response json.RawMessage, completed bool) error {
	if d.done {
		d.warn("duplicate_terminal")
		return nil
	}
	d.done = true
	var resp struct {
		Usage json.RawMessage `json:"usage"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
	}
	if len(response) > 0 {
		if err := json.Unmarshal(response, &resp); err != nil {
			d.warn("malformed_terminal_response")
		}
	}
	usage, err := decodeUsage(resp.Usage, d.sawUsage)
	if err != nil {
		return d.protocolError(err)
	}
	d.sawUsage = usage
	if completed {
		return d.emit(canon.TurnFinished{Status: canon.Completed(), Usage: usage})
	}
	reason := canon.IncompleteUpstreamStall
	if resp.IncompleteDetails != nil {
		switch resp.IncompleteDetails.Reason {
		case "max_output_tokens":
			reason = canon.IncompleteMaxOutputTokens
		case "content_filter":
			reason = canon.IncompleteContentFilter
		default:
			d.warn("unknown_incomplete_reason:" + resp.IncompleteDetails.Reason)
		}
	}
	return d.emit(canon.TurnFinished{Status: canon.Incomplete(reason), Usage: usage})
}

func (d *Decoder) failed(payload []byte) error {
	if d.done {
		d.warn("duplicate_terminal")
		return nil
	}
	parsed, ok := openaierr.Parse(payload)
	if !ok {
		d.warn("failed_without_error")
		return nil
	}
	d.done = true
	copied := parsed
	return d.emit(canon.TurnFailed{Failure: canon.Failure{Reason: canon.FailUnknown, Message: openaierr.Text(parsed), Provider: &copied}, Usage: d.sawUsage})
}

func (d *Decoder) emit(ev canon.Event) error {
	if err := d.sink(ev); err != nil {
		return fmt.Errorf("codex sse: sink: %w", err)
	}
	return nil
}

func (d *Decoder) warn(code string) {
	d.warnings = append(d.warnings, code)
}

func decodeItem(raw json.RawMessage) (canon.Item, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, err
	}
	switch probe.Type {
	case "message":
		var m struct {
			ID      string `json:"id"`
			Role    string `json:"role"`
			Phase   string `json:"phase"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		var content []canon.Content
		for _, part := range m.Content {
			if part.Type == "output_text" {
				content = append(content, canon.TextContent{Text: part.Text})
			}
		}
		return canon.Message{ID: canon.ItemID(m.ID), Role: roleFromWire(m.Role), Phase: canon.ParseMessagePhase(m.Phase), Content: content}, nil
	case "reasoning":
		var r struct {
			ID      string `json:"id"`
			Summary []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"summary"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			EncryptedContent string `json:"encrypted_content"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		item := canon.ReasoningItem{ID: canon.ItemID(r.ID)}
		for _, s := range r.Summary {
			item.Summary = append(item.Summary, canon.TextContent{Text: s.Text})
		}
		for _, c := range r.Content {
			if c.Type == "reasoning_text" {
				item.Content = c.Text
			}
		}
		if r.EncryptedContent != "" {
			item.State = reasoningState(r.EncryptedContent)
		}
		return item, nil
	case "function_call":
		var f struct {
			ID        string `json:"id"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, err
		}
		return canon.FunctionCall{
			ID: canon.ItemID(f.ID), CallID: canon.CallID(f.CallID),
			Name: canon.ToolName(f.Name), Arguments: []byte(f.Arguments),
		}, nil
	case "custom_tool_call":
		var f struct {
			ID     string `json:"id"`
			CallID string `json:"call_id"`
			Name   string `json:"name"`
			Input  string `json:"input"`
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, err
		}
		return canon.CustomToolCall{
			ID: canon.ItemID(f.ID), CallID: canon.CallID(f.CallID),
			Name: canon.ToolName(f.Name), Input: f.Input,
		}, nil
	case "local_shell_call":
		var f struct {
			ID     string `json:"id"`
			CallID string `json:"call_id"`
			Action *struct {
				Command []string `json:"command"`
			} `json:"action"`
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, err
		}
		command := ""
		if f.Action != nil {
			command = strings.Join(f.Action.Command, " ")
		}
		return canon.LocalShellCall{ID: canon.ItemID(f.ID), CallID: canon.CallID(f.CallID), Command: command}, nil
	default:
		return nil, nil
	}
}

func roleFromWire(r string) canon.Role {
	switch r {
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
