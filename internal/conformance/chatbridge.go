package conformance

func BridgeChatSse(events []NormalizedEvent) []NormalizedEvent {
	br := &chatBridge{}
	br.emit("response.created", map[string]any{
		"response": map[string]any{
			"id": "resp_bridge", "object": "response", "created_at": int64(0),
			"status": "in_progress", "model": "fixture-model", "output": []any{}, "usage": nil,
		},
	})
	br.emit("response.in_progress", map[string]any{
		"response": map[string]any{
			"id": "resp_bridge", "object": "response", "created_at": int64(0),
			"status": "in_progress", "model": "fixture-model", "output": []any{}, "usage": nil,
		},
	})
	for _, ev := range events {
		switch ev.Event {
		case "message":
			br.message(ev.Data)
		case "[DONE]":
			br.terminate()
		default:
			br.emit(ev.Event, ev.Data)
		}
	}
	if !br.terminated {
		br.emit("response.incomplete", map[string]any{
			"response": map[string]any{
				"id": "resp_bridge", "object": "response", "created_at": int64(0),
				"status": "incomplete", "model": "fixture-model", "output": br.finished,
				"incomplete_details": map[string]any{"reason": "adapter_eof"},
			},
		})
	}
	return br.out
}

type chatBridge struct {
	out        []NormalizedEvent
	msg        *bridgeMsg
	tools      []bridgeTool
	outputIdx  int
	finished   []any
	finishSeen string
	terminated bool
}

type bridgeMsg struct {
	itemID string
	text   string
	phase  string
}

type bridgeTool struct {
	index int
	id    string
	name  string
	args  string
}

func (b *chatBridge) emit(name string, data any) {
	b.out = append(b.out, NormalizedEvent{Event: name, Data: data, Ordinal: len(b.out)})
}

func (b *chatBridge) message(data any) {
	root, ok := data.(map[string]any)
	if !ok {
		return
	}
	choices, _ := root["choices"].([]any)
	for _, cv := range choices {
		choice, ok := cv.(map[string]any)
		if !ok {
			continue
		}
		if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
			b.finishSeen = fr
		}
		delta, ok := choice["delta"].(map[string]any)
		if !ok {
			continue
		}
		if content, ok := delta["content"].(string); ok && content != "" {
			b.textDelta(content)
		}
		if calls, ok := delta["tool_calls"].([]any); ok {
			for _, cv := range calls {
				call, ok := cv.(map[string]any)
				if !ok {
					continue
				}
				index := -1
				if idx, ok := call["index"].(float64); ok {
					index = int(idx)
				}
				id, _ := call["id"].(string)
				fn, _ := call["function"].(map[string]any)
				name, _ := fn["name"].(string)
				args, _ := fn["arguments"].(string)
				slot := -1
				for i := range b.tools {
					if b.tools[i].index == index && index >= 0 {
						slot = i
						break
					}
				}
				if slot < 0 {
					b.tools = append(b.tools, bridgeTool{index: index, id: id, name: name, args: args})
					continue
				}
				if id != "" && b.tools[slot].id == "" {
					b.tools[slot].id = id
				}
				if name != "" && b.tools[slot].name == "" {
					b.tools[slot].name = name
				}
				b.tools[slot].args += args
			}
		}
	}
	if b.finishSeen != "" {
		b.flushTools()
	}
}

func (b *chatBridge) textDelta(text string) {
	if b.msg == nil {
		itemID := "msg_1"
		item := map[string]any{
			"type": "message", "id": itemID, "status": "in_progress",
			"role": "assistant", "content": []any{},
		}
		b.emit("response.output_item.added", map[string]any{"output_index": b.outputIdx, "item": item})
		b.emit("response.content_part.added", map[string]any{
			"item_id": itemID, "output_index": b.outputIdx, "content_index": 0,
			"part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
		})
		b.msg = &bridgeMsg{itemID: itemID}
	}
	b.msg.text += text
	b.emit("response.output_text.delta", map[string]any{
		"item_id": b.msg.itemID, "output_index": b.outputIdx, "content_index": 0, "delta": text,
	})
}

func (b *chatBridge) closeMsg(phase string) {
	if b.msg == nil {
		return
	}
	itemID := b.msg.itemID
	text := b.msg.text
	b.emit("response.output_text.done", map[string]any{
		"item_id": itemID, "output_index": b.outputIdx, "content_index": 0, "text": text,
	})
	b.emit("response.content_part.done", map[string]any{
		"item_id": itemID, "output_index": b.outputIdx, "content_index": 0,
		"part": map[string]any{"type": "output_text", "text": text, "annotations": []any{}},
	})
	item := map[string]any{
		"type": "message", "id": itemID, "status": "completed", "role": "assistant",
		"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}},
	}
	if phase != "" {
		item["phase"] = phase
	}
	b.emit("response.output_item.done", map[string]any{"output_index": b.outputIdx, "item": item})
	b.finished = append(b.finished, item)
	b.outputIdx++
	b.msg = nil
}

func (b *chatBridge) flushTools() {
	if len(b.tools) == 0 {
		return
	}
	for i := range b.tools {
		t := b.tools[i]
		itemID := "fc_" + itoa(i+1)
		open := map[string]any{
			"type": "function_call", "id": itemID, "call_id": t.id,
			"name": t.name, "arguments": "", "status": "in_progress",
		}
		b.emit("response.output_item.added", map[string]any{"output_index": b.outputIdx, "item": open})
		b.emit("response.function_call_arguments.delta", map[string]any{
			"item_id": itemID, "output_index": b.outputIdx, "delta": t.args,
		})
		b.emit("response.function_call_arguments.done", map[string]any{
			"item_id": itemID, "output_index": b.outputIdx, "arguments": t.args,
		})
		final := map[string]any{
			"type": "function_call", "id": itemID, "call_id": t.id,
			"name": t.name, "arguments": t.args, "status": "completed",
		}
		b.emit("response.output_item.done", map[string]any{"output_index": b.outputIdx, "item": final})
		b.finished = append(b.finished, final)
		b.outputIdx++
	}
	b.tools = nil
}

func (b *chatBridge) terminate() {
	if b.terminated {
		return
	}
	b.terminated = true
	b.flushTools()
	phase := "final_answer"
	if b.finishSeen == "length" || b.finishSeen == "content_filter" {
		phase = ""
	}
	b.closeMsg(phase)
	b.emit("response.completed", map[string]any{
		"response": map[string]any{
			"id": "resp_bridge", "object": "response", "created_at": int64(0),
			"status": "completed", "model": "fixture-model", "output": b.finished,
			"usage": map[string]any{
				"input_tokens": 0, "output_tokens": 0, "total_tokens": 0,
				"input_tokens_details":  map[string]any{"cached_tokens": 0},
				"output_tokens_details": map[string]any{"reasoning_tokens": 0},
			},
		},
	})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
