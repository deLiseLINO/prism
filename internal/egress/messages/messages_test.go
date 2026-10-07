package messages

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
)

type sseFrame struct {
	name string
	data map[string]any
}

func parseFrames(t *testing.T, out string) []sseFrame {
	t.Helper()
	var frames []sseFrame
	for _, chunk := range strings.Split(out, "\n\n") {
		if chunk == "" {
			continue
		}
		var f sseFrame
		for _, line := range strings.Split(chunk, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				f.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &f.data); err != nil {
					t.Fatalf("decode data for %s: %v", f.name, err)
				}
			default:
				t.Fatalf("unexpected SSE line %q", line)
			}
		}
		if f.name == "" || f.data == nil {
			t.Fatalf("incomplete SSE frame %q", chunk)
		}
		frames = append(frames, f)
	}
	return frames
}

func frameNames(frames []sseFrame) []string {
	out := make([]string, len(frames))
	for i, f := range frames {
		out[i] = f.name
	}
	return out
}

func runStream(t *testing.T, h ResponseHeader, evs []canon.Event) (string, Egress) {
	t.Helper()
	var buf bytes.Buffer
	eg := New(&buf, true)
	if err := eg.Begin(h); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	for i, ev := range evs {
		if err := eg.Frame(ev); err != nil {
			t.Fatalf("Frame %d: %v", i, err)
		}
	}
	if err := eg.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	return buf.String(), eg
}

func TestStrictSequenceAndIndexing(t *testing.T) {
	out, _ := runStream(t,
		ResponseHeader{ID: "msg_1", Model: "claude-codex--gpt-5"},
		[]canon.Event{
			canon.ItemStarted{Item: canon.Message{ID: "t1", Role: canon.RoleAssistant, Content: []canon.Content{}}},
			canon.TextDelta{ItemID: "t1", Text: "Hello"},
			canon.TextDelta{ItemID: "t1", Text: " world"},
			canon.ItemFinished{Item: canon.Message{ID: "t1", Role: canon.RoleAssistant}},
			canon.ItemStarted{Item: canon.ReasoningItem{ID: "r1", Content: "hmm"}},
			canon.ReasoningDelta{ItemID: "r1", Text: "think"},
			canon.ReasoningDelta{ItemID: "r1", Text: "ing"},
			canon.ItemFinished{Item: canon.ReasoningItem{ID: "r1", Content: "thinking", Signature: "sig1"}},
			canon.ItemStarted{Item: canon.FunctionCall{ID: "f1", CallID: "call_1", Name: "get_weather"}},
			canon.ToolArgumentsDelta{ItemID: "f1", Bytes: []byte(`{"city"`)},
			canon.ToolArgumentsDelta{ItemID: "f1", Bytes: []byte(`: "SF"}`)},
			canon.ItemFinished{Item: canon.FunctionCall{ID: "f1", CallID: "call_1", Name: "get_weather", Arguments: []byte(`{"city": "SF"}`)}},
			canon.TurnFinished{Status: canon.Completed(), Usage: canon.Usage{InputTokens: 10, OutputTokens: 5}},
		})
	frames := parseFrames(t, out)
	got := frameNames(frames)
	want := []string{
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_delta",
		"content_block_stop",
		"content_block_start",
		"content_block_delta",
		"content_block_delta",
		"content_block_delta",
		"content_block_stop",
		"content_block_start",
		"content_block_delta",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("frame sequence = %v, want %v", got, want)
	}
	indexes := []float64{}
	for _, f := range frames {
		if strings.HasPrefix(f.name, "content_block_") {
			indexes = append(indexes, f.data["index"].(float64))
		}
	}
	wantIndexes := []float64{0, 0, 0, 0, 1, 1, 1, 1, 1, 2, 2, 2, 2}
	if len(indexes) != len(wantIndexes) {
		t.Fatalf("index count = %d, want %d", len(indexes), len(wantIndexes))
	}
	for i, v := range wantIndexes {
		if indexes[i] != v {
			t.Fatalf("index[%d] = %v, want %v", i, indexes[i], v)
		}
	}
}

func TestEventMapping(t *testing.T) {
	out, _ := runStream(t,
		ResponseHeader{ID: "msg_1", Model: "claude-antigravity--gemini-3-pro"},
		[]canon.Event{
			canon.ItemStarted{Item: canon.ReasoningItem{ID: "r1"}},
			canon.ReasoningDelta{ItemID: "r1", Text: "deep"},
			canon.ItemFinished{Item: canon.ReasoningItem{ID: "r1", Content: "deep", Signature: "sig9"}},
			canon.ItemStarted{Item: canon.Message{ID: "t1", Role: canon.RoleAssistant}},
			canon.TextDelta{ItemID: "t1", Text: "hi"},
			canon.ItemFinished{Item: canon.Message{ID: "t1", Role: canon.RoleAssistant, Content: []canon.Content{canon.TextContent{Text: "hi"}}}},
			canon.ItemStarted{Item: canon.FunctionCall{ID: "f1", CallID: "call_7", Name: "run_tool"}},
			canon.ToolArgumentsDelta{ItemID: "f1", Bytes: []byte(`{"x":1}`)},
			canon.ItemFinished{Item: canon.FunctionCall{ID: "f1", CallID: "call_7", Name: "run_tool", Arguments: []byte(`{"x":1}`)}},
			canon.TurnFinished{Status: canon.Completed()},
		})
	frames := parseFrames(t, out)
	byName := map[string][]map[string]any{}
	for _, f := range frames {
		byName[f.name] = append(byName[f.name], f.data)
	}
	start := frames[0].data["message"].(map[string]any)
	if start["id"] != "msg_1" || start["role"] != "assistant" || start["model"] != "claude-antigravity--gemini-3-pro" {
		t.Fatalf("message_start message = %v", start)
	}
	if start["type"] != "message" {
		t.Fatalf("message type = %v, want message", start["type"])
	}
	first := byName["content_block_start"][0]
	block := first["content_block"].(map[string]any)
	if block["type"] != "thinking" {
		t.Fatalf("first block type = %v, want thinking", block["type"])
	}
	if block["thinking"] != "" {
		t.Fatalf("thinking block initial thinking = %v, want empty", block["thinking"])
	}
	deltas := byName["content_block_delta"]
	thinkingDelta := deltas[0]["delta"].(map[string]any)
	if thinkingDelta["type"] != "thinking_delta" || thinkingDelta["thinking"] != "deep" {
		t.Fatalf("thinking delta = %v", thinkingDelta)
	}
	if deltas[0]["index"].(float64) != 0 {
		t.Fatalf("thinking delta index = %v, want 0", deltas[0]["index"])
	}
	signatureDelta := deltas[1]["delta"].(map[string]any)
	if signatureDelta["type"] != "signature_delta" || signatureDelta["signature"] != "sig9" {
		t.Fatalf("signature delta = %v", signatureDelta)
	}
	if deltas[1]["index"].(float64) != 0 {
		t.Fatalf("signature delta index = %v, want 0", deltas[1]["index"])
	}
	textDelta := deltas[2]["delta"].(map[string]any)
	if textDelta["type"] != "text_delta" || textDelta["text"] != "hi" {
		t.Fatalf("text delta = %v", textDelta)
	}
	if deltas[2]["index"].(float64) != 1 {
		t.Fatalf("text delta index = %v, want 1", deltas[2]["index"])
	}
	toolStart := byName["content_block_start"][2]
	toolBlock := toolStart["content_block"].(map[string]any)
	if toolBlock["type"] != "tool_use" || toolBlock["id"] != "call_7" || toolBlock["name"] != "run_tool" {
		t.Fatalf("tool_use block = %v", toolBlock)
	}
	if toolBlock["input"] == nil {
		t.Fatalf("tool_use input missing, want {}")
	}
	jsonDelta := deltas[3]["delta"].(map[string]any)
	if jsonDelta["type"] != "input_json_delta" || jsonDelta["partial_json"] != `{"x":1}` {
		t.Fatalf("input_json_delta = %v", jsonDelta)
	}
	if deltas[3]["index"].(float64) != 2 {
		t.Fatalf("json delta index = %v, want 2", deltas[3]["index"])
	}
}

func TestStopReasonTranslation(t *testing.T) {
	cases := []struct {
		name     string
		evs      []canon.Event
		terminal canon.Event
		want     string
	}{
		{
			name:     "completed text turn",
			evs:      []canon.Event{canon.ItemStarted{Item: canon.Message{ID: "t1"}}, canon.TextDelta{ItemID: "t1", Text: "ok"}, canon.ItemFinished{Item: canon.Message{ID: "t1"}}},
			terminal: canon.TurnFinished{Status: canon.Completed()},
			want:     "end_turn",
		},
		{
			name: "completed tool turn",
			evs: []canon.Event{
				canon.ItemStarted{Item: canon.FunctionCall{ID: "f1", CallID: "call_1", Name: "t"}},
				canon.ItemFinished{Item: canon.FunctionCall{ID: "f1", CallID: "call_1", Name: "t"}},
			},
			terminal: canon.TurnFinished{Status: canon.Completed()},
			want:     "tool_use",
		},
		{
			name:     "max output tokens",
			evs:      []canon.Event{canon.ItemStarted{Item: canon.Message{ID: "t1"}}, canon.ItemFinished{Item: canon.Message{ID: "t1"}}},
			terminal: canon.TurnFinished{Status: canon.Incomplete(canon.IncompleteMaxOutputTokens)},
			want:     "max_tokens",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, _ := runStream(t, ResponseHeader{ID: "msg_1", Model: "m"}, append(tc.evs, tc.terminal))
			frames := parseFrames(t, out)
			delta := frames[len(frames)-2]
			if delta.name != "message_delta" {
				t.Fatalf("terminal frame = %s, want message_delta", delta.name)
			}
			if got := delta.data["delta"].(map[string]any)["stop_reason"]; got != tc.want {
				t.Fatalf("stop_reason = %v, want %v", got, tc.want)
			}
			if frames[len(frames)-1].name != "message_stop" {
				t.Fatalf("last frame = %s, want message_stop", frames[len(frames)-1].name)
			}
		})
	}
}

func TestTurnFailedEmitsTypedError(t *testing.T) {
	out, _ := runStream(t,
		ResponseHeader{ID: "msg_1", Model: "m"},
		[]canon.Event{
			canon.ItemStarted{Item: canon.Message{ID: "t1"}},
			canon.TextDelta{ItemID: "t1", Text: "partial"},
			canon.ItemFinished{Item: canon.Message{ID: "t1"}},
			canon.TurnFailed{Failure: canon.Failure{Reason: canon.FailRateLimited, Message: "upstream limited"}},
		})
	frames := parseFrames(t, out)
	if frames[len(frames)-1].name != "error" {
		t.Fatalf("last frame = %s, want error", frames[len(frames)-1].name)
	}
	for _, f := range frames {
		if f.name == "message_stop" {
			t.Fatalf("message_stop emitted after TurnFailed")
		}
	}
	f := frames[len(frames)-1]
	if f.data["type"] != "error" {
		t.Fatalf("data type = %v, want error", f.data["type"])
	}
	errBody := f.data["error"].(map[string]any)
	if errBody["type"] != "rate_limit_error" {
		t.Fatalf("error type = %v, want rate_limit_error", errBody["type"])
	}
	if errBody["message"] != "upstream limited" {
		t.Fatalf("error message = %v", errBody["message"])
	}
}

func TestProviderErrorKeepsCode(t *testing.T) {
	raw := []byte(`{"message":"slow down","type":"tokens","code":"rate_limit_exceeded","param":null}`)
	var buf bytes.Buffer
	eg := New(&buf, false)
	if err := eg.Begin(ResponseHeader{ID: "m", Model: "x"}); err != nil {
		t.Fatal(err)
	}
	err := eg.Frame(canon.TurnFailed{Failure: canon.Failure{
		Reason:   canon.FailRateLimited,
		Message:  "slow down",
		Provider: &canon.ProviderError{Error: raw},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := eg.Flush(); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	body := got["error"].(map[string]any)
	if body["code"] != "rate_limit_exceeded" || body["type"] != "tokens" || body["message"] != "slow down" {
		t.Fatalf("error = %v", body)
	}
	if _, ok := body["param"]; !ok {
		t.Fatalf("param dropped: %v", body)
	}
}

func TestFailureReasonErrorTypes(t *testing.T) {
	cases := map[canon.FailureReason]string{
		canon.FailUnauthorized:      "authentication_error",
		canon.FailForbidden:         "permission_error",
		canon.FailQuotaExhausted:    "billing_error",
		canon.FailServerOverloaded:  "overloaded_error",
		canon.FailContextLength:     "invalid_request_error",
		canon.FailNotFound:          "not_found_error",
		canon.FailTimeout:           "timeout_error",
		canon.FailUpstreamTransport: "api_error",
		canon.FailToolArgsMalformed: "invalid_request_error",
		canon.FailOriginRejected:    "api_error",
	}
	for reason, want := range cases {
		var buf bytes.Buffer
		eg := New(&buf, true)
		if err := eg.Begin(ResponseHeader{ID: "m", Model: "x"}); err != nil {
			t.Fatalf("Begin: %v", err)
		}
		if err := eg.Frame(canon.TurnFailed{Failure: canon.Failure{Reason: reason, Message: "x"}}); err != nil {
			t.Fatalf("Frame: %v", err)
		}
		if err := eg.Flush(); err != nil {
			t.Fatalf("Flush: %v", err)
		}
		frames := parseFrames(t, buf.String())
		got := frames[len(frames)-1].data["error"].(map[string]any)["type"]
		if got != want {
			t.Fatalf("reason %d: error type = %v, want %v", reason, got, want)
		}
	}
}

func TestUsageZeroDefaultFields(t *testing.T) {
	out, _ := runStream(t,
		ResponseHeader{ID: "msg_1", Model: "m"},
		[]canon.Event{canon.TurnFinished{Status: canon.Completed()}})
	frames := parseFrames(t, out)
	startUsage := frames[0].data["message"].(map[string]any)["usage"].(map[string]any)
	for _, key := range []string{"input_tokens", "cache_creation_input_tokens", "cache_read_input_tokens", "output_tokens"} {
		v, ok := startUsage[key]
		if !ok {
			t.Fatalf("message_start usage missing %q", key)
		}
		if v.(float64) != 0 {
			t.Fatalf("message_start usage %q = %v, want 0", key, v)
		}
	}
	deltaUsage := frames[len(frames)-2].data["usage"].(map[string]any)
	for _, key := range []string{"input_tokens", "cache_creation_input_tokens", "cache_read_input_tokens", "output_tokens"} {
		if _, ok := deltaUsage[key]; !ok {
			t.Fatalf("message_delta usage missing %q", key)
		}
	}
}

func TestUsageCarriesValues(t *testing.T) {
	out, _ := runStream(t,
		ResponseHeader{ID: "msg_1", Model: "m"},
		[]canon.Event{canon.TurnFinished{
			Status: canon.Completed(),
			Usage:  canon.Usage{InputTokens: 12, CachedInputTokens: 4, OutputTokens: 34},
		}})
	frames := parseFrames(t, out)
	usage := frames[len(frames)-2].data["usage"].(map[string]any)
	if usage["input_tokens"].(float64) != 8 || usage["cache_read_input_tokens"].(float64) != 4 || usage["output_tokens"].(float64) != 34 {
		t.Fatalf("message_delta usage = %v", usage)
	}
}

func TestCommitGating(t *testing.T) {
	var buf bytes.Buffer
	eg := New(&buf, true)
	if got := eg.Lifecycle().CommitState(); got != provider.NotStarted {
		t.Fatalf("commit state before Begin = %v, want NotStarted", got)
	}
	if err := eg.Begin(ResponseHeader{ID: "msg_1", Model: "m"}); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if got := eg.Lifecycle().CommitState(); got != provider.ResponseStarted {
		t.Fatalf("commit state after Begin = %v, want ResponseStarted", got)
	}
	if err := eg.Frame(canon.ItemStarted{Item: canon.Message{ID: "t1", Role: canon.RoleAssistant}}); err != nil {
		t.Fatalf("Frame: %v", err)
	}
	if got := eg.Lifecycle().CommitState(); got != provider.OutputCommitted {
		t.Fatalf("commit state after content_block_start = %v, want OutputCommitted", got)
	}
	if err := eg.Frame(canon.ItemFinished{Item: canon.Message{ID: "t1"}}); err != nil {
		t.Fatalf("Frame: %v", err)
	}
	if err := eg.Frame(canon.TurnFinished{Status: canon.Completed()}); err != nil {
		t.Fatalf("Frame: %v", err)
	}
	if err := eg.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
}

func TestItemStateAvailableEmitsNothing(t *testing.T) {
	var buf bytes.Buffer
	eg := New(&buf, true)
	if err := eg.Begin(ResponseHeader{ID: "msg_1", Model: "m"}); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	before := buf.Len()
	if err := eg.Frame(canon.ItemStateAvailable{ItemID: "r1", State: canon.OpaqueRef{Store: "anthropic", Key: "k"}}); err != nil {
		t.Fatalf("Frame: %v", err)
	}
	if buf.Len() != before {
		t.Fatalf("ItemStateAvailable wrote %q, want nothing", buf.String()[before:])
	}
}

func TestFrameContractErrors(t *testing.T) {
	t.Run("frame before begin", func(t *testing.T) {
		eg := New(&bytes.Buffer{}, true)
		err := mustFrameErr(t, eg.Frame(canon.TextDelta{ItemID: "t1", Text: "x"}))
		if err.Reason != ReasonNotBegun {
			t.Fatalf("reason = %v, want not_begun", err.Reason)
		}
	})
	t.Run("flush before begin", func(t *testing.T) {
		eg := New(&bytes.Buffer{}, true)
		err := mustFrameErr(t, eg.Flush())
		if err.Reason != ReasonNotBegun {
			t.Fatalf("reason = %v, want not_begun", err.Reason)
		}
	})
	t.Run("double begin", func(t *testing.T) {
		var buf bytes.Buffer
		eg := New(&buf, true)
		if err := eg.Begin(ResponseHeader{ID: "m", Model: "x"}); err != nil {
			t.Fatalf("Begin: %v", err)
		}
		err := mustFrameErr(t, eg.Begin(ResponseHeader{ID: "m", Model: "x"}))
		if err.Reason != ReasonAlreadyBegun {
			t.Fatalf("reason = %v, want already_begun", err.Reason)
		}
	})
	t.Run("delta without block", func(t *testing.T) {
		var buf bytes.Buffer
		eg := New(&buf, true)
		if err := eg.Begin(ResponseHeader{ID: "m", Model: "x"}); err != nil {
			t.Fatalf("Begin: %v", err)
		}
		err := mustFrameErr(t, eg.Frame(canon.TextDelta{ItemID: "nope", Text: "x"}))
		if err.Reason != ReasonUnknownItem || err.ItemID != "nope" {
			t.Fatalf("err = %+v, want unknown_item for nope", err)
		}
	})
	t.Run("delta kind mismatch", func(t *testing.T) {
		var buf bytes.Buffer
		eg := New(&buf, true)
		if err := eg.Begin(ResponseHeader{ID: "m", Model: "x"}); err != nil {
			t.Fatalf("Begin: %v", err)
		}
		if err := eg.Frame(canon.ItemStarted{Item: canon.Message{ID: "t1"}}); err != nil {
			t.Fatalf("Frame: %v", err)
		}
		err := mustFrameErr(t, eg.Frame(canon.ReasoningDelta{ItemID: "t1", Text: "x"}))
		if err.Reason != ReasonBlockMismatch {
			t.Fatalf("reason = %v, want block_mismatch", err.Reason)
		}
	})
	t.Run("custom tool input requires open call", func(t *testing.T) {
		var buf bytes.Buffer
		eg := New(&buf, true)
		if err := eg.Begin(ResponseHeader{ID: "m", Model: "x"}); err != nil {
			t.Fatalf("Begin: %v", err)
		}
		err := mustFrameErr(t, eg.Frame(canon.CustomToolInputDelta{ItemID: "c1", Text: "x"}))
		if err.Reason != ReasonUnknownItem {
			t.Fatalf("reason = %v, want unknown_item", err.Reason)
		}
	})
	t.Run("unsupported item started", func(t *testing.T) {
		var buf bytes.Buffer
		eg := New(&buf, true)
		if err := eg.Begin(ResponseHeader{ID: "m", Model: "x"}); err != nil {
			t.Fatalf("Begin: %v", err)
		}
		err := mustFrameErr(t, eg.Frame(canon.ItemStarted{Item: canon.LocalShellCall{ID: "s1", CallID: "c", Command: "ls"}}))
		if err.Reason != ReasonUnsupportedItem {
			t.Fatalf("reason = %v, want unsupported_item", err.Reason)
		}
	})
	t.Run("finish for unknown item", func(t *testing.T) {
		var buf bytes.Buffer
		eg := New(&buf, true)
		if err := eg.Begin(ResponseHeader{ID: "m", Model: "x"}); err != nil {
			t.Fatalf("Begin: %v", err)
		}
		err := mustFrameErr(t, eg.Frame(canon.ItemFinished{Item: canon.Message{ID: "ghost"}}))
		if err.Reason != ReasonUnknownItem {
			t.Fatalf("reason = %v, want unknown_item", err.Reason)
		}
	})
	t.Run("flush without terminal", func(t *testing.T) {
		var buf bytes.Buffer
		eg := New(&buf, true)
		if err := eg.Begin(ResponseHeader{ID: "m", Model: "x"}); err != nil {
			t.Fatalf("Begin: %v", err)
		}
		err := mustFrameErr(t, eg.Flush())
		if err.Reason != ReasonNoTerminal {
			t.Fatalf("reason = %v, want no_terminal", err.Reason)
		}
	})
	t.Run("flush closes a block left open by a truncated turn", func(t *testing.T) {
		var buf bytes.Buffer
		eg := New(&buf, true)
		if err := eg.Begin(ResponseHeader{ID: "m", Model: "x"}); err != nil {
			t.Fatalf("Begin: %v", err)
		}
		for _, ev := range []canon.Event{
			canon.ItemStarted{Item: canon.Message{ID: "t1"}},
			canon.TextDelta{ItemID: "t1", Text: "partial"},
			canon.TurnFinished{Status: canon.Incomplete(canon.IncompleteUpstreamStall)},
		} {
			if err := eg.Frame(ev); err != nil {
				t.Fatalf("Frame: %v", err)
			}
		}
		if err := eg.Flush(); err != nil {
			t.Fatalf("Flush: %v", err)
		}
		names := frameNames(parseFrames(t, buf.String()))
		want := []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "error"}
		if strings.Join(names, ",") != strings.Join(want, ",") {
			t.Fatalf("frames = %v, want %v", names, want)
		}
	})
	t.Run("frame after terminal", func(t *testing.T) {
		var buf bytes.Buffer
		eg := New(&buf, true)
		if err := eg.Begin(ResponseHeader{ID: "m", Model: "x"}); err != nil {
			t.Fatalf("Begin: %v", err)
		}
		if err := eg.Frame(canon.TurnFinished{Status: canon.Completed()}); err != nil {
			t.Fatalf("Frame: %v", err)
		}
		err := mustFrameErr(t, eg.Frame(canon.TurnFinished{Status: canon.Completed()}))
		if err.Reason != ReasonAfterTerminal {
			t.Fatalf("reason = %v, want after_terminal", err.Reason)
		}
		err = mustFrameErr(t, eg.Frame(canon.TextDelta{ItemID: "t1", Text: "x"}))
		if err.Reason != ReasonAfterTerminal {
			t.Fatalf("reason = %v, want after_terminal", err.Reason)
		}
	})
	t.Run("double flush", func(t *testing.T) {
		var buf bytes.Buffer
		eg := New(&buf, true)
		if err := eg.Begin(ResponseHeader{ID: "m", Model: "x"}); err != nil {
			t.Fatalf("Begin: %v", err)
		}
		if err := eg.Frame(canon.TurnFinished{Status: canon.Completed()}); err != nil {
			t.Fatalf("Frame: %v", err)
		}
		if err := eg.Flush(); err != nil {
			t.Fatalf("Flush: %v", err)
		}
		err := mustFrameErr(t, eg.Flush())
		if err.Reason != ReasonAlreadyFlushed {
			t.Fatalf("reason = %v, want already_flushed", err.Reason)
		}
	})
}

func mustFrameErr(t *testing.T, err error) *FrameError {
	t.Helper()
	fe, ok := err.(*FrameError)
	if !ok {
		t.Fatalf("error = %v (%T), want *FrameError", err, err)
	}
	return fe
}

func TestNonStreamingEmitsSingleMessageJSON(t *testing.T) {
	var buf bytes.Buffer
	eg := New(&buf, false)
	if err := eg.Begin(ResponseHeader{ID: "msg_9", Model: "claude-codex--gpt-5"}); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	rawInput := "*** Begin Patch\n+value = 9007199254740993\n*** End Patch\n"
	arguments := `{"large":9007199254740993,"fraction":1.2300,"exponent":1e+09}`
	evs := []canon.Event{
		canon.ItemStarted{Item: canon.ReasoningItem{ID: "r1", Content: ""}},
		canon.ReasoningDelta{ItemID: "r1", Text: "thinking"},
		canon.ItemFinished{Item: canon.ReasoningItem{ID: "r1", Content: "thinking", Signature: "sig1"}},
		canon.ItemStarted{Item: canon.Message{ID: "t1", Role: canon.RoleAssistant, Content: []canon.Content{}}},
		canon.TextDelta{ItemID: "t1", Text: "Hello"},
		canon.TextDelta{ItemID: "t1", Text: " world"},
		canon.ItemFinished{Item: canon.Message{ID: "t1", Role: canon.RoleAssistant, Content: []canon.Content{canon.TextContent{Text: "Hello world"}}}},
		canon.ItemStarted{Item: canon.CustomToolCall{ID: "c1", CallID: "call_c1", Name: "patch"}},
		canon.CustomToolInputDelta{ItemID: "c1", Text: rawInput},
		canon.ItemFinished{Item: canon.CustomToolCall{ID: "c1", CallID: "call_c1", Name: "patch", Input: rawInput}},
		canon.ItemStarted{Item: canon.FunctionCall{ID: "f1", CallID: "call_f1", Name: "measure"}},
		canon.ToolArgumentsDelta{ItemID: "f1", Bytes: []byte(arguments)},
		canon.ItemFinished{Item: canon.FunctionCall{ID: "f1", CallID: "call_f1", Name: "measure", Arguments: []byte(arguments)}},
		canon.TurnFinished{Status: canon.Completed(), Usage: canon.Usage{InputTokens: 3, OutputTokens: 5}},
	}
	for i, ev := range evs {
		if err := eg.Frame(ev); err != nil {
			t.Fatalf("Frame %d: %v", i, err)
		}
	}
	if err := eg.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	var msg struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type      string `json:"type"`
			Text      string `json:"text"`
			Thinking  string `json:"thinking"`
			Signature string `json:"signature"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(buf.Bytes(), &msg); err != nil {
		t.Fatalf("non-streaming output is not one JSON message: %v\n%s", err, buf.String())
	}
	if msg.ID != "msg_9" || msg.Type != "message" || msg.Role != "assistant" {
		t.Fatalf("envelope wrong: %+v", msg)
	}
	if len(msg.Content) != 4 {
		t.Fatalf("content blocks: %+v", msg.Content)
	}
	if msg.Content[0].Type != "thinking" || msg.Content[0].Thinking != "thinking" || msg.Content[0].Signature != "sig1" {
		t.Fatalf("thinking block: %+v", msg.Content[0])
	}
	if msg.Content[1].Type != "text" || msg.Content[1].Text != "Hello world" {
		t.Fatalf("text block: %+v", msg.Content[1])
	}
	custom := msg.Content[2]
	if custom.Type != "tool_use" || custom.ID != "call_c1" || custom.Name != "patch" {
		t.Fatalf("custom tool block: %+v", custom)
	}
	var wrapper struct {
		Input string `json:"input"`
	}
	if err := json.Unmarshal(custom.Input, &wrapper); err != nil || wrapper.Input != rawInput {
		t.Fatalf("custom input = %s, decoded = %q, err = %v", custom.Input, wrapper.Input, err)
	}
	function := msg.Content[3]
	if function.Type != "tool_use" || function.ID != "call_f1" || function.Name != "measure" || string(function.Input) != arguments {
		t.Fatalf("numeric tool block: %+v", function)
	}
	if msg.StopReason != "tool_use" {
		t.Fatalf("stop reason: %q", msg.StopReason)
	}
	if msg.Usage.InputTokens != 3 || msg.Usage.OutputTokens != 5 {
		t.Fatalf("usage: %+v", msg.Usage)
	}
}

func TestNonStreamingFailedEmitsErrorJSON(t *testing.T) {
	var buf bytes.Buffer
	eg := New(&buf, false)
	if err := eg.Begin(ResponseHeader{ID: "msg_e", Model: "claude-codex--gpt-5"}); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := eg.Frame(canon.TurnFailed{Failure: canon.Failure{Reason: canon.FailUpstreamTransport, Message: "upstream down"}}); err != nil {
		t.Fatalf("Frame: %v", err)
	}
	if err := eg.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	var e struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(buf.Bytes(), &e); err != nil {
		t.Fatalf("failed output is not one JSON error: %v\n%s", err, buf.String())
	}
	if e.Type != "error" || e.Error.Message != "upstream down" {
		t.Fatalf("error envelope: %+v", e)
	}
}

func TestNonStreamingEmptyTurnHasContentArray(t *testing.T) {
	var buf bytes.Buffer
	eg := New(&buf, false)
	if err := eg.Begin(ResponseHeader{ID: "m", Model: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := eg.Frame(canon.TurnFinished{Status: canon.Completed()}); err != nil {
		t.Fatal(err)
	}
	if err := eg.Flush(); err != nil {
		t.Fatal(err)
	}
	var msg struct {
		Content *[]any `json:"content"`
	}
	if err := json.Unmarshal(buf.Bytes(), &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Content == nil {
		t.Fatalf("content must be [] for an empty turn, body %s", buf.String())
	}
}

func TestNonStreamingInterruptedTurnReportsError(t *testing.T) {
	var buf bytes.Buffer
	eg := New(&buf, false)
	if err := eg.Begin(ResponseHeader{ID: "m", Model: "x"}); err != nil {
		t.Fatal(err)
	}
	for _, ev := range []canon.Event{
		canon.ItemStarted{Item: canon.Message{ID: "t1"}},
		canon.TextDelta{ItemID: "t1", Text: "par"},
		canon.TextDelta{ItemID: "t1", Text: "tial"},
		canon.TurnFinished{Status: canon.Incomplete(canon.IncompleteUpstreamStall)},
	} {
		if err := eg.Frame(ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := eg.Flush(); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(buf.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Type != "error" || response.Error.Type != "api_error" || response.Error.Message == "" || len(response.Content) != 0 {
		t.Fatalf("interrupted turn did not return an error envelope: %s", buf.String())
	}
}

func TestFinishOnlyContentIsStreamedAsDeltas(t *testing.T) {
	out, _ := runStream(t, ResponseHeader{ID: "msg_1", Model: "m"}, []canon.Event{
		canon.ItemStarted{Item: canon.ReasoningItem{ID: "r1"}},
		canon.ItemFinished{Item: canon.ReasoningItem{ID: "r1", Content: "deep", Signature: "sig"}},
		canon.ItemStarted{Item: canon.Message{ID: "t1", Role: canon.RoleAssistant}},
		canon.ItemFinished{Item: canon.Message{ID: "t1", Role: canon.RoleAssistant, Content: []canon.Content{canon.TextContent{Text: "whole"}}}},
		canon.ItemStarted{Item: canon.FunctionCall{ID: "f1", CallID: "c1", Name: "lookup"}},
		canon.ItemFinished{Item: canon.FunctionCall{ID: "f1", CallID: "c1", Name: "lookup", Arguments: []byte(`{"q":1}`)}},
		canon.TurnFinished{Status: canon.Completed()},
	})
	got := map[string]string{}
	for _, f := range parseFrames(t, out) {
		if f.name != "content_block_delta" {
			continue
		}
		d := f.data["delta"].(map[string]any)
		switch d["type"] {
		case "thinking_delta":
			got["thinking"] += d["thinking"].(string)
		case "text_delta":
			got["text"] += d["text"].(string)
		case "input_json_delta":
			got["json"] += d["partial_json"].(string)
		}
	}
	if got["thinking"] != "deep" || got["text"] != "whole" || got["json"] != `{"q":1}` {
		t.Fatalf("deltas = %v\n%s", got, out)
	}
}
