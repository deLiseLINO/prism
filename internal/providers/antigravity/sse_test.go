package antigravity

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
)

func decodeFixture(t *testing.T, name string) []canon.Event {
	t.Helper()
	data, err := os.ReadFile("fixtures/" + name)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var events []canon.Event
	err = DecodeStream(strings.NewReader(string(data)+"\n"), func(e canon.Event) error {
		events = append(events, e)
		return nil
	})
	if err != nil {
		t.Fatalf("DecodeStream: %v", err)
	}
	return events
}

func countTerminals(events []canon.Event) int {
	n := 0
	for _, e := range events {
		switch e.(type) {
		case canon.TurnFinished, canon.TurnFailed:
			n++
		}
	}
	return n
}

func TestDecodeStreamTextFixture(t *testing.T) {
	events := decodeFixture(t, "stream-text.sse")
	if countTerminals(events) != 1 {
		t.Fatalf("expected exactly one terminal event, got %d: %v", countTerminals(events), events)
	}
	var text strings.Builder
	for _, e := range events {
		if delta, ok := e.(canon.TextDelta); ok {
			text.WriteString(delta.Text)
		}
	}
	if text.String() != "Hello world" {
		t.Fatalf("text deltas = %q", text.String())
	}
	last := events[len(events)-1].(canon.TurnFinished)
	if last.Status.Kind() != canon.StatusCompleted {
		t.Fatalf("status = %v, want completed", last.Status.Kind())
	}
	if last.Usage.InputTokens != 11 || last.Usage.OutputTokens != 4 ||
		last.Usage.CachedInputTokens != 3 || last.Usage.ReasoningTokens != 7 {
		t.Fatalf("usage = %+v", last.Usage)
	}
	if last.Usage.TotalTokens != 15 {
		t.Fatalf("total tokens = %d", last.Usage.TotalTokens)
	}
}

func TestDecodeStreamThinkingFixture(t *testing.T) {
	events := decodeFixture(t, "stream-thinking.sse")
	if countTerminals(events) != 1 {
		t.Fatalf("expected exactly one terminal event, got %d: %v", countTerminals(events), events)
	}
	var reasoning, text strings.Builder
	var reasoningItem, messageItem bool
	for _, e := range events {
		switch ev := e.(type) {
		case canon.ItemStarted:
			switch it := ev.Item.(type) {
			case canon.ReasoningItem:
				if it.ID != "assistant-0-reasoning" {
					t.Fatalf("reasoning item id = %q", it.ID)
				}
				reasoningItem = true
			case canon.Message:
				if it.ID != "assistant-0" {
					t.Fatalf("message item id = %q", it.ID)
				}
				messageItem = true
			}
		case canon.ReasoningDelta:
			reasoning.WriteString(ev.Text)
		case canon.TextDelta:
			text.WriteString(ev.Text)
		case canon.ItemFinished:
			if it, ok := ev.Item.(canon.ReasoningItem); ok && it.Signature == "" {
				t.Fatalf("reasoning item finished without signature")
			}
		}
	}
	if !reasoningItem || !messageItem {
		t.Fatalf("missing reasoning or message item: %v %v", reasoningItem, messageItem)
	}
	if reasoning.String() != "budget-ok" {
		t.Fatalf("reasoning deltas = %q", reasoning.String())
	}
	if text.String() != "budget-ok" {
		t.Fatalf("text deltas = %q", text.String())
	}
	// reasoning must close before the message opens, so egress never sees a
	// thinking delta against a text block
	var sawMessageStart, sawReasoningFinish bool
	for _, e := range events {
		switch ev := e.(type) {
		case canon.ItemStarted:
			if _, ok := ev.Item.(canon.Message); ok {
				if sawReasoningFinish != true {
					t.Fatalf("message started before reasoning finished")
				}
				sawMessageStart = true
			}
		case canon.ItemFinished:
			if _, ok := ev.Item.(canon.ReasoningItem); ok {
				sawReasoningFinish = true
			}
		}
	}
	_ = sawMessageStart
	last := events[len(events)-1].(canon.TurnFinished)
	if last.Status.Kind() != canon.StatusCompleted {
		t.Fatalf("status = %v, want completed", last.Status.Kind())
	}
}

func TestDecodeStreamToolCallFixture(t *testing.T) {
	events := decodeFixture(t, "stream-toolcall.sse")
	if countTerminals(events) != 1 {
		t.Fatalf("expected exactly one terminal event, got %d", countTerminals(events))
	}
	var reasoningSeen bool
	var finished *canon.FunctionCall
	for _, e := range events {
		switch v := e.(type) {
		case canon.ReasoningDelta:
			reasoningSeen = true
		case canon.ItemFinished:
			if call, ok := v.Item.(canon.FunctionCall); ok {
				finished = &call
			}
		}
	}
	if !reasoningSeen {
		t.Fatal("thought part must decode to a reasoning delta")
	}
	if finished == nil {
		t.Fatal("function call item missing")
	}
	if finished.Name != "get_weather" || finished.CallID != "call_1" {
		t.Fatalf("function call = %+v", finished)
	}
	if string(finished.Arguments) != `{"city":"Paris"}` {
		t.Fatalf("arguments = %s", finished.Arguments)
	}
	if finished.State.Store != "antigravity" || !likelyRealSignature(finished.State.Key) {
		t.Fatalf("thought signature must ride the call state: %+v", finished.State)
	}
	last := events[len(events)-1].(canon.TurnFinished)
	if last.Usage.OutputTokens != 9 {
		t.Fatalf("usage after final chunk = %+v", last.Usage)
	}
}

func TestDecodeStreamOneTerminalInvariant(t *testing.T) {
	for _, name := range []string{"stream-text.sse", "stream-toolcall.sse"} {
		events := decodeFixture(t, name)
		if got := countTerminals(events); got != 1 {
			t.Errorf("%s: terminals = %d, want 1", name, got)
		}
	}
}

func TestDecodeStreamErrorFrame(t *testing.T) {
	stream := "data: {\"error\":{\"message\":\"upstream boom\"}}\n\n"
	var events []canon.Event
	err := DecodeStream(strings.NewReader(stream), func(e canon.Event) error { events = append(events, e); return nil })
	var re provider.RunError
	if !errors.As(err, &re) || re.Kind != provider.TerminalEmitted {
		t.Fatalf("DecodeStream: %v", err)
	}
	if countTerminals(events) != 1 {
		t.Fatalf("terminals = %d", countTerminals(events))
	}
	failed := events[0].(canon.TurnFailed)
	if failed.Failure.Reason != canon.FailOriginRejected || !strings.Contains(failed.Failure.Message, "upstream boom") {
		t.Fatalf("turn failed = %+v", failed)
	}
}

func TestDecodeStreamMissingWrapper(t *testing.T) {
	stream := "data: {\"candidates\":[]}\n\n"
	var events []canon.Event
	_ = DecodeStream(strings.NewReader(stream), func(e canon.Event) error {
		events = append(events, e)
		return nil
	})
	if failed, ok := lastTerminal(events).(canon.TurnFailed); !ok || failed.Failure.Reason != canon.FailUpstreamTransport {
		t.Fatalf("terminal = %v", lastTerminal(events))
	}
}

func lastTerminal(events []canon.Event) canon.Event {
	var last canon.Event
	for _, e := range events {
		switch e.(type) {
		case canon.TurnFinished, canon.TurnFailed:
			last = e
		}
	}
	return last
}

func TestDecodeStreamNoTerminalSignal(t *testing.T) {
	stream := "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"partial\"}]}}]}}\n\n"
	var events []canon.Event
	_ = DecodeStream(strings.NewReader(stream), func(e canon.Event) error {
		events = append(events, e)
		return nil
	})
	if failed, ok := lastTerminal(events).(canon.TurnFailed); !ok || failed.Failure.Reason != canon.FailUpstreamTransport {
		t.Fatalf("terminal = %v", lastTerminal(events))
	}
	if countTerminals(events) != 1 {
		t.Fatalf("terminals = %d, want 1", countTerminals(events))
	}
	closed := false
	for _, e := range events {
		if f, ok := e.(canon.ItemFinished); ok {
			if _, isMsg := f.Item.(canon.Message); isMsg {
				closed = true
			}
		}
	}
	if !closed {
		t.Fatal("open message must be closed before the synthesized terminal")
	}
}

func TestDecodeStreamEOFGate(t *testing.T) {
	for _, stream := range []string{
		`{"response":{"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}}`,
		`{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"read","args":{}}}]}}]}}`,
		`{"response":{"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1}}}`,
		`{"response":{"candidates":[{"finishReason":"STOP"}]}}`,
	} {
		var events []canon.Event
		payload := "data: " + stream + "\n\n"
		if strings.Contains(stream, "STOP") {
			payload = strings.TrimSuffix(payload, "\n")
		}
		err := DecodeStream(strings.NewReader(payload), func(e canon.Event) error { events = append(events, e); return nil })
		var re provider.RunError
		if !errors.As(err, &re) || re.Kind != provider.TerminalEmitted || re.Class != provider.ClassTransport {
			t.Fatalf("error = %v", err)
		}
		if _, ok := lastTerminal(events).(canon.TurnFailed); !ok || countTerminals(events) != 1 {
			t.Fatalf("events = %v", events)
		}
	}
}

func TestDecodeStreamMalformedFrame(t *testing.T) {
	stream := "data: {not json}\n\n"
	var events []canon.Event
	_ = DecodeStream(strings.NewReader(stream), func(e canon.Event) error {
		events = append(events, e)
		return nil
	})
	if countTerminals(events) != 1 {
		t.Fatalf("expected one terminal, got %d", countTerminals(events))
	}
	failed := events[0].(canon.TurnFailed)
	if failed.Failure.Reason != canon.FailUpstreamTransport {
		t.Fatalf("reason = %v", failed.Failure.Reason)
	}
}

func TestDecodeStreamTruncatedToolTurn(t *testing.T) {
	stream := "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"name\":\"f\",\"args\":{}}}]}}]}}\n\n" +
		"data: {\"response\":{\"candidates\":[{\"finishReason\":\"MAX_TOKENS\"}]}}\n\n"
	var events []canon.Event
	_ = DecodeStream(strings.NewReader(stream), func(e canon.Event) error {
		events = append(events, e)
		return nil
	})
	failed, ok := events[len(events)-1].(canon.TurnFailed)
	if !ok {
		t.Fatalf("truncated tool turn must fail closed, got %T", events[len(events)-1])
	}
	if !strings.Contains(failed.Failure.Message, "MAX_TOKENS") {
		t.Fatalf("message = %q", failed.Failure.Message)
	}
}

func TestDecodeStreamFinishReasonMapping(t *testing.T) {
	cases := []struct {
		reason     string
		want       canon.StatusKind
		wantInc    canon.IncompleteReason
		wantIncSet bool
	}{
		{"STOP", canon.StatusCompleted, 0, false},
		{"MAX_TOKENS", canon.StatusIncomplete, canon.IncompleteMaxOutputTokens, true},
		{"SAFETY", canon.StatusIncomplete, canon.IncompleteContentFilter, true},
		{"PROHIBITED_CONTENT", canon.StatusIncomplete, canon.IncompleteContentFilter, true},
	}
	for _, tc := range cases {
		payload, _ := json.Marshal(map[string]any{
			"response": map[string]any{
				"candidates": []any{map[string]any{"finishReason": tc.reason}},
			},
		})
		stream := "data: " + string(payload) + "\n\n"
		var events []canon.Event
		_ = DecodeStream(strings.NewReader(stream), func(e canon.Event) error {
			events = append(events, e)
			return nil
		})
		finished := events[len(events)-1].(canon.TurnFinished)
		if finished.Status.Kind() != tc.want {
			t.Errorf("%s: kind = %v", tc.reason, finished.Status.Kind())
		}
		if tc.wantIncSet {
			reason, _ := finished.Status.Reason()
			if reason != tc.wantInc {
				t.Errorf("%s: incomplete reason = %v", tc.reason, reason)
			}
		}
	}
}
