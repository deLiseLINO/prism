package conformance

import "strings"

func FinalizeChatObservation(o *Observation, events []NormalizedEvent, jsonBody any, status int) {
	FinalizeObservation(o, chatToResponseEvents(events, jsonBody), jsonBody, status)
}

func chatToResponseEvents(events []NormalizedEvent, jsonBody any) []NormalizedEvent {
	if jsonBody != nil {
		return chatNonstreamEvents(jsonBody)
	}
	return chatStreamEvents(events)
}

func chatNonstreamEvents(jsonBody any) []NormalizedEvent {
	root, ok := jsonBody.(map[string]any)
	if !ok {
		return []NormalizedEvent{{Event: "error", Data: "invalid chat response", Ordinal: 0}}
	}
	choices, _ := root["choices"].([]any)
	if len(choices) == 0 {
		return []NormalizedEvent{{Event: "error", Data: "chat response contained no choices", Ordinal: 0}}
	}
	choice, _ := choices[0].(map[string]any)
	if fr, ok := choice["finish_reason"].(string); ok && fr == "error" {
		return []NormalizedEvent{{Event: "error", Data: "chat response finished with error", Ordinal: 0}}
	}
	var out []NormalizedEvent
	message, _ := choice["message"].(map[string]any)
	if text, ok := message["content"].(string); ok && text != "" {
		out = append(out, textDeltaEvent(text, len(out)))
	}
	if calls, ok := message["tool_calls"].([]any); ok {
		for _, raw := range calls {
			tc, _ := raw.(map[string]any)
			fn, _ := tc["function"].(map[string]any)
			id, _ := tc["id"].(string)
			name, _ := fn["name"].(string)
			args, _ := fn["arguments"].(string)
			if id == "" || name == "" {
				continue
			}
			out = append(out, functionCallItemEvent(id, name, args, len(out)))
		}
	}
	out = append(out, completedEvent(len(out)))
	return out
}

type chatDeltaCall struct {
	index int
	id    string
	name  string
	args  string
}

func chatStreamEvents(events []NormalizedEvent) []NormalizedEvent {
	var text strings.Builder
	var calls []chatDeltaCall
	callIdx := map[int]int{}
	sawDone := false
	sawFinish := false
	for _, ev := range events {
		if ev.Event == "[DONE]" {
			sawDone = true
			continue
		}
		data, ok := ev.Data.(map[string]any)
		if !ok {
			continue
		}
		if _, isErr := data["error"]; isErr {
			return []NormalizedEvent{{Event: "error", Data: "chat stream reported an error", Ordinal: 0}}
		}
		choices, _ := data["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]any)
		if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
			sawFinish = true
		}
		delta, _ := choice["delta"].(map[string]any)
		if d, ok := delta["content"].(string); ok && d != "" {
			text.WriteString(d)
		}
		if rawCalls, ok := delta["tool_calls"].([]any); ok {
			for _, raw := range rawCalls {
				tc, _ := raw.(map[string]any)
				idx := -1
				if f, ok := tc["index"].(float64); ok {
					idx = int(f)
				}
				if idx < 0 {
					idx = len(calls)
				}
				pos, seen := callIdx[idx]
				if !seen {
					pos = len(calls)
					callIdx[idx] = pos
					calls = append(calls, chatDeltaCall{index: idx})
				}
				call := &calls[pos]
				if id, ok := tc["id"].(string); ok && id != "" && call.id == "" {
					call.id = id
				}
				if fn, ok := tc["function"].(map[string]any); ok {
					if name, ok := fn["name"].(string); ok && name != "" && call.name == "" {
						call.name = name
					}
					if args, ok := fn["arguments"].(string); ok {
						call.args += args
					}
				}
			}
		}
	}
	var out []NormalizedEvent
	if text.Len() > 0 {
		out = append(out, textDeltaEvent(text.String(), len(out)))
	}
	for _, call := range calls {
		out = append(out, functionCallItemEvent(call.id, call.name, call.args, len(out)))
	}
	if sawDone || sawFinish {
		out = append(out, completedEvent(len(out)))
	}
	return out
}

func textDeltaEvent(text string, ordinal int) NormalizedEvent {
	return NormalizedEvent{Event: "response.output_text.delta", Data: map[string]any{"delta": text}, Ordinal: ordinal}
}

func functionCallItemEvent(id, name, args string, ordinal int) NormalizedEvent {
	return NormalizedEvent{
		Event: "response.output_item.done",
		Data: map[string]any{
			"output_index": ordinal,
			"item": map[string]any{
				"type":      "function_call",
				"id":        id,
				"call_id":   id,
				"name":      name,
				"arguments": args,
				"status":    "completed",
			},
		},
		Ordinal: ordinal,
	}
}

func completedEvent(ordinal int) NormalizedEvent {
	return NormalizedEvent{
		Event:   "response.completed",
		Data:    map[string]any{"response": map[string]any{"status": "completed"}},
		Ordinal: ordinal,
	}
}
