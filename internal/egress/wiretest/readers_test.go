package wiretest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// outcome is what a protocol-strict client reconstructs from a response.
type outcome struct {
	Text      string
	Reasoning string
	Tools     []toolWant
	Stop      string
	Failed    bool
	Usage     usageSeen
}

type usageSeen struct {
	Present                      bool
	In, Out, Cached, Reason, Tot int64
}

type sseFrame struct {
	Event string
	Data  string
}

// parseSSE is a strict WHATWG-style reader: only event/data fields, blank-line
// terminated frames, no trailing partial frame, valid UTF-8.
func parseSSE(raw []byte) ([]sseFrame, error) {
	if !utf8.Valid(raw) {
		return nil, fmt.Errorf("stream is not valid UTF-8")
	}
	if len(raw) > 0 && !bytes.HasSuffix(raw, []byte("\n\n")) {
		return nil, fmt.Errorf("stream ends inside a frame: %q", tail(raw))
	}
	var out []sseFrame
	for _, block := range strings.Split(strings.TrimSuffix(string(raw), "\n\n"), "\n\n") {
		var f sseFrame
		var data []string
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				if f.Event != "" {
					return nil, fmt.Errorf("two event fields in one frame %q", block)
				}
				f.Event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = append(data, strings.TrimPrefix(line, "data: "))
			case strings.HasPrefix(line, ":"):
			default:
				return nil, fmt.Errorf("unexpected SSE line %q", line)
			}
		}
		f.Data = strings.Join(data, "\n")
		out = append(out, f)
	}
	return out, nil
}

func tail(b []byte) string {
	if len(b) > 60 {
		b = b[len(b)-60:]
	}
	return string(b)
}

func asMap(raw string) (map[string]any, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("bad JSON %q: %w", raw, err)
	}
	return m, nil
}

func num(v any) int64 {
	n, ok := v.(json.Number)
	if !ok {
		return -1
	}
	i, _ := n.Int64()
	return i
}

// ---- Responses ----

func readResponsesStream(raw []byte) (outcome, error) {
	frames, err := parseSSE(raw)
	if err != nil {
		return outcome{}, err
	}
	if len(frames) < 3 {
		return outcome{}, fmt.Errorf("too few frames: %d", len(frames))
	}
	last := frames[len(frames)-1]
	if last.Event != "" || last.Data != "[DONE]" {
		return outcome{}, fmt.Errorf("stream must end with bare data: [DONE], got %+v", last)
	}
	frames = frames[:len(frames)-1]
	type item struct {
		typ, id   string
		text, arg strings.Builder
		partOpen  bool
		done      bool
	}
	items := map[int]*item{}
	var out outcome
	var finalResp map[string]any
	seq := int64(-1)
	for i, f := range frames {
		m, err := asMap(f.Data)
		if err != nil {
			return outcome{}, err
		}
		typ, _ := m["type"].(string)
		if f.Event == "" || f.Event != typ {
			return outcome{}, fmt.Errorf("frame %d: event %q != data.type %q", i, f.Event, typ)
		}
		if s := num(m["sequence_number"]); s != seq+1 {
			return outcome{}, fmt.Errorf("frame %d (%s): sequence_number %d, want %d", i, typ, s, seq+1)
		} else {
			seq = s
		}
		if finalResp != nil {
			return outcome{}, fmt.Errorf("event %s after terminal", typ)
		}
		switch {
		case i == 0 && typ != "response.created":
			return outcome{}, fmt.Errorf("first event %s, want response.created", typ)
		case i == 1 && typ != "response.in_progress":
			return outcome{}, fmt.Errorf("second event %s, want response.in_progress", typ)
		}
		oi := int(num(m["output_index"]))
		switch typ {
		case "response.created", "response.in_progress":
			r, _ := m["response"].(map[string]any)
			if r["id"] == "" || r["object"] != "response" || r["status"] != "in_progress" {
				return outcome{}, fmt.Errorf("%s response = %v", typ, r)
			}
		case "response.output_item.added":
			it, _ := m["item"].(map[string]any)
			if _, dup := items[oi]; dup || oi != len(items) {
				return outcome{}, fmt.Errorf("output_item.added output_index %d (have %d)", oi, len(items))
			}
			id, _ := it["id"].(string)
			t, _ := it["type"].(string)
			if id == "" || it["status"] != "in_progress" {
				return outcome{}, fmt.Errorf("added item lacks id/status: %v", it)
			}
			items[oi] = &item{typ: t, id: id}
		case "response.content_part.added":
			it := items[oi]
			if it == nil || it.typ != "message" || m["item_id"] != it.id || num(m["content_index"]) != 0 {
				return outcome{}, fmt.Errorf("content_part.added out of place: %v", m)
			}
			it.partOpen = true
		case "response.output_text.delta":
			it := items[oi]
			if it == nil || it.typ != "message" || !it.partOpen || m["item_id"] != it.id {
				return outcome{}, fmt.Errorf("output_text.delta out of place: %v", m)
			}
			it.text.WriteString(m["delta"].(string))
		case "response.output_text.done":
			it := items[oi]
			if it == nil || m["text"] != it.text.String() || m["item_id"] != it.id {
				return outcome{}, fmt.Errorf("output_text.done mismatch: %v", m)
			}
		case "response.content_part.done":
			it := items[oi]
			p, _ := m["part"].(map[string]any)
			if it == nil || p["type"] != "output_text" || p["text"] != it.text.String() {
				return outcome{}, fmt.Errorf("content_part.done mismatch: %v", m)
			}
		case "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
			it := items[oi]
			if it == nil || it.typ != "reasoning" || m["item_id"] != it.id {
				return outcome{}, fmt.Errorf("reasoning delta out of place: %v", m)
			}
			it.text.WriteString(m["delta"].(string))
		case "response.reasoning_text.done":
			it := items[oi]
			if it == nil || m["text"] != it.text.String() {
				return outcome{}, fmt.Errorf("reasoning_text.done mismatch: %v", m)
			}
		case "response.function_call_arguments.delta":
			it := items[oi]
			if it == nil || it.typ != "function_call" || m["item_id"] != it.id {
				return outcome{}, fmt.Errorf("fn args delta out of place: %v", m)
			}
			it.arg.WriteString(m["delta"].(string))
		case "response.function_call_arguments.done":
			it := items[oi]
			if it == nil || m["arguments"] != it.arg.String() || m["item_id"] != it.id {
				return outcome{}, fmt.Errorf("fn args done mismatch: %v (streamed %q)", m, it.arg.String())
			}
		case "response.output_item.done":
			it := items[oi]
			d, _ := m["item"].(map[string]any)
			if it == nil || it.done || d["id"] != it.id || d["type"] != it.typ {
				return outcome{}, fmt.Errorf("output_item.done mismatch: %v", m)
			}
			if d["status"] != "completed" && d["status"] != "incomplete" {
				return outcome{}, fmt.Errorf("output_item.done status %v", d["status"])
			}
			it.done = true
		case "response.completed", "response.incomplete", "response.failed":
			finalResp, _ = m["response"].(map[string]any)
		case "response.heartbeat":
		default:
			return outcome{}, fmt.Errorf("unknown event %s", typ)
		}
	}
	if finalResp == nil {
		return outcome{}, fmt.Errorf("no terminal event")
	}
	out, err = outcomeFromResponsesObject(finalResp)
	if err != nil {
		return outcome{}, err
	}
	if out.Failed {
		return out, nil
	}
	for oi, it := range items {
		if !it.done {
			return outcome{}, fmt.Errorf("item %d (%s) never done on a successful terminal", oi, it.typ)
		}
	}
	streamed := ""
	for i := 0; i < len(items); i++ {
		if items[i].typ == "message" {
			streamed += items[i].text.String()
		}
	}
	if streamed != out.Text {
		return outcome{}, fmt.Errorf("streamed text %q != final response text %q", trunc(streamed), trunc(out.Text))
	}
	return out, nil
}

func trunc(s string) string {
	if len(s) > 80 {
		return s[:80] + "…"
	}
	return s
}

func readResponsesJSON(raw []byte) (outcome, error) {
	m, err := asMap(strings.TrimSpace(string(raw)))
	if err != nil {
		return outcome{}, err
	}
	return outcomeFromResponsesObject(m)
}

func outcomeFromResponsesObject(r map[string]any) (outcome, error) {
	var out outcome
	if r["object"] != "response" || r["id"] == "" || r["model"] == "" {
		return out, fmt.Errorf("response envelope: %v", r)
	}
	if _, ok := r["created_at"]; !ok {
		return out, fmt.Errorf("response has no created_at")
	}
	st, _ := r["status"].(string)
	switch st {
	case "completed":
		out.Stop = "end"
	case "incomplete":
		d, _ := r["incomplete_details"].(map[string]any)
		switch d["reason"] {
		case "max_output_tokens":
			out.Stop = "length"
		case "content_filter":
			out.Stop = "filter"
		default:
			out.Stop = "stall"
		}
	case "failed":
		e, _ := r["error"].(map[string]any)
		if e == nil || e["message"] == nil && e["code"] == nil {
			return out, fmt.Errorf("failed response has no error object: %v", r)
		}
		out.Failed = true
		return out, nil
	default:
		return out, fmt.Errorf("terminal status %q", st)
	}
	output, ok := r["output"].([]any)
	if !ok {
		return out, fmt.Errorf("output is not an array")
	}
	for _, o := range output {
		it := o.(map[string]any)
		switch it["type"] {
		case "message":
			for _, p := range it["content"].([]any) {
				pm := p.(map[string]any)
				if pm["type"] != "output_text" {
					return out, fmt.Errorf("content part %v", pm)
				}
				out.Text += pm["text"].(string)
			}
		case "reasoning":
			if _, ok := it["summary"].([]any); !ok {
				return out, fmt.Errorf("reasoning item without summary array: %v", it)
			}
			if c, ok := it["content"].([]any); ok {
				for _, p := range c {
					out.Reasoning += p.(map[string]any)["text"].(string)
				}
			}
		case "function_call":
			args, _ := it["arguments"].(string)
			if it["call_id"] == "" || it["name"] == "" {
				return out, fmt.Errorf("function_call lacks call_id/name: %v", it)
			}
			out.Tools = append(out.Tools, toolWant{it["call_id"].(string), it["name"].(string), args})
		default:
			return out, fmt.Errorf("unexpected output item %v", it["type"])
		}
	}
	if len(out.Tools) > 0 && out.Stop == "end" {
		out.Stop = "tool"
	}
	u, _ := r["usage"].(map[string]any)
	if u == nil {
		return out, fmt.Errorf("usage missing on terminal response")
	}
	in, _ := u["input_tokens_details"].(map[string]any)
	od, _ := u["output_tokens_details"].(map[string]any)
	out.Usage = usageSeen{true, num(u["input_tokens"]), num(u["output_tokens"]), num(in["cached_tokens"]), num(od["reasoning_tokens"]), num(u["total_tokens"])}
	return out, nil
}

// ---- Chat Completions ----

func readChatStream(raw []byte) (outcome, error) {
	frames, err := parseSSE(raw)
	if err != nil {
		return outcome{}, err
	}
	var out outcome
	if len(frames) == 0 {
		return out, fmt.Errorf("empty stream")
	}
	tools := map[int]*toolWant{}
	var order []int
	finished := false
	done := false
	for i, f := range frames {
		if f.Event != "" {
			return out, fmt.Errorf("chat stream frame %d carries an event name %q", i, f.Event)
		}
		if done {
			return out, fmt.Errorf("frame after [DONE]")
		}
		if f.Data == "[DONE]" {
			done = true
			continue
		}
		m, err := asMap(f.Data)
		if err != nil {
			return out, err
		}
		if e, ok := m["error"]; ok {
			if i != len(frames)-1 {
				return out, fmt.Errorf("error frame is not last")
			}
			em, _ := e.(map[string]any)
			if em == nil || em["message"] == nil {
				return out, fmt.Errorf("error frame has no message: %v", m)
			}
			out.Failed = true
			return out, nil
		}
		if finished {
			return out, fmt.Errorf("data frame after finish_reason")
		}
		if m["object"] != "chat.completion.chunk" || m["id"] == "" || m["model"] == "" {
			return out, fmt.Errorf("chunk envelope: %v", m)
		}
		if _, ok := m["created"]; !ok {
			return out, fmt.Errorf("chunk has no created")
		}
		ch := m["choices"].([]any)
		if len(ch) != 1 {
			return out, fmt.Errorf("choices = %d", len(ch))
		}
		c := ch[0].(map[string]any)
		d := c["delta"].(map[string]any)
		if i == 0 && d["role"] != "assistant" {
			return out, fmt.Errorf("first chunk must set role=assistant: %v", d)
		}
		if s, ok := d["content"].(string); ok {
			out.Text += s
		}
		if s, ok := d["reasoning_content"].(string); ok {
			out.Reasoning += s
		}
		if tc, ok := d["tool_calls"].([]any); ok {
			for _, t := range tc {
				tm := t.(map[string]any)
				idx := int(num(tm["index"]))
				fnm, _ := tm["function"].(map[string]any)
				cur := tools[idx]
				if cur == nil {
					if idx != len(order) {
						return out, fmt.Errorf("tool index %d out of order", idx)
					}
					id, _ := tm["id"].(string)
					name, _ := fnm["name"].(string)
					if id == "" || tm["type"] != "function" || name == "" {
						return out, fmt.Errorf("first tool delta lacks id/type/name: %v", tm)
					}
					cur = &toolWant{CallID: id, Name: name}
					tools[idx] = cur
					order = append(order, idx)
				} else if tm["id"] != nil || (fnm != nil && fnm["name"] != nil) {
					return out, fmt.Errorf("continuation tool delta repeats id/name: %v", tm)
				}
				if a, ok := fnm["arguments"].(string); ok {
					cur.Args += a
				}
			}
		}
		if fr, ok := c["finish_reason"].(string); ok {
			finished = true
			switch fr {
			case "stop":
				out.Stop = "end"
			case "tool_calls":
				out.Stop = "tool"
			case "length":
				out.Stop = "length"
			case "content_filter":
				out.Stop = "filter"
			default:
				return out, fmt.Errorf("finish_reason %q", fr)
			}
			if u, ok := m["usage"].(map[string]any); ok {
				out.Usage = chatUsage(u)
			}
		}
	}
	if !done {
		return out, fmt.Errorf("missing data: [DONE]")
	}
	if !finished {
		return out, fmt.Errorf("no finish_reason chunk")
	}
	for _, idx := range order {
		out.Tools = append(out.Tools, *tools[idx])
	}
	return out, nil
}

func chatUsage(u map[string]any) usageSeen {
	pd, _ := u["prompt_tokens_details"].(map[string]any)
	cd, _ := u["completion_tokens_details"].(map[string]any)
	return usageSeen{true, num(u["prompt_tokens"]), num(u["completion_tokens"]), num(pd["cached_tokens"]), num(cd["reasoning_tokens"]), num(u["total_tokens"])}
}

func readChatJSON(raw []byte) (outcome, error) {
	m, err := asMap(strings.TrimSpace(string(raw)))
	if err != nil {
		return outcome{}, err
	}
	var out outcome
	if e, ok := m["error"].(map[string]any); ok {
		if e["message"] == nil {
			return out, fmt.Errorf("error without message")
		}
		out.Failed = true
		return out, nil
	}
	if m["object"] != "chat.completion" || m["id"] == "" || m["model"] == "" {
		return out, fmt.Errorf("completion envelope: %v", m)
	}
	c := m["choices"].([]any)[0].(map[string]any)
	msg := c["message"].(map[string]any)
	if msg["role"] != "assistant" {
		return out, fmt.Errorf("role %v", msg["role"])
	}
	if s, ok := msg["content"].(string); ok {
		out.Text = s
	} else if msg["content"] != nil {
		return out, fmt.Errorf("content type %T", msg["content"])
	}
	if s, ok := msg["reasoning_content"].(string); ok {
		out.Reasoning = s
	}
	if tc, ok := msg["tool_calls"].([]any); ok {
		for _, t := range tc {
			tm := t.(map[string]any)
			f := tm["function"].(map[string]any)
			if tm["id"] == "" || tm["type"] != "function" {
				return out, fmt.Errorf("tool_call %v", tm)
			}
			out.Tools = append(out.Tools, toolWant{tm["id"].(string), f["name"].(string), f["arguments"].(string)})
		}
	}
	switch c["finish_reason"] {
	case "stop":
		out.Stop = "end"
	case "tool_calls":
		out.Stop = "tool"
	case "length":
		out.Stop = "length"
	case "content_filter":
		out.Stop = "filter"
	default:
		return out, fmt.Errorf("finish_reason %v", c["finish_reason"])
	}
	u, ok := m["usage"].(map[string]any)
	if !ok {
		return out, fmt.Errorf("usage missing")
	}
	out.Usage = chatUsage(u)
	return out, nil
}

// ---- Messages ----

func readMessagesStream(raw []byte) (outcome, error) {
	frames, err := parseSSE(raw)
	if err != nil {
		return outcome{}, err
	}
	var out outcome
	if len(frames) < 2 {
		return out, fmt.Errorf("too few frames")
	}
	type block struct {
		typ, text, sig string
		tool           toolWant
		open           bool
	}
	blocks := map[int]*block{}
	next := 0
	stopped := false
	sawDelta := false
	for i, f := range frames {
		if stopped {
			return out, fmt.Errorf("frame after message_stop")
		}
		m, err := asMap(f.Data)
		if err != nil {
			return out, err
		}
		typ, _ := m["type"].(string)
		if f.Event != typ {
			return out, fmt.Errorf("frame %d: event %q != data.type %q", i, f.Event, typ)
		}
		if i == 0 && typ != "message_start" {
			return out, fmt.Errorf("first event %s", typ)
		}
		idx := int(num(m["index"]))
		switch typ {
		case "message_start":
			msg := m["message"].(map[string]any)
			if msg["type"] != "message" || msg["role"] != "assistant" || msg["id"] == "" || msg["model"] == "" {
				return out, fmt.Errorf("message_start: %v", msg)
			}
			if c, ok := msg["content"].([]any); !ok || len(c) != 0 {
				return out, fmt.Errorf("message_start content must be []")
			}
			if u, ok := msg["usage"].(map[string]any); !ok || u["input_tokens"] == nil || u["output_tokens"] == nil {
				return out, fmt.Errorf("message_start usage incomplete: %v", msg["usage"])
			}
		case "ping":
		case "content_block_start":
			if idx != next {
				return out, fmt.Errorf("block index %d, want %d", idx, next)
			}
			cb := m["content_block"].(map[string]any)
			b := &block{typ: cb["type"].(string), open: true}
			switch b.typ {
			case "text":
				if _, ok := cb["text"].(string); !ok {
					return out, fmt.Errorf("text block without text")
				}
			case "thinking":
				if _, ok := cb["thinking"].(string); !ok {
					return out, fmt.Errorf("thinking block without thinking")
				}
			case "tool_use":
				if cb["id"] == "" || cb["name"] == "" {
					return out, fmt.Errorf("tool_use block lacks id/name: %v", cb)
				}
				b.tool = toolWant{CallID: cb["id"].(string), Name: cb["name"].(string)}
			case "redacted_thinking":
			default:
				return out, fmt.Errorf("block type %s", b.typ)
			}
			blocks[idx] = b
			next++
		case "content_block_delta":
			b := blocks[idx]
			if b == nil || !b.open {
				return out, fmt.Errorf("delta for closed/unknown block %d", idx)
			}
			d := m["delta"].(map[string]any)
			sawDelta = true
			switch d["type"] {
			case "text_delta":
				if b.typ != "text" {
					return out, fmt.Errorf("text_delta on %s", b.typ)
				}
				b.text += d["text"].(string)
			case "thinking_delta":
				if b.typ != "thinking" {
					return out, fmt.Errorf("thinking_delta on %s", b.typ)
				}
				b.text += d["thinking"].(string)
			case "signature_delta":
				if b.typ != "thinking" {
					return out, fmt.Errorf("signature_delta on %s", b.typ)
				}
				b.sig = d["signature"].(string)
			case "input_json_delta":
				if b.typ != "tool_use" {
					return out, fmt.Errorf("input_json_delta on %s", b.typ)
				}
				b.tool.Args += d["partial_json"].(string)
			default:
				return out, fmt.Errorf("delta type %v", d["type"])
			}
		case "content_block_stop":
			b := blocks[idx]
			if b == nil || !b.open {
				return out, fmt.Errorf("stop for closed/unknown block %d", idx)
			}
			b.open = false
		case "message_delta":
			for _, b := range blocks {
				if b.open {
					return out, fmt.Errorf("message_delta with open block")
				}
			}
			d := m["delta"].(map[string]any)
			switch d["stop_reason"] {
			case "end_turn", "stop_sequence":
				out.Stop = "end"
			case "tool_use":
				out.Stop = "tool"
			case "max_tokens":
				out.Stop = "length"
			case "refusal":
				out.Stop = "filter"
			default:
				return out, fmt.Errorf("stop_reason %v", d["stop_reason"])
			}
			u, _ := m["usage"].(map[string]any)
			if u == nil || u["output_tokens"] == nil {
				return out, fmt.Errorf("message_delta usage lacks output_tokens: %v", m)
			}
			out.Usage = usageSeen{true, num(u["input_tokens"]), num(u["output_tokens"]), num(u["cache_read_input_tokens"]), -1, -1}
		case "message_stop":
			if out.Stop == "" {
				return out, fmt.Errorf("message_stop before message_delta")
			}
			stopped = true
		case "error":
			if i != len(frames)-1 {
				return out, fmt.Errorf("error is not last")
			}
			e, _ := m["error"].(map[string]any)
			if e == nil || e["type"] == nil || e["message"] == nil {
				return out, fmt.Errorf("error event lacks type/message: %v", m)
			}
			out.Failed = true
			return out, nil
		default:
			return out, fmt.Errorf("unknown event %s", typ)
		}
	}
	_ = sawDelta
	if !stopped {
		return out, fmt.Errorf("stream ended without message_stop")
	}
	for i := 0; i < next; i++ {
		b := blocks[i]
		switch b.typ {
		case "text":
			out.Text += b.text
		case "thinking":
			out.Reasoning += b.text
			if b.sig == "" {
				return out, fmt.Errorf("thinking block %d closed without a signature_delta", i)
			}
		case "tool_use":
			out.Tools = append(out.Tools, b.tool)
		}
	}
	return out, nil
}

func readMessagesJSON(raw []byte) (outcome, error) {
	m, err := asMap(strings.TrimSpace(string(raw)))
	if err != nil {
		return outcome{}, err
	}
	var out outcome
	if m["type"] == "error" {
		e, _ := m["error"].(map[string]any)
		if e == nil || e["type"] == nil || e["message"] == nil {
			return out, fmt.Errorf("error lacks type/message: %v", m)
		}
		out.Failed = true
		return out, nil
	}
	if m["type"] != "message" || m["role"] != "assistant" || m["id"] == "" || m["model"] == "" {
		return out, fmt.Errorf("message envelope: %v", m)
	}
	content, ok := m["content"].([]any)
	if !ok {
		return out, fmt.Errorf("content is not an array")
	}
	for _, c := range content {
		cm := c.(map[string]any)
		switch cm["type"] {
		case "text":
			out.Text += cm["text"].(string)
		case "thinking":
			out.Reasoning += cm["thinking"].(string)
		case "tool_use":
			in, ok := cm["input"].(map[string]any)
			if !ok {
				return out, fmt.Errorf("tool_use input is not an object: %v", cm["input"])
			}
			b, _ := json.Marshal(in)
			out.Tools = append(out.Tools, toolWant{cm["id"].(string), cm["name"].(string), string(b)})
		case "redacted_thinking":
		default:
			return out, fmt.Errorf("block %v", cm["type"])
		}
	}
	switch m["stop_reason"] {
	case "end_turn":
		out.Stop = "end"
	case "tool_use":
		out.Stop = "tool"
	case "max_tokens":
		out.Stop = "length"
	default:
		return out, fmt.Errorf("stop_reason %v", m["stop_reason"])
	}
	u, _ := m["usage"].(map[string]any)
	if u == nil {
		return out, fmt.Errorf("usage missing")
	}
	out.Usage = usageSeen{true, num(u["input_tokens"]), num(u["output_tokens"]), num(u["cache_read_input_tokens"]), -1, -1}
	return out, nil
}
