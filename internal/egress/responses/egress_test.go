package responses

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"prism/internal/canon"
	"prism/internal/egress"
	"prism/internal/execution"
	"prism/internal/provider"
)

type lockBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
	ch  chan time.Time
}

func newFakeClock(t0 time.Time) *fakeClock {
	return &fakeClock{now: t0, ch: make(chan time.Time, 16)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(time.Duration) <-chan time.Time { return c.ch }

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func (c *fakeClock) Fire() { c.ch <- c.Now() }

type frame struct {
	event string
	data  string
	raw   string
}

func parseFrames(t *testing.T, b *lockBuffer) []frame {
	t.Helper()
	var out []frame
	for _, raw := range strings.Split(b.String(), "\n\n") {
		if raw == "" {
			continue
		}
		f := frame{raw: raw}
		for _, line := range strings.Split(raw, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				f.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				f.data = strings.TrimPrefix(line, "data: ")
			}
		}
		out = append(out, f)
	}
	return out
}

func eventFrames(t *testing.T, b *lockBuffer) []frame {
	t.Helper()
	var out []frame
	for _, f := range parseFrames(t, b) {
		if f.event != "" {
			out = append(out, f)
		}
	}
	return out
}

func dataMap(t *testing.T, f frame) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(f.data), &m); err != nil {
		t.Fatalf("decode %s data %q: %v", f.event, f.data, err)
	}
	return m
}

func header() egress.ResponseHeader {
	return egress.ResponseHeader{ID: "resp_1", Model: "gpt-test", CreatedAt: time.Unix(1700000000, 0)}
}

func codexFacts() execution.Facts {
	return execution.Facts{Client: execution.ClientCodex, Session: "s", Thread: "t"}
}

func grokFacts() execution.Facts {
	return execution.Facts{Client: execution.ClientGrok, Session: "s", Thread: "t"}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not reached")
}

func seqOf(t *testing.T, f frame) int64 {
	t.Helper()
	m := dataMap(t, f)
	v, ok := m["sequence_number"].(float64)
	if !ok {
		t.Fatalf("event %s has no sequence_number", f.event)
	}
	return int64(v)
}

func TestBeginEmitsPreambleOnce(t *testing.T) {
	b := &lockBuffer{}
	e := NewWithClock(b, codexFacts(), newFakeClock(time.Unix(0, 0)))
	defer e.Close()
	if err := e.Begin(header()); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := e.Begin(header()); err == nil {
		t.Fatal("second begin must fail")
	}
	frames := eventFrames(t, b)
	if len(frames) != 2 {
		t.Fatalf("preamble frames = %d, want 2", len(frames))
	}
	if frames[0].event != "response.created" || frames[1].event != "response.in_progress" {
		t.Fatalf("preamble order = %s, %s", frames[0].event, frames[1].event)
	}
	if seqOf(t, frames[0]) != 0 || seqOf(t, frames[1]) != 1 {
		t.Fatalf("preamble sequences = %d, %d", seqOf(t, frames[0]), seqOf(t, frames[1]))
	}
	created := dataMap(t, frames[0])["response"].(map[string]any)
	if created["id"] != "resp_1" || created["status"] != "in_progress" || created["object"] != "response" {
		t.Fatalf("created snapshot = %v", created)
	}
	if got := e.Lifecycle().CommitState(); got != provider.ResponseStarted {
		t.Fatalf("commit state after begin = %d, want ResponseStarted", got)
	}
}

func TestPreambleOnceAcrossFailoverAttempts(t *testing.T) {
	b := &lockBuffer{}
	e := NewWithClock(b, codexFacts(), newFakeClock(time.Unix(0, 0)))
	defer e.Close()
	if err := e.Begin(header()); err != nil {
		t.Fatalf("begin: %v", err)
	}
	callID := canon.CallID("call_1")
	itemID := canon.ItemID("fc_1")
	for _, ev := range []canon.Event{
		canon.ItemStarted{Item: canon.FunctionCall{ID: itemID, CallID: callID, Name: "run"}},
		canon.ToolArgumentsDelta{ItemID: itemID, Bytes: []byte(`{"x"`)},
		canon.ToolArgumentsDelta{ItemID: itemID, Bytes: []byte(`:1}`)},
		canon.ItemFinished{Item: canon.FunctionCall{ID: itemID, CallID: callID, Name: "run", Arguments: []byte(`{"x":1}`)}},
		canon.TurnFinished{Status: canon.Completed(), Usage: canon.Usage{InputTokens: 5, OutputTokens: 7, TotalTokens: 12}},
	} {
		if err := e.Frame(ev); err != nil {
			t.Fatalf("frame %T: %v", ev, err)
		}
	}
	if err := e.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	created := 0
	for _, f := range eventFrames(t, b) {
		if f.event == "response.created" {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("response.created emitted %d times, want 1", created)
	}
	if !strings.Contains(b.String(), "data: [DONE]") {
		t.Fatal("missing [DONE]")
	}
}

func TestEventOrderAndSequenceNumbers(t *testing.T) {
	b := &lockBuffer{}
	e := NewWithClock(b, codexFacts(), newFakeClock(time.Unix(0, 0)))
	defer e.Close()
	msgID := canon.ItemID("msg_1")
	if err := e.Begin(header()); err != nil {
		t.Fatalf("begin: %v", err)
	}
	for _, ev := range []canon.Event{
		canon.ItemStarted{Item: canon.Message{ID: msgID, Role: canon.RoleAssistant}},
		canon.TextDelta{ItemID: msgID, Text: "hello "},
		canon.TextDelta{ItemID: msgID, Text: "world"},
		canon.ItemFinished{Item: canon.Message{ID: msgID, Role: canon.RoleAssistant, Content: []canon.Content{canon.TextContent{Text: "hello world"}}}},
		canon.TurnFinished{Status: canon.Completed(), Usage: canon.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}},
	} {
		if err := e.Frame(ev); err != nil {
			t.Fatalf("frame %T: %v", ev, err)
		}
	}
	if err := e.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	frames := parseFrames(t, b)
	want := []string{
		"response.created",
		"response.in_progress",
		"response.output_item.added",
		"response.content_part.added",
		"response.output_text.delta",
		"response.output_text.delta",
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",
		"response.completed",
	}
	if len(frames) != len(want)+1 {
		t.Fatalf("frames = %d, want %d (+DONE)", len(frames), len(want))
	}
	for i, name := range want {
		if frames[i].event != name {
			t.Fatalf("frame %d = %s, want %s", i, frames[i].event, name)
		}
	}
	if frames[len(frames)-1].data != "[DONE]" {
		t.Fatalf("last frame = %q, want [DONE]", frames[len(frames)-1].data)
	}
	for i := range want {
		if got := seqOf(t, frames[i]); got != int64(i) {
			t.Fatalf("frame %d (%s) sequence = %d, want %d", i, frames[i].event, got, i)
		}
	}
	completed := dataMap(t, frames[len(want)-1])["response"].(map[string]any)
	if completed["status"] != "completed" {
		t.Fatalf("completed status = %v", completed["status"])
	}
	output := completed["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("completed output = %d items", len(output))
	}
	item := output[0].(map[string]any)
	if item["type"] != "message" || item["status"] != "completed" {
		t.Fatalf("final item = %v", item)
	}
	content := item["content"].([]any)
	if len(content) != 1 || content[0].(map[string]any)["text"] != "hello world" {
		t.Fatalf("final content = %v", content)
	}
}

func TestCommitStateTransitions(t *testing.T) {
	b := &lockBuffer{}
	e := NewWithClock(b, codexFacts(), newFakeClock(time.Unix(0, 0)))
	defer e.Close()
	if got := e.Lifecycle().CommitState(); got != provider.NotStarted {
		t.Fatalf("initial commit state = %d, want NotStarted", got)
	}
	if err := e.Begin(header()); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if got := e.Lifecycle().CommitState(); got != provider.ResponseStarted {
		t.Fatalf("commit state after begin = %d, want ResponseStarted", got)
	}
	msgID := canon.ItemID("msg_1")
	if err := e.Frame(canon.ItemStarted{Item: canon.Message{ID: msgID, Role: canon.RoleAssistant}}); err != nil {
		t.Fatalf("frame: %v", err)
	}
	if got := e.Lifecycle().CommitState(); got != provider.OutputCommitted {
		t.Fatalf("commit state after output = %d, want OutputCommitted", got)
	}
	if err := e.Frame(canon.TurnFinished{Status: canon.Completed()}); err != nil {
		t.Fatalf("terminal: %v", err)
	}
	if err := e.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if got := e.Lifecycle().CommitState(); got != provider.OutputCommitted {
		t.Fatalf("commit state after terminal = %d, want OutputCommitted", got)
	}
}

func TestHeartbeatCodexAfterSilence(t *testing.T) {
	b := &lockBuffer{}
	clock := newFakeClock(time.Unix(0, 0))
	e := NewWithClock(b, codexFacts(), clock)
	defer e.Close()
	if err := e.Begin(header()); err != nil {
		t.Fatalf("begin: %v", err)
	}
	clock.Advance(time.Second)
	clock.Fire()
	time.Sleep(50 * time.Millisecond)
	if strings.Contains(b.String(), "response.heartbeat") {
		t.Fatal("heartbeat before two seconds of silence")
	}
	clock.Advance(1500 * time.Millisecond)
	clock.Fire()
	waitFor(t, func() bool { return strings.Contains(b.String(), "response.heartbeat") })
}

func TestHeartbeatCodexTyped(t *testing.T) {
	b := &lockBuffer{}
	clock := newFakeClock(time.Unix(0, 0))
	e := NewWithClock(b, codexFacts(), clock)
	defer e.Close()
	if err := e.Begin(header()); err != nil {
		t.Fatalf("begin: %v", err)
	}
	clock.Advance(2500 * time.Millisecond)
	clock.Fire()
	waitFor(t, func() bool { return strings.Contains(b.String(), "response.heartbeat") })
	frames := eventFrames(t, b)
	if frames[len(frames)-1].event != "response.heartbeat" {
		t.Fatalf("last event = %s, want response.heartbeat", frames[len(frames)-1].event)
	}
	if seqOf(t, frames[len(frames)-1]) != 2 {
		t.Fatalf("heartbeat sequence = %d, want 2", seqOf(t, frames[len(frames)-1]))
	}
	clock.Advance(3 * time.Second)
	clock.Fire()
	waitFor(t, func() bool { return strings.Count(b.String(), "response.heartbeat") >= 4 })
	seqs := []int64{}
	for _, f := range eventFrames(t, b) {
		seqs = append(seqs, seqOf(t, f))
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Fatalf("sequence not strictly monotonic: %v", seqs)
		}
	}
}

func TestHeartbeatGrokComment(t *testing.T) {
	b := &lockBuffer{}
	clock := newFakeClock(time.Unix(0, 0))
	e := NewWithClock(b, grokFacts(), clock)
	defer e.Close()
	if err := e.Begin(header()); err != nil {
		t.Fatalf("begin: %v", err)
	}
	clock.Advance(3 * time.Second)
	clock.Fire()
	waitFor(t, func() bool { return strings.Contains(b.String(), ": keep-alive") })
	for _, f := range eventFrames(t, b) {
		if f.event == "response.heartbeat" {
			t.Fatal("grok profile must not receive typed heartbeat")
		}
	}
	if !strings.Contains(b.String(), ": keep-alive\n\n") {
		t.Fatalf("missing keep-alive comment in %q", b.String())
	}
}

func TestNoHeartbeatAfterTerminal(t *testing.T) {
	b := &lockBuffer{}
	clock := newFakeClock(time.Unix(0, 0))
	e := NewWithClock(b, codexFacts(), clock)
	defer e.Close()
	if err := e.Begin(header()); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := e.Frame(canon.TurnFinished{Status: canon.Completed()}); err != nil {
		t.Fatalf("terminal: %v", err)
	}
	clock.Advance(10 * time.Second)
	clock.Fire()
	time.Sleep(50 * time.Millisecond)
	frames := eventFrames(t, b)
	if len(frames) != 2 {
		t.Fatalf("frames = %d, want 2 (no heartbeat after terminal)", len(frames))
	}
	if err := e.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	all := parseFrames(t, b)
	if all[len(all)-1].data != "[DONE]" {
		t.Fatalf("last frame = %q", all[len(all)-1].data)
	}
}

func TestUsageZeroDefaultsPresent(t *testing.T) {
	b := &lockBuffer{}
	e := NewWithClock(b, codexFacts(), newFakeClock(time.Unix(0, 0)))
	defer e.Close()
	if err := e.Begin(header()); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := e.Frame(canon.TurnFinished{Status: canon.Completed(), Usage: canon.Usage{InputTokens: 10, CachedInputTokens: 4, OutputTokens: 6, ReasoningTokens: 2, TotalTokens: 16}}); err != nil {
		t.Fatalf("terminal: %v", err)
	}
	if err := e.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	var completed frame
	for _, f := range eventFrames(t, b) {
		if f.event == "response.completed" {
			completed = f
		}
	}
	usage := dataMap(t, completed)["response"].(map[string]any)["usage"].(map[string]any)
	if usage["input_tokens"].(float64) != 10 || usage["output_tokens"].(float64) != 6 || usage["total_tokens"].(float64) != 16 {
		t.Fatalf("usage = %v", usage)
	}
	if usage["input_tokens_details"].(map[string]any)["cached_tokens"].(float64) != 4 {
		t.Fatalf("cached tokens = %v", usage["input_tokens_details"])
	}
	if usage["output_tokens_details"].(map[string]any)["reasoning_tokens"].(float64) != 2 {
		t.Fatalf("reasoning tokens = %v", usage["output_tokens_details"])
	}

	zero := &lockBuffer{}
	e2 := NewWithClock(zero, codexFacts(), newFakeClock(time.Unix(0, 0)))
	defer e2.Close()
	if err := e2.Begin(header()); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := e2.Frame(canon.TurnFinished{Status: canon.Completed()}); err != nil {
		t.Fatalf("terminal: %v", err)
	}
	if err := e2.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	for _, f := range eventFrames(t, zero) {
		if f.event != "response.completed" {
			continue
		}
		u := dataMap(t, f)["response"].(map[string]any)["usage"].(map[string]any)
		for _, key := range []string{"input_tokens", "output_tokens", "total_tokens"} {
			if _, ok := u[key]; !ok {
				t.Fatalf("usage missing %s: %v", key, u)
			}
		}
		details := u["input_tokens_details"].(map[string]any)
		if _, ok := details["cached_tokens"]; !ok {
			t.Fatalf("usage missing cached_tokens: %v", u)
		}
		if details["cached_tokens"].(float64) != 0 {
			t.Fatalf("cached tokens = %v, want 0", details["cached_tokens"])
		}
		reasoning := u["output_tokens_details"].(map[string]any)
		if _, ok := reasoning["reasoning_tokens"]; !ok {
			t.Fatalf("usage missing reasoning_tokens: %v", u)
		}
		if reasoning["reasoning_tokens"].(float64) != 0 {
			t.Fatalf("reasoning tokens = %v, want 0", reasoning["reasoning_tokens"])
		}
	}
}

func TestTerminalMapping(t *testing.T) {
	b := &lockBuffer{}
	e := NewWithClock(b, codexFacts(), newFakeClock(time.Unix(0, 0)))
	defer e.Close()
	if err := e.Begin(header()); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := e.Frame(canon.TurnFinished{Status: canon.Incomplete(canon.IncompleteMaxOutputTokens)}); err != nil {
		t.Fatalf("terminal: %v", err)
	}
	if err := e.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	var incomplete frame
	for _, f := range eventFrames(t, b) {
		if f.event == "response.incomplete" {
			incomplete = f
		}
	}
	if incomplete.event == "" {
		t.Fatal("missing response.incomplete")
	}
	resp := dataMap(t, incomplete)["response"].(map[string]any)
	if resp["status"] != "incomplete" {
		t.Fatalf("incomplete status = %v", resp["status"])
	}
	if resp["incomplete_details"].(map[string]any)["reason"] != "max_output_tokens" {
		t.Fatalf("incomplete details = %v", resp["incomplete_details"])
	}

	fb := &lockBuffer{}
	fe := NewWithClock(fb, codexFacts(), newFakeClock(time.Unix(0, 0)))
	defer fe.Close()
	if err := fe.Begin(header()); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := fe.Frame(canon.TurnFailed{Failure: canon.Failure{Reason: canon.FailRateLimited, Message: "upstream returned 429"}}); err != nil {
		t.Fatalf("terminal: %v", err)
	}
	if err := fe.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	var failed frame
	for _, f := range eventFrames(t, fb) {
		if f.event == "response.failed" {
			failed = f
		}
	}
	if failed.event == "" {
		t.Fatal("missing response.failed")
	}
	resp = dataMap(t, failed)["response"].(map[string]any)
	if resp["status"] != "failed" {
		t.Fatalf("failed status = %v", resp["status"])
	}
	errObj := resp["error"].(map[string]any)
	if errObj["code"] != "rate_limited" {
		t.Fatalf("error code = %v, want rate_limited", errObj["code"])
	}
	if errObj["message"] != "upstream returned 429" {
		t.Fatalf("error message = %v", errObj["message"])
	}
	if _, ok := resp["usage"].(map[string]any); !ok {
		t.Fatal("failed snapshot missing usage object")
	}
}

func TestItemVocabulary(t *testing.T) {
	b := &lockBuffer{}
	e := NewWithClock(b, codexFacts(), newFakeClock(time.Unix(0, 0)))
	defer e.Close()
	if err := e.Begin(header()); err != nil {
		t.Fatalf("begin: %v", err)
	}
	callID := canon.CallID("call_1")
	fnID := canon.ItemID("fc_1")
	customID := canon.ItemID("ctc_1")
	reasoningID := canon.ItemID("rs_1")
	shellID := canon.ItemID("shell_1")
	for _, ev := range []canon.Event{
		canon.ItemStarted{Item: canon.FunctionCall{ID: fnID, CallID: callID, Name: "run"}},
		canon.ItemStarted{Item: canon.CustomToolCall{ID: customID, CallID: callID, Name: "patch"}},
		canon.ItemStarted{Item: canon.ReasoningItem{ID: reasoningID, Summary: []canon.TextContent{{Text: "thinking"}}}},
		canon.ItemStarted{Item: canon.LocalShellCall{ID: shellID, CallID: callID, Command: "ls -la"}},
		canon.ToolArgumentsDelta{ItemID: fnID, Bytes: []byte(`{"a"`)},
		canon.CustomToolInputDelta{ItemID: customID, Text: "***"},
		canon.ReasoningDelta{ItemID: reasoningID, Text: "step"},
		canon.ItemFinished{Item: canon.FunctionCall{ID: fnID, CallID: callID, Name: "run", Arguments: []byte(`{"a":1}`)}},
		canon.ItemFinished{Item: canon.CustomToolCall{ID: customID, CallID: callID, Name: "patch", Input: "***"}},
		canon.ItemFinished{Item: canon.ReasoningItem{ID: reasoningID, Content: "step", Summary: []canon.TextContent{{Text: "thinking"}}}},
		canon.ItemFinished{Item: canon.LocalShellCall{ID: shellID, CallID: callID, Command: "ls -la"}},
		canon.TurnFinished{Status: canon.Completed()},
	} {
		if err := e.Frame(ev); err != nil {
			t.Fatalf("frame %T: %v", ev, err)
		}
	}
	if err := e.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	names := []string{}
	items := map[string]map[string]any{}
	for _, f := range eventFrames(t, b) {
		names = append(names, f.event)
		if f.event == "response.output_item.done" {
			item := dataMap(t, f)["item"].(map[string]any)
			items[item["id"].(string)] = item
		}
	}
	joined := strings.Join(names, ",")
	for _, want := range []string{
		"response.function_call_arguments.delta",
		"response.function_call_arguments.done",
		"response.custom_tool_call_input.delta",
		"response.custom_tool_call_input.done",
		"response.reasoning_text.delta",
		"response.reasoning_text.done",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in %s", want, joined)
		}
	}
	if fn := items["fc_1"]; fn == nil || fn["arguments"] != `{"a":1}` {
		t.Fatalf("function call item = %v", fn)
	}
	if ct := items["ctc_1"]; ct == nil || ct["input"] != "***" {
		t.Fatalf("custom tool item = %v", ct)
	}
	if rs := items["rs_1"]; rs == nil {
		t.Fatal("missing reasoning item")
	} else {
		summary := rs["summary"].([]any)
		if len(summary) != 1 || summary[0].(map[string]any)["text"] != "thinking" {
			t.Fatalf("reasoning summary = %v", rs["summary"])
		}
		content := rs["content"].([]any)
		if len(content) != 1 || content[0].(map[string]any)["text"] != "step" {
			t.Fatalf("reasoning content = %v", rs["content"])
		}
	}
	if sh := items["shell_1"]; sh == nil {
		t.Fatal("missing local shell item")
	} else {
		action := sh["action"].(map[string]any)
		cmd := action["command"].([]any)
		if len(cmd) != 1 || cmd[0] != "ls -la" {
			t.Fatalf("shell action = %v", action)
		}
	}
}

func TestContractErrors(t *testing.T) {
	b := &lockBuffer{}
	e := NewWithClock(b, codexFacts(), newFakeClock(time.Unix(0, 0)))
	defer e.Close()
	if err := e.Frame(canon.TurnFinished{Status: canon.Completed()}); err == nil {
		t.Fatal("frame before begin must fail")
	}
	if err := e.Begin(header()); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := e.Flush(); err == nil {
		t.Fatal("flush without terminal must fail")
	}
	if err := e.Frame(canon.TurnFinished{Status: canon.Completed()}); err != nil {
		t.Fatalf("terminal: %v", err)
	}
	if err := e.Frame(canon.TextDelta{ItemID: "x", Text: "y"}); err == nil {
		t.Fatal("frame after terminal must fail")
	}
	if err := e.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := e.Flush(); err == nil {
		t.Fatal("second flush must fail")
	}
	if err := e.Frame(canon.TurnFinished{Status: canon.Completed()}); err == nil {
		t.Fatal("frame after flush must fail")
	}
}

func TestDuplicateTerminal(t *testing.T) {
	b := &lockBuffer{}
	e := NewWithClock(b, codexFacts(), newFakeClock(time.Unix(0, 0)))
	defer e.Close()
	if err := e.Begin(header()); err != nil {
		t.Fatalf("begin: %v", err)
	}
	terminal := canon.TurnFinished{Status: canon.Completed(), Usage: canon.Usage{TotalTokens: 3}}
	if err := e.Frame(terminal); err != nil {
		t.Fatalf("terminal: %v", err)
	}
	if err := e.Frame(terminal); err != nil {
		t.Fatalf("identical terminal re-delivery: %v", err)
	}
	if err := e.Frame(canon.TurnFinished{Status: canon.Completed(), Usage: canon.Usage{TotalTokens: 9}}); err == nil {
		t.Fatal("conflicting terminal must fail")
	}
	if err := e.Frame(canon.TurnFailed{Failure: canon.Failure{Reason: canon.FailUnknown}}); err == nil {
		t.Fatal("conflicting terminal kind must fail")
	}
	if err := e.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	completed := 0
	failed := 0
	for _, f := range eventFrames(t, b) {
		switch f.event {
		case "response.completed":
			completed++
		case "response.failed":
			failed++
		}
	}
	if completed != 1 || failed != 0 {
		t.Fatalf("terminal frames: completed=%d failed=%d", completed, failed)
	}
}

func TestItemStateAvailableEmitsNothing(t *testing.T) {
	b := &lockBuffer{}
	e := NewWithClock(b, codexFacts(), newFakeClock(time.Unix(0, 0)))
	defer e.Close()
	if err := e.Begin(header()); err != nil {
		t.Fatalf("begin: %v", err)
	}
	before := len(parseFrames(t, b))
	if err := e.Frame(canon.ItemStateAvailable{ItemID: "msg_1", State: canon.OpaqueRef{Store: "s", Key: "k"}}); err != nil {
		t.Fatalf("state event: %v", err)
	}
	if after := len(parseFrames(t, b)); after != before {
		t.Fatalf("state event emitted %d frames", after-before)
	}
	if got := e.Lifecycle().CommitState(); got != provider.ResponseStarted {
		t.Fatalf("state event moved commit state to %d", got)
	}
}

func TestDeltaForUnknownItemFails(t *testing.T) {
	b := &lockBuffer{}
	e := NewWithClock(b, codexFacts(), newFakeClock(time.Unix(0, 0)))
	defer e.Close()
	if err := e.Begin(header()); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := e.Frame(canon.TextDelta{ItemID: "nope", Text: "x"}); err == nil {
		t.Fatal("delta for unknown item must fail")
	}
}

func TestUnsupportedOutputItemFails(t *testing.T) {
	b := &lockBuffer{}
	e := NewWithClock(b, codexFacts(), newFakeClock(time.Unix(0, 0)))
	defer e.Close()
	if err := e.Begin(header()); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := e.Frame(canon.ItemStarted{Item: canon.FunctionOutput{CallID: "c1"}}); err == nil {
		t.Fatal("unsupported output item must fail")
	}
}
