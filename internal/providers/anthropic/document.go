package anthropic

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/deLiseLINO/prism/internal/provider"
)

// streamDocument serves gateways that ignore stream:true and answer with one
// message document. It replays the document as the frames a stream would carry
// so block, usage and stop handling stay in one place.
func (r *Runner) streamDocument(body io.Reader, sink provider.Sink, custom customTools) error {
	raw, err := io.ReadAll(io.LimitReader(body, 100<<20))
	if err != nil {
		return &provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassTransport, Cause: err}
	}
	doc, err := decodeStreamJSON(raw)
	if err != nil {
		return &provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassServer, Cause: fmt.Errorf("anthropic: malformed message document: %w", err)}
	}
	state := &streamState{custom: custom, sink: sink, store: r.state, log: r.log, blocks: make(map[string]*openBlock)}
	if errObj, ok := doc["error"].(map[string]any); ok {
		return state.upstreamErrorEvent(map[string]any{"error": errObj})
	}
	blocks, ok := doc["content"].([]any)
	if !ok {
		return &provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassTransport, Cause: errors.New("anthropic: message content must be an array")}
	}
	stop, ok := doc["stop_reason"].(string)
	if !ok || stop == "" {
		return &provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassTransport, Cause: errors.New("anthropic: message document lacks terminal stop_reason")}
	}
	frames := []map[string]any{{
		"type":    "message_start",
		"message": map[string]any{"id": doc["id"], "usage": doc["usage"]},
	}}
	for i, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok {
			return &provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassTransport, Cause: errors.New("anthropic: malformed message content block")}
		}
		frames = append(frames, blockFrames(i, block)...)
	}
	delta := map[string]any{"stop_reason": doc["stop_reason"]}
	frames = append(frames,
		map[string]any{"type": "message_delta", "delta": delta, "usage": doc["usage"]},
		map[string]any{"type": "message_stop"},
	)
	for _, frame := range frames {
		data, err := json.Marshal(frame)
		if err != nil {
			return &provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassServer, Cause: err}
		}
		if err := state.handle(sseFrame{data: data}); err != nil && !errors.Is(err, errTerminalDone) {
			return err
		}
	}
	if !state.terminal {
		return &provider.RunError{Kind: provider.TerminalOmitted, Class: provider.ClassTransport, Cause: errors.New("anthropic: message document produced no terminal")}
	}
	return nil
}

func blockFrames(index int, block map[string]any) []map[string]any {
	start := map[string]any{"type": "content_block_start", "index": index}
	var delta map[string]any
	switch block["type"] {
	case "text":
		start["content_block"] = map[string]any{"type": "text", "text": ""}
		delta = map[string]any{"type": "text_delta", "text": block["text"]}
	case "thinking":
		start["content_block"] = map[string]any{"type": "thinking", "thinking": ""}
		delta = map[string]any{"type": "thinking_delta", "thinking": block["thinking"]}
	case "tool_use":
		start["content_block"] = map[string]any{"type": "tool_use", "id": block["id"], "name": block["name"], "input": map[string]any{}}
		input, _ := json.Marshal(block["input"])
		delta = map[string]any{"type": "input_json_delta", "partial_json": string(input)}
	default:
		start["content_block"] = block
	}
	frames := []map[string]any{start}
	if delta != nil {
		frames = append(frames, map[string]any{"type": "content_block_delta", "index": index, "delta": delta})
	}
	if sig, ok := block["signature"].(string); ok && sig != "" && block["type"] == "thinking" {
		frames = append(frames, map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]any{"type": "signature_delta", "signature": sig}})
	}
	return append(frames, map[string]any{"type": "content_block_stop", "index": index})
}
