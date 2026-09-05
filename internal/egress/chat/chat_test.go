package chat

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"prism/internal/canon"
	"prism/internal/provider"
)

func header() ResponseHeader {
	return ResponseHeader{ID: "resp_1", Model: "gpt-fixture", CreatedAt: time.Unix(1700000000, 0).UTC()}
}

func frames(t *testing.T, buf *bytes.Buffer) []string {
	t.Helper()
	var out []string
	for _, part := range strings.Split(buf.String(), "\n\n") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.HasPrefix(part, "data: ") {
			t.Fatalf("non-SSE frame %q", part)
		}
		out = append(out, strings.TrimPrefix(part, "data: "))
	}
	return out
}

func decode(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return m
}

func firstChoice(t *testing.T, frame map[string]any) map[string]any {
	t.Helper()
	choices, ok := frame["choices"].([]any)
	if !ok || len(choices) != 1 {
		t.Fatalf("frame %+v wants one choice", frame)
	}
	c, ok := choices[0].(map[string]any)
	if !ok {
		t.Fatalf("choice shape %+v", choices[0])
	}
	return c
}

func hasWarning(c *Chat, code string) bool {
	for _, w := range c.Warnings() {
		if w.Code == code {
			return true
		}
	}
	return false
}

func TestCommitPoint(t *testing.T) {
	var buf bytes.Buffer
	c := New(&buf, true)
	if c.CommitState() != provider.NotStarted {
		t.Fatalf("fresh commit state = %d, want NotStarted", c.CommitState())
	}
	if err := c.Begin(header()); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if c.CommitState() != provider.ResponseStarted {
		t.Fatalf("after begin commit state = %d, want ResponseStarted", c.CommitState())
	}
	if err := c.Frame(canon.TextDelta{ItemID: "m1", Text: "hi"}); err != nil {
		t.Fatalf("frame text: %v", err)
	}
	if c.CommitState() != provider.OutputCommitted {
		t.Fatalf("after first delta commit state = %d, want OutputCommitted", c.CommitState())
	}
	if c.CommitState() < provider.OutputCommitted {
		t.Fatal("failover must be impossible after the first delta chunk")
	}
	if err := c.Begin(header()); err == nil {
		t.Fatal("second begin must fail")
	}
}

func TestChunkFraming(t *testing.T) {
	var buf bytes.Buffer
	c := New(&buf, true)
	if err := c.Begin(header()); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := c.Frame(canon.TextDelta{ItemID: "m1", Text: "Hello"}); err != nil {
		t.Fatalf("frame text: %v", err)
	}
	if err := c.Frame(canon.TextDelta{ItemID: "m1", Text: " world"}); err != nil {
		t.Fatalf("frame text: %v", err)
	}
	if err := c.Frame(canon.TurnFinished{Status: canon.Completed()}); err != nil {
		t.Fatalf("frame terminal: %v", err)
	}
	if err := c.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	fs := frames(t, &buf)
	if len(fs) != 5 {
		t.Fatalf("frames = %d, want 5", len(fs))
	}
	if fs[4] != "[DONE]" {
		t.Fatalf("last frame = %q, want [DONE]", fs[4])
	}
	f0 := decode(t, fs[0])
	if f0["object"] != chunkObject || f0["id"] != "resp_1" || f0["model"] != "gpt-fixture" {
		t.Fatalf("preamble header = %+v", f0)
	}
	if f0["created"] != float64(1700000000) {
		t.Fatalf("created = %v", f0["created"])
	}
	d0 := firstChoice(t, f0)["delta"].(map[string]any)
	if d0["role"] != assistantRole {
		t.Fatalf("preamble delta = %+v, want role assistant", d0)
	}
	if _, ok := d0["content"]; ok {
		t.Fatalf("preamble delta must not carry content: %+v", d0)
	}
	for i, want := range []string{"Hello", " world"} {
		fi := decode(t, fs[i+1])
		ch := firstChoice(t, fi)
		if ch["index"] != float64(0) {
			t.Fatalf("frame %d index = %v, want 0", i+1, ch["index"])
		}
		d := ch["delta"].(map[string]any)
		if _, ok := d["role"]; ok {
			t.Fatalf("delta frame %d must not repeat role: %+v", i+1, d)
		}
		if d["content"] != want {
			t.Fatalf("frame %d content = %v, want %q", i+1, d["content"], want)
		}
	}
	if !strings.Contains(fs[0], `"finish_reason":null`) {
		t.Fatalf("delta chunks must carry null finish_reason: %s", fs[0])
	}
}

func TestReasoningStreamedAsReasoningContent(t *testing.T) {
	var buf bytes.Buffer
	c := New(&buf, true)
	if err := c.Begin(header()); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := c.Frame(canon.ReasoningDelta{ItemID: "r1", Text: "think"}); err != nil {
		t.Fatalf("frame reasoning: %v", err)
	}
	if err := c.Frame(canon.ReasoningDelta{ItemID: "r1", Text: "ing"}); err != nil {
		t.Fatalf("frame reasoning: %v", err)
	}
	fs := frames(t, &buf)
	if len(fs) != 3 {
		t.Fatalf("frames = %d, want preamble + 2 reasoning deltas; got %v", len(fs), fs)
	}
	second := firstChoice(t, decode(t, fs[1]))["delta"].(map[string]any)
	third := firstChoice(t, decode(t, fs[2]))["delta"].(map[string]any)
	if second["reasoning_content"] != "think" || third["reasoning_content"] != "ing" {
		t.Fatalf("reasoning deltas = %v, %v", second["reasoning_content"], third["reasoning_content"])
	}
	if _, present := second["content"]; present {
		t.Fatalf("reasoning delta must not carry content: %v", second)
	}
	if c.CommitState() != provider.OutputCommitted {
		t.Fatalf("reasoning is provider-derived output; commit state = %d", c.CommitState())
	}
}

func TestToolCallStreamAssembly(t *testing.T) {
	var buf bytes.Buffer
	c := New(&buf, true)
	if err := c.Begin(header()); err != nil {
		t.Fatalf("begin: %v", err)
	}
	seq := []canon.Event{
		canon.ItemStarted{Item: canon.FunctionCall{ID: "i1", CallID: "call_1", Name: "get_weather"}},
		canon.ToolArgumentsDelta{ItemID: "i1", Bytes: []byte(`{"city"`)},
		canon.ToolArgumentsDelta{ItemID: "i1", Bytes: []byte(`:"Paris"}`)},
		canon.ItemFinished{Item: canon.FunctionCall{ID: "i1", CallID: "call_1", Name: "get_weather", Arguments: []byte(`{"city":"Paris"}`)}},
		canon.ItemStarted{Item: canon.FunctionCall{ID: "i2", CallID: "call_2", Name: "get_time"}},
		canon.ItemFinished{Item: canon.FunctionCall{ID: "i2", CallID: "call_2", Name: "get_time", Arguments: []byte(`{}`)}},
	}
	for _, ev := range seq {
		if err := c.Frame(ev); err != nil {
			t.Fatalf("frame %T: %v", ev, ev)
		}
	}
	if c.CommitState() != provider.OutputCommitted {
		t.Fatalf("tool call chunks are provider-derived; commit state = %d", c.CommitState())
	}
	if err := c.Frame(canon.TurnFinished{Status: canon.Completed()}); err != nil {
		t.Fatalf("frame terminal: %v", err)
	}
	if err := c.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	fs := frames(t, &buf)
	intro1 := decode(t, fs[1])
	tc := firstChoice(t, intro1)["delta"].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
	if tc["index"] != float64(0) || tc["id"] != "call_1" || tc["type"] != functionType {
		t.Fatalf("intro tool call = %+v", tc)
	}
	fn := tc["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Fatalf("intro function = %+v", fn)
	}
	if _, ok := fn["arguments"]; ok {
		t.Fatalf("intro must not carry arguments: %+v", fn)
	}
	assembled := ""
	for _, raw := range fs[2:4] {
		f := decode(t, raw)
		d := firstChoice(t, f)["delta"].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
		if d["index"] != float64(0) {
			t.Fatalf("fragment index = %v, want 0", d["index"])
		}
		if _, ok := d["id"]; ok {
			t.Fatalf("fragment must not repeat id: %+v", d)
		}
		assembled += d["function"].(map[string]any)["arguments"].(string)
	}
	if assembled != `{"city":"Paris"}` {
		t.Fatalf("assembled arguments = %q", assembled)
	}
	intro2 := decode(t, fs[4])
	tc2 := firstChoice(t, intro2)["delta"].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
	if tc2["index"] != float64(1) || tc2["id"] != "call_2" {
		t.Fatalf("second intro = %+v", tc2)
	}
	final := decode(t, fs[len(fs)-2])
	ch := firstChoice(t, final)
	if ch["finish_reason"] != "tool_calls" {
		t.Fatalf("finish_reason = %v, want tool_calls", ch["finish_reason"])
	}
	if _, ok := ch["delta"].(map[string]any)["content"]; ok {
		t.Fatal("final delta must be empty")
	}
	u, ok := final["usage"].(map[string]any)
	if !ok {
		t.Fatalf("final chunk usage missing: %s", fs[len(fs)-2])
	}
	for _, key := range []string{"prompt_tokens", "completion_tokens", "total_tokens", "prompt_tokens_details", "completion_tokens_details"} {
		if _, ok := u[key]; !ok {
			t.Fatalf("usage zero-default field %q missing in final chunk", key)
		}
	}
}

func TestToolCallWholeArguments(t *testing.T) {
	var buf bytes.Buffer
	c := New(&buf, true)
	if err := c.Begin(header()); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := c.Frame(canon.ItemStarted{Item: canon.FunctionCall{ID: "i1", CallID: "call_1", Name: "get_weather"}}); err != nil {
		t.Fatalf("frame start: %v", err)
	}
	if err := c.Frame(canon.ItemFinished{Item: canon.FunctionCall{ID: "i1", CallID: "call_1", Name: "get_weather", Arguments: []byte(`{"city":"Paris"}`)}}); err != nil {
		t.Fatalf("frame finish: %v", err)
	}
	fs := frames(t, &buf)
	if len(fs) != 3 {
		t.Fatalf("frames = %d, want 3", len(fs))
	}
	d := firstChoice(t, decode(t, fs[2]))["delta"].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
	if d["function"].(map[string]any)["arguments"] != `{"city":"Paris"}` {
		t.Fatalf("whole arguments delta = %+v", d)
	}
}

func TestFinishReasonMapping(t *testing.T) {
	tests := []struct {
		name string
		ev   canon.Event
		want string
	}{
		{"completed", canon.TurnFinished{Status: canon.Completed()}, "stop"},
		{"max_tokens", canon.TurnFinished{Status: canon.Incomplete(canon.IncompleteMaxOutputTokens)}, "length"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			c := New(&buf, true)
			if err := c.Begin(header()); err != nil {
				t.Fatalf("begin: %v", err)
			}
			if err := c.Frame(tt.ev); err != nil {
				t.Fatalf("frame terminal: %v", err)
			}
			fs := frames(t, &buf)
			ch := firstChoice(t, decode(t, fs[1]))
			if ch["finish_reason"] != tt.want {
				t.Fatalf("finish_reason = %v, want %q", ch["finish_reason"], tt.want)
			}
			if hasWarning(c, WarnFinishUnmapped) {
				t.Fatalf("mapped status must not warn: %+v", c.Warnings())
			}
		})
	}
	t.Run("unmapped_incomplete", func(t *testing.T) {
		var buf bytes.Buffer
		c := New(&buf, true)
		if err := c.Begin(header()); err != nil {
			t.Fatalf("begin: %v", err)
		}
		if err := c.Frame(canon.TurnFinished{Status: canon.Incomplete(canon.IncompleteContentFilter)}); err != nil {
			t.Fatalf("frame terminal: %v", err)
		}
		fs := frames(t, &buf)
		if got := firstChoice(t, decode(t, fs[1]))["finish_reason"]; got != "stop" {
			t.Fatalf("finish_reason = %v, want stop", got)
		}
		if !hasWarning(c, WarnFinishUnmapped) {
			t.Fatalf("warnings = %+v, want %s", c.Warnings(), WarnFinishUnmapped)
		}
	})
}

func TestTurnFailedErrorEnvelope(t *testing.T) {
	var buf bytes.Buffer
	c := New(&buf, true)
	if err := c.Begin(header()); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := c.Frame(canon.TurnFailed{Failure: canon.Failure{Reason: canon.FailRateLimited, Message: "boom"}}); err != nil {
		t.Fatalf("frame failure: %v", err)
	}
	if err := c.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	fs := frames(t, &buf)
	if len(fs) < 2 || fs[len(fs)-1] != "[DONE]" {
		t.Fatalf("stream must end with an error frame and [DONE], got %v", fs)
	}
	body := decode(t, fs[1])["error"].(map[string]any)
	if body["message"] != "boom" || body["type"] != "rate_limit_error" || body["code"] != "rate_limited" {
		t.Fatalf("error envelope = %+v", body)
	}
}

func TestNonStreamingAggregation(t *testing.T) {
	var buf bytes.Buffer
	c := New(&buf, false)
	if err := c.Begin(header()); err != nil {
		t.Fatalf("begin: %v", err)
	}
	seq := []canon.Event{
		canon.TextDelta{ItemID: "m1", Text: "Hello"},
		canon.ItemStarted{Item: canon.FunctionCall{ID: "i1", CallID: "call_1", Name: "get_weather"}},
		canon.ToolArgumentsDelta{ItemID: "i1", Bytes: []byte(`{"city":"Paris"}`)},
		canon.ItemFinished{Item: canon.FunctionCall{ID: "i1", CallID: "call_1", Name: "get_weather", Arguments: []byte(`{"city":"Paris"}`)}},
		canon.TurnFinished{
			Status: canon.Completed(),
			Usage:  canon.Usage{InputTokens: 5, OutputTokens: 7, TotalTokens: 12, CachedInputTokens: 2, ReasoningTokens: 3},
		},
	}
	for _, ev := range seq {
		if err := c.Frame(ev); err != nil {
			t.Fatalf("frame %T: %v", ev, ev)
		}
	}
	if strings.Contains(buf.String(), "\n") {
		t.Fatalf("non-streaming body must be one JSON document, got %q", buf.String())
	}
	out := decode(t, buf.String())
	if out["object"] != completionObject {
		t.Fatalf("object = %v", out["object"])
	}
	ch := firstChoice(t, out)
	if ch["finish_reason"] != "tool_calls" {
		t.Fatalf("finish_reason = %v, want tool_calls", ch["finish_reason"])
	}
	msg := ch["message"].(map[string]any)
	if msg["role"] != assistantRole || msg["content"] != "Hello" {
		t.Fatalf("message = %+v", msg)
	}
	tcs := msg["tool_calls"].([]any)
	if len(tcs) != 1 {
		t.Fatalf("tool_calls = %+v", tcs)
	}
	tc := tcs[0].(map[string]any)
	if tc["id"] != "call_1" || tc["type"] != functionType {
		t.Fatalf("tool call = %+v", tc)
	}
	fn := tc["function"].(map[string]any)
	if fn["name"] != "get_weather" || fn["arguments"] != `{"city":"Paris"}` {
		t.Fatalf("function = %+v", fn)
	}
	u := out["usage"].(map[string]any)
	if u["prompt_tokens"] != float64(5) || u["completion_tokens"] != float64(7) || u["total_tokens"] != float64(12) {
		t.Fatalf("usage = %+v", u)
	}
	if u["prompt_tokens_details"].(map[string]any)["cached_tokens"] != float64(2) {
		t.Fatalf("cached tokens = %+v", u["prompt_tokens_details"])
	}
	if u["completion_tokens_details"].(map[string]any)["reasoning_tokens"] != float64(3) {
		t.Fatalf("reasoning tokens = %+v", u["completion_tokens_details"])
	}
}

func TestNonStreamingZeroUsageFieldsPresent(t *testing.T) {
	var buf bytes.Buffer
	c := New(&buf, false)
	if err := c.Begin(header()); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := c.Frame(canon.TurnFinished{Status: canon.Completed()}); err != nil {
		t.Fatalf("frame terminal: %v", err)
	}
	raw := buf.String()
	for _, key := range []string{`"prompt_tokens":0`, `"completion_tokens":0`, `"total_tokens":0`, `"cached_tokens":0`, `"reasoning_tokens":0`} {
		if !strings.Contains(raw, key) {
			t.Fatalf("usage zero-default %s missing in %s", key, raw)
		}
	}
}

func TestNonStreamingTurnFailed(t *testing.T) {
	var buf bytes.Buffer
	c := New(&buf, false)
	if err := c.Begin(header()); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := c.Frame(canon.TurnFailed{Failure: canon.Failure{Reason: canon.FailUnauthorized, Message: "nope"}}); err != nil {
		t.Fatalf("frame failure: %v", err)
	}
	if err := c.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	body := decode(t, buf.String())["error"].(map[string]any)
	if body["type"] != "authentication_error" || body["code"] != "unauthorized" || body["message"] != "nope" {
		t.Fatalf("error envelope = %+v", body)
	}
}

func TestFlushEmitsDoneOnce(t *testing.T) {
	var buf bytes.Buffer
	c := New(&buf, true)
	if err := c.Begin(header()); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := c.Frame(canon.TurnFinished{Status: canon.Completed()}); err != nil {
		t.Fatalf("frame terminal: %v", err)
	}
	if err := c.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := c.Flush(); err != nil {
		t.Fatalf("second flush: %v", err)
	}
	if got := strings.Count(buf.String(), "[DONE]"); got != 1 {
		t.Fatalf("[DONE] count = %d, want 1", got)
	}
}

func TestFrameGuards(t *testing.T) {
	var buf bytes.Buffer
	c := New(&buf, true)
	if err := c.Frame(canon.TextDelta{ItemID: "m1", Text: "x"}); err == nil {
		t.Fatal("frame before begin must fail")
	}
	if err := c.Begin(header()); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := c.Flush(); err == nil {
		t.Fatal("flush before terminal must fail")
	}
	if err := c.Frame(canon.TurnFinished{Status: canon.Completed()}); err != nil {
		t.Fatalf("frame terminal: %v", err)
	}
	if err := c.Frame(canon.TextDelta{ItemID: "m1", Text: "x"}); err == nil {
		t.Fatal("frame after terminal must fail")
	}
}

func TestBeginEmptyID(t *testing.T) {
	var buf bytes.Buffer
	c := New(&buf, true)
	if err := c.Begin(ResponseHeader{Model: "m"}); err == nil {
		t.Fatal("begin without response id must fail")
	}
}

func TestUnrepresentableItemsWarn(t *testing.T) {
	var buf bytes.Buffer
	c := New(&buf, true)
	if err := c.Begin(header()); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := c.Frame(canon.ItemFinished{Item: canon.LocalShellCall{ID: "s1", CallID: "call_s", Command: "ls"}}); err != nil {
		t.Fatalf("frame shell: %v", err)
	}
	if !hasWarning(c, WarnItemUnrepresentable) {
		t.Fatalf("warnings = %+v, want %s", c.Warnings(), WarnItemUnrepresentable)
	}
	if len(frames(t, &buf)) != 1 {
		t.Fatal("unrepresentable item must not emit a frame")
	}
}
