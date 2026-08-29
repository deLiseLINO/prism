package codex

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"prism/internal/canon"
)

type sseEnvelope struct {
	Type     string          `json:"type"`
	Response json.RawMessage `json:"response"`
}

type Decoder struct {
	sink     func(canon.Event) error
	warnings []string
	sawUsage canon.Usage
	done     bool
}

func NewDecoder(sink func(canon.Event) error) *Decoder {
	return &Decoder{sink: sink}
}

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
	case "response.created", "response.in_progress", "response.queued",
		"response.content_part.added", "response.content_part.done",
		"response.reasoning_summary_part.added", "response.reasoning_summary_part.done",
		"response.output_text.done", "response.function_call_arguments.done",
		"response.custom_tool_call_input.done", "response.reasoning_text.delta",
		"response.reasoning_text.done", "response.heartbeat":
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
		return d.failed(raw)
	default:
		d.warn("unknown_sse_type:" + raw.Type)
		return nil
	}
}

func (d *Decoder) outputItem(raw sseEnvelope, added bool) error {
	item, err := decodeItem(raw.Response)
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
		Usage *struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
			TotalTokens  int64 `json:"total_tokens"`
			InputDetails *struct {
				CachedTokens int64 `json:"cached_tokens"`
			} `json:"input_tokens_details"`
			OutputDetails *struct {
				ReasoningTokens int64 `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
	}
	if len(response) > 0 {
		if err := json.Unmarshal(response, &resp); err != nil {
			d.warn("malformed_terminal_response")
		}
	}
	usage := canon.Usage{}
	if resp.Usage != nil {
		usage = canon.Usage{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
			TotalTokens:  resp.Usage.TotalTokens,
		}
		if resp.Usage.InputDetails != nil {
			usage.CachedInputTokens = resp.Usage.InputDetails.CachedTokens
		}
		if resp.Usage.OutputDetails != nil {
			usage.ReasoningTokens = resp.Usage.OutputDetails.ReasoningTokens
		}
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

func (d *Decoder) failed(raw sseEnvelope) error {
	if d.done {
		d.warn("duplicate_terminal")
		return nil
	}
	d.done = true
	message := "upstream stream failed"
	var body map[string]any
	if err := json.Unmarshal(raw.Response, &body); err == nil {
		if errObj, ok := body["error"].(map[string]any); ok {
			if m, ok := errObj["message"].(string); ok && m != "" {
				message = m
			}
		}
	}
	return d.emit(canon.TurnFailed{Failure: canon.Failure{Reason: canon.FailUnknown, Message: message}})
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
		return canon.Message{ID: canon.ItemID(m.ID), Role: roleFromWire(m.Role), Content: content}, nil
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
	if strings.HasPrefix(encrypted, prismReasoningPrefix) {
		if decoded, ok := decodeReasoningEnvelope(encrypted); ok {
			return canon.OpaqueRef{Store: reasoningStorePRISMR1, Key: decoded}
		}
	}
	return canon.OpaqueRef{Store: reasoningStoreNative, Key: encrypted}
}

func decodeReasoningEnvelope(encrypted string) (string, bool) {
	payload, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(encrypted, prismReasoningPrefix))
	if err != nil {
		return "", false
	}
	var envelope struct {
		Sig *string  `json:"sig"`
		Red []string `json:"red"`
		Txt *string  `json:"txt"`
		Krc *string  `json:"krc"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return "", false
	}
	if envelope.Sig == nil && len(envelope.Red) == 0 && envelope.Txt == nil && envelope.Krc == nil {
		return "", false
	}
	return string(payload), true
}
