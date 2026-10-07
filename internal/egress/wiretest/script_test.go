package wiretest

import (
	"strings"

	"github.com/deLiseLINO/prism/internal/canon"
)

type toolWant struct {
	CallID string
	Name   string
	Args   string
}

type want struct {
	Text      string
	Reasoning string
	Tools     []toolWant
	// Stop is the normalised client-visible stop class: end, tool, length, filter.
	Stop   string
	Failed bool
	Usage  canon.Usage
}

type scenario struct {
	Name   string
	Events []canon.Event
	Want   want
	// SkipResponses/SkipChat/SkipMessages name wires whose vocabulary cannot
	// express the scenario; the gap is asserted elsewhere.
	Skip map[string]bool
}

func msg(id, text string) canon.Message {
	m := canon.Message{ID: canon.ItemID(id), Role: canon.RoleAssistant}
	if text != "" {
		m.Content = []canon.Content{canon.TextContent{Text: text}}
	}
	return m
}

func fn(id, call, name, args string) canon.FunctionCall {
	return canon.FunctionCall{ID: canon.ItemID(id), CallID: canon.CallID(call), Name: canon.ToolName(name), Arguments: []byte(args)}
}

func chunks(s string, n int) []string {
	var out []string
	r := []rune(s)
	for len(r) > 0 {
		k := n
		if k > len(r) {
			k = len(r)
		}
		out = append(out, string(r[:k]))
		r = r[k:]
	}
	return out
}

func textTurn(id string, parts ...string) []canon.Event {
	evs := []canon.Event{canon.ItemStarted{Item: msg(id, "")}}
	for _, p := range parts {
		evs = append(evs, canon.TextDelta{ItemID: canon.ItemID(id), Text: p})
	}
	return append(evs, canon.ItemFinished{Item: msg(id, strings.Join(parts, ""))})
}

func scenarios() []scenario {
	usage := canon.Usage{InputTokens: 120, OutputTokens: 30, CachedInputTokens: 100, ReasoningTokens: 12, TotalTokens: 150}
	big := strings.Repeat("данные-日本語-🙂 ", 20000)
	done := canon.TurnFinished{Status: canon.Completed()}
	withUsage := canon.TurnFinished{Status: canon.Completed(), Usage: usage}
	return []scenario{
		{Name: "text", Events: append(textTurn("m1", "Hel", "lo ", "wörld"), withUsage),
			Want: want{Text: "Hello wörld", Stop: "end", Usage: usage}},
		{Name: "unicode-large", Events: append(textTurn("m1", chunks(big, 4096)...), done),
			Want: want{Text: big, Stop: "end"}},
		{Name: "empty-content", Events: append(textTurn("m1"), done), Want: want{Stop: "end"}},
		{Name: "no-output", Events: []canon.Event{done}, Want: want{Stop: "end"}},
		{Name: "reasoning-then-text",
			Events: append(append([]canon.Event{
				canon.ItemStarted{Item: canon.ReasoningItem{ID: "r1"}},
				canon.ReasoningDelta{ItemID: "r1", Text: "think "},
				canon.ReasoningDelta{ItemID: "r1", Text: "hard"},
				canon.ItemFinished{Item: canon.ReasoningItem{ID: "r1", Content: "think hard", Signature: "sig-abc"}},
			}, textTurn("m1", "answer")...), withUsage),
			Want: want{Text: "answer", Reasoning: "think hard", Stop: "end", Usage: usage}},
		{Name: "single-tool", Events: []canon.Event{
			canon.ItemStarted{Item: fn("f1", "call_1", "lookup", "")},
			canon.ToolArgumentsDelta{ItemID: "f1", Bytes: []byte(`{"q":`)},
			canon.ToolArgumentsDelta{ItemID: "f1", Bytes: []byte(`"é"}`)},
			canon.ItemFinished{Item: fn("f1", "call_1", "lookup", `{"q":"é"}`)},
			withUsage},
			Want: want{Tools: []toolWant{{"call_1", "lookup", `{"q":"é"}`}}, Stop: "tool", Usage: usage}},
		{Name: "parallel-tools-interleaved", Events: []canon.Event{
			canon.ItemStarted{Item: fn("f1", "call_1", "a", "")},
			canon.ItemStarted{Item: fn("f2", "call_2", "b", "")},
			canon.ToolArgumentsDelta{ItemID: "f1", Bytes: []byte(`{"x"`)},
			canon.ToolArgumentsDelta{ItemID: "f2", Bytes: []byte(`{"y"`)},
			canon.ToolArgumentsDelta{ItemID: "f1", Bytes: []byte(`:1}`)},
			canon.ToolArgumentsDelta{ItemID: "f2", Bytes: []byte(`:2}`)},
			canon.ItemFinished{Item: fn("f1", "call_1", "a", `{"x":1}`)},
			canon.ItemFinished{Item: fn("f2", "call_2", "b", `{"y":2}`)},
			withUsage},
			Want: want{Tools: []toolWant{{"call_1", "a", `{"x":1}`}, {"call_2", "b", `{"y":2}`}}, Stop: "tool", Usage: usage}},
		{Name: "text-then-tool", Events: append(append(textTurn("m1", "let me check"), []canon.Event{
			canon.ItemStarted{Item: fn("f1", "call_1", "lookup", "")},
			canon.ToolArgumentsDelta{ItemID: "f1", Bytes: []byte(`{}`)},
			canon.ItemFinished{Item: fn("f1", "call_1", "lookup", `{}`)},
		}...), done),
			Want: want{Text: "let me check", Tools: []toolWant{{"call_1", "lookup", `{}`}}, Stop: "tool"}},
		{Name: "tool-without-deltas", Events: []canon.Event{
			canon.ItemStarted{Item: fn("f1", "call_1", "lookup", "")},
			canon.ItemFinished{Item: fn("f1", "call_1", "lookup", `{"q":1}`)},
			done},
			Want: want{Tools: []toolWant{{"call_1", "lookup", `{"q":1}`}}, Stop: "tool"}},
		{Name: "finish-only-text-and-tool", Events: []canon.Event{
			canon.ItemStarted{Item: msg("m1", "")},
			canon.ItemFinished{Item: msg("m1", "whole answer")},
			canon.ItemStarted{Item: fn("f1", "call_1", "lookup", "")},
			canon.ItemFinished{Item: fn("f1", "call_1", "lookup", `{"q":1}`)},
			done},
			Want: want{Text: "whole answer", Tools: []toolWant{{"call_1", "lookup", `{"q":1}`}}, Stop: "tool"}},
		{Name: "finish-only-reasoning", Events: []canon.Event{
			canon.ItemStarted{Item: canon.ReasoningItem{ID: "r1"}},
			canon.ItemFinished{Item: canon.ReasoningItem{ID: "r1", Content: "deep thought", Signature: "sig-1"}},
			done},
			Want: want{Reasoning: "deep thought", Stop: "end"}},
		{Name: "terminal-snapshot-suffix", Events: []canon.Event{
			canon.ItemStarted{Item: msg("m1", "")},
			canon.TextDelta{ItemID: "m1", Text: "prefix"},
			canon.ItemFinished{Item: msg("m1", "prefix suffix")},
			canon.ItemStarted{Item: fn("f1", "call_1", "lookup", "")},
			canon.ToolArgumentsDelta{ItemID: "f1", Bytes: []byte(`{"id":`)},
			canon.ItemFinished{Item: fn("f1", "call_1", "lookup", `{"id":42}`)},
			done},
			Want: want{Text: "prefix suffix", Tools: []toolWant{{"call_1", "lookup", `{"id":42}`}}, Stop: "tool"}},
		{Name: "max-tokens", Events: append(textTurn("m1", "cut off"), canon.TurnFinished{Status: canon.Incomplete(canon.IncompleteMaxOutputTokens), Usage: usage}),
			Want: want{Text: "cut off", Stop: "length", Usage: usage}},
		{Name: "content-filter", Events: append(textTurn("m1", "part"), canon.TurnFinished{Status: canon.Incomplete(canon.IncompleteContentFilter)}),
			Want: want{Text: "part", Stop: "filter"}},
		{Name: "upstream-stall-mid-text", Events: []canon.Event{
			canon.ItemStarted{Item: msg("m1", "")},
			canon.TextDelta{ItemID: "m1", Text: "partial"},
			canon.TurnFinished{Status: canon.Incomplete(canon.IncompleteUpstreamStall)}},
			Want: want{Text: "partial", Stop: "stall"}},
		{Name: "failed-mid-text", Events: []canon.Event{
			canon.ItemStarted{Item: msg("m1", "")},
			canon.TextDelta{ItemID: "m1", Text: "partial"},
			canon.TurnFailed{Failure: canon.Failure{Reason: canon.FailServerOverloaded, Message: "overloaded"}}},
			Want: want{Failed: true}},
		{Name: "failed-mid-tool", Events: []canon.Event{
			canon.ItemStarted{Item: fn("f1", "call_1", "a", "")},
			canon.ToolArgumentsDelta{ItemID: "f1", Bytes: []byte(`{"x"`)},
			canon.TurnFailed{Failure: canon.Failure{Reason: canon.FailUpstreamTransport, Message: "reset"}}},
			Want: want{Failed: true}},
		{Name: "failed-before-output", Events: []canon.Event{
			canon.TurnFailed{Failure: canon.Failure{Reason: canon.FailRateLimited, Message: "slow down"}}},
			Want: want{Failed: true}},
		{Name: "failed-provider-error-passthrough", Events: []canon.Event{
			canon.TurnFailed{Failure: canon.Failure{Reason: canon.FailUnknown, Message: "bad",
				Provider: &canon.ProviderError{Error: []byte(`{"message":"bad","type":"invalid_request_error","code":"x"}`)}}}},
			Want: want{Failed: true}},
	}
}
