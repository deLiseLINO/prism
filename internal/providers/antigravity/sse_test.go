package antigravity

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"prism/internal/canon"
)

func decodeFixture(t *testing.T, name string) []canon.Event {
	t.Helper()
	data, err := os.ReadFile("fixtures/" + name)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var events []canon.Event
	err = DecodeStream(strings.NewReader(string(data)), func(e canon.Event) error {
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
	if err := DecodeStream(strings.NewReader(stream), func(e canon.Event) error {
		events = append(events, e)
		return nil
	}); err != nil {
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
	failed, ok := lastTerminal(events).(canon.TurnFailed)
	if !ok {
		t.Fatalf("expected TurnFailed, got %T", lastTerminal(events))
	}
	if failed.Failure.Reason != canon.FailUpstreamTransport {
		t.Fatalf("reason = %v", failed.Failure.Reason)
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
	last, ok := lastTerminal(events).(canon.TurnFailed)
	if !ok {
		t.Fatalf("expected TurnFailed, got %T", lastTerminal(events))
	}
	if last.Failure.Reason != canon.FailUpstreamTransport {
		t.Fatalf("reason = %v", last.Failure.Reason)
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
	if failed.Failure.Reason != canon.FailOriginRejected {
		t.Fatalf("reason = %v", failed.Failure.Reason)
	}
}

func TestDecodeStreamTruncatedToolTurn(t *testing.T) {
	stream := "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"name\":\"f\",\"args\":{}}}]}}]}}\n" +
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
