package conformance

import (
	"encoding/json"
	"strings"
	"testing"
)

func testObservationJSON() string {
	return `{
		"client": {
			"request": {"status": 0, "headers": {}, "json": null, "rawBytes": 0},
			"response": {
				"status": 200,
				"headers": {"content-type": "text/event-stream"},
				"json": null,
				"events": [
					{"event": "response.output_text.delta", "data": {"delta": "hel"}, "ordinal": 0},
					{"event": "response.output_text.delta", "data": {"delta": "lo"}, "ordinal": 1},
					{"event": "response.completed", "data": {}, "ordinal": 2}
				],
				"toolCalls": [
					{"id": "call_abc_123", "name": "lookup", "arguments": {"q": "x"}, "kind": "function", "ordinal": 0},
					{"id": "call_def_456", "name": "lookup", "arguments": {"q": "y"}, "kind": "function", "ordinal": 1}
				],
				"mcpCalls": [],
				"terminal": "finished",
				"normalizedText": "hello"
			}
		},
		"upstream": {
			"requests": [
				{"status": 0, "headers": {}, "json": {"model": "fixture-model"}, "rawBytes": 25}
			],
			"responses": []
		},
		"process": {"exitCode": null},
		"verifiers": {
			"firstId": "msg_1", "secondId": "msg_1", "thirdId": "msg_2", "resultId": "call_abc_123", "emptyId": ""
		}
	}`
}

func evaluator(t *testing.T, obsJSON string) *Evaluator {
	t.Helper()
	obs := decodeJSON([]byte(obsJSON))
	m, ok := obs.(map[string]any)
	if !ok {
		t.Fatal("observation must decode to an object")
	}
	return &Evaluator{Obs: m}
}

func assertion(op, selector string, expected string, required bool) Assertion {
	return Assertion{ID: "t", Operator: op, Selector: selector, Expected: json.RawMessage(expected), Required: required}
}

func TestRegistryClosedSet(t *testing.T) {
	want := []string{
		"http_status_equals", "json_path_equals", "json_path_present", "json_path_absent",
		"sse_event_sequence", "sse_event_count", "terminal_signal_equals", "id_matches",
		"id_stable_across_events", "id_correlates", "tool_call_equals", "tool_result_correlates",
		"normalized_text_equals", "verifier_result_equals",
	}
	if len(Registry) != len(want) {
		t.Fatalf("registry size %d want %d", len(Registry), len(want))
	}
	for _, name := range want {
		if _, ok := Registry[name]; !ok {
			t.Fatalf("registry missing %s", name)
		}
	}
}

func TestOperatorsPass(t *testing.T) {
	e := evaluator(t, testObservationJSON())
	cases := []Assertion{
		assertion("http_status_equals", "", `200`, true),
		assertion("json_path_equals", "/upstream/requests/0/json/model", `"fixture-model"`, true),
		assertion("json_path_present", "/client/response/terminal", `null`, true),
		assertion("json_path_absent", "/client/response/nope", `null`, true),
		assertion("sse_event_sequence", "", `["response.output_text.delta","response.output_text.delta","response.completed"]`, true),
		assertion("sse_event_count", "", `{"event":"response.output_text.delta","count":2}`, true),
		assertion("terminal_signal_equals", "", `"finished"`, true),
		assertion("id_matches", "/client/response/toolCalls/0/id", `"responses_call"`, true),
		assertion("id_matches", "/client/response/toolCalls/0/id", `"nonempty_128"`, true),
		assertion("id_stable_across_events", "", `["/verifiers/firstId","/verifiers/secondId"]`, true),
		assertion("id_correlates", "", `["/verifiers/firstId","/verifiers/secondId"]`, true),
		assertion("tool_call_equals", "/client/response/toolCalls/0/arguments", `{"q":"x"}`, true),
		assertion("tool_result_correlates", "", `{"call":"/client/response/toolCalls/0/id","result":"/verifiers/resultId"}`, true),
		assertion("normalized_text_equals", "", `"hello"`, true),
		assertion("verifier_result_equals", "/verifiers/firstId", `"msg_1"`, true),
	}
	results := e.Evaluate(cases)
	for i, res := range results {
		if !res.Passed {
			t.Fatalf("case %d %s: expected pass, got %s (%s)", i, cases[i].Operator, res.Reason, res.ObservedSummary)
		}
		if res.Reason != "" {
			t.Fatalf("case %d %s: reason must be empty on pass, got %s", i, cases[i].Operator, res.Reason)
		}
	}
}

func TestOperatorsFailReasons(t *testing.T) {
	e := evaluator(t, testObservationJSON())
	cases := []struct {
		a    Assertion
		want FailReason
	}{
		{assertion("http_status_equals", "", `404`, true), ReasonValueMismatch},
		{assertion("json_path_equals", "/upstream/requests/0/json/model", `"nope"`, true), ReasonValueMismatch},
		{assertion("json_path_equals", "/upstream/requests/9/json/model", `"x"`, true), ReasonSelectorMissing},
		{assertion("json_path_present", "/client/response/nope", `null`, true), ReasonSelectorMissing},
		{assertion("json_path_absent", "/client/response/terminal", `null`, true), ReasonSelectorPresent},
		{assertion("sse_event_sequence", "", `["response.output_text.delta","response.completed"]`, true), ReasonEventSequenceMismatch},
		{assertion("sse_event_sequence", "", `["response.completed","response.output_text.delta","response.output_text.delta"]`, true), ReasonEventSequenceMismatch},
		{assertion("sse_event_count", "", `{"event":"response.output_text.delta","count":3}`, true), ReasonEventCountMismatch},
		{assertion("terminal_signal_equals", "", `"failed"`, true), ReasonValueMismatch},
		{assertion("id_matches", "/client/response/toolCalls/0/id", `"responses_message"`, true), ReasonIDGrammarMismatch},
		{assertion("id_matches", "/client/response/toolCalls/0/id", `"no_such_grammar"`, true), ReasonIDGrammarMismatch},
		{assertion("id_matches", "/client/response/nope", `"responses_call"`, true), ReasonSelectorMissing},
		{assertion("id_stable_across_events", "", `["/verifiers/firstId","/verifiers/thirdId"]`, true), ReasonIDNotStable},
		{assertion("id_stable_across_events", "", `["/verifiers/firstId"]`, true), ReasonInvalidExpected},
		{assertion("id_stable_across_events", "", `["/client/response/toolCalls/0/arguments","/verifiers/firstId"]`, true), ReasonSelectorTypeMismatch},
		{assertion("id_stable_across_events", "", `["/client/response/nope","/verifiers/firstId"]`, true), ReasonSelectorMissing},
		{assertion("id_correlates", "", `["/verifiers/firstId","/verifiers/thirdId"]`, true), ReasonIDCorrelationMismatch},
		{assertion("id_correlates", "", `["/verifiers/firstId","/verifiers/secondId","/verifiers/thirdId"]`, true), ReasonInvalidExpected},
		{assertion("id_correlates", "", `["/client/response/nope","/verifiers/secondId"]`, true), ReasonSelectorMissing},
		{assertion("id_correlates", "", `["/verifiers/firstId","/verifiers/emptyId"]`, true), ReasonIDCorrelationMismatch},
		{assertion("id_correlates", "", `["/verifiers/firstId",""]`, true), ReasonSelectorMissing},
		{assertion("tool_call_equals", "/client/response/toolCalls/0/arguments", `{"q":"z"}`, true), ReasonValueMismatch},
		{assertion("tool_call_equals", "/client/response/toolCalls/5/arguments", `{}`, true), ReasonSelectorMissing},
		{assertion("tool_result_correlates", "", `{"call":"/client/response/toolCalls/0/id","result":"/verifiers/thirdId"}`, true), ReasonToolResultCorrelationMismatch},
		{assertion("tool_result_correlates", "", `{"call":"/client/response/toolCalls/0/id"}`, true), ReasonInvalidExpected},
		{assertion("tool_result_correlates", "", `{"call":"/client/response/nope","result":"/verifiers/resultId"}`, true), ReasonSelectorMissing},
		{assertion("normalized_text_equals", "", `"goodbye"`, true), ReasonValueMismatch},
		{assertion("verifier_result_equals", "/verifiers/firstId", `"msg_2"`, true), ReasonValueMismatch},
	}
	results := e.Evaluate(func() []Assertion {
		out := make([]Assertion, 0, len(cases))
		for _, c := range cases {
			out = append(out, c.a)
		}
		return out
	}())
	for i, c := range cases {
		if results[i].Passed {
			t.Fatalf("case %d %s: expected fail", i, c.a.Operator)
		}
		if results[i].Reason != c.want {
			t.Fatalf("case %d %s: reason %s want %s", i, c.a.Operator, results[i].Reason, c.want)
		}
	}
}

func TestUnknownOperator(t *testing.T) {
	e := evaluator(t, testObservationJSON())
	res := e.Evaluate([]Assertion{assertion("no_such_operator", "", `null`, true)})[0]
	if res.Passed || res.Reason != ReasonUnknownOperator {
		t.Fatalf("unknown operator: %+v", res)
	}
	if !strings.Contains(res.ObservedSummary, "no_such_operator") {
		t.Fatalf("summary %s", res.ObservedSummary)
	}
}

func TestEvaluationErrorRecovered(t *testing.T) {
	e := evaluator(t, testObservationJSON())
	a := Assertion{ID: "t", Operator: "sse_event_sequence", Expected: json.RawMessage(`"not-an-array"`)}
	res := e.Evaluate([]Assertion{a})[0]
	if res.Passed || res.Reason != ReasonEventSequenceMismatch {
		t.Fatalf("non-array expected must fail as sequence mismatch: %+v", res)
	}
}

func TestSummarizeTruncation(t *testing.T) {
	long := strings.Repeat("x", 130)
	if got := summarize(long); len(got) != 123 {
		t.Fatalf("string truncation len %d want 123 (120 + ellipsis)", len(got))
	}
	if !strings.HasSuffix(summarize(long), "…") {
		t.Fatal("string truncation must append ellipsis")
	}
	big := make(map[string]any, 0)
	big["k"] = strings.Repeat("y", 250)
	if got := summarize(big); len(got) != 203 {
		t.Fatalf("json truncation len %d want 203 (200 + ellipsis)", len(got))
	}
}

func TestClosedObservationShape(t *testing.T) {
	obs := Empty()
	raw, err := obs.JSON()
	if err != nil {
		t.Fatal(err)
	}
	got := decodeJSON(raw).(map[string]any)
	client := got["client"].(map[string]any)
	response := client["response"].(map[string]any)
	upstream := got["upstream"].(map[string]any)
	for _, key := range []string{"events", "toolCalls", "mcpCalls"} {
		if arr, ok := response[key].([]any); !ok || len(arr) != 0 {
			t.Fatalf("%s must be an empty array", key)
		}
	}
	if v, ok := response["terminal"]; !ok || v != nil {
		t.Fatalf("terminal must be null, got %v", v)
	}
	if v, ok := response["normalizedText"]; !ok || v != "" {
		t.Fatalf("normalizedText must be empty string, got %v", v)
	}
	if reqs, ok := upstream["requests"].([]any); !ok || len(reqs) != 0 {
		t.Fatalf("upstream.requests must be empty array")
	}
	if v, ok := got["process"].(map[string]any)["exitCode"]; !ok || v != nil {
		t.Fatalf("process.exitCode must be null")
	}
	if v, ok := got["verifiers"].(map[string]any); !ok || len(v) != 0 {
		t.Fatalf("verifiers must be empty object")
	}
}

func TestRecordUpstreamRequest(t *testing.T) {
	obs := Empty()
	req := &UpstreamRequest{
		Method: "POST",
		URL:    "https://api.openai.com/v1/chat/completions",
		Headers: Headers{
			{Name: "Content-Type", Value: "application/json"},
			{Name: "Authorization", Value: "Bearer fixture-key"},
		},
		Body: []byte(`{"model":"fixture-model"}`),
	}
	RecordUpstreamRequest(obs, req)
	raw, err := obs.JSON()
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeJSON(raw).(map[string]any)
	reqs := doc["upstream"].(map[string]any)["requests"].([]any)
	if len(reqs) != 1 {
		t.Fatalf("requests %d", len(reqs))
	}
	rec := reqs[0].(map[string]any)
	if rec["status"] != float64(0) || rec["rawBytes"] != float64(25) {
		t.Fatalf("record %+v", rec)
	}
	hdrs := rec["headers"].(map[string]any)
	if hdrs["content-type"] != "application/json" || hdrs["authorization"] != "Bearer fixture-key" {
		t.Fatalf("recorded headers %v", hdrs)
	}
	json, ok := rec["json"].(map[string]any)
	if !ok || json["model"] != "fixture-model" {
		t.Fatalf("recorded json %v", rec["json"])
	}
}

func TestHeadersGet(t *testing.T) {
	hs := Headers{{Name: "Content-Type", Value: "application/json"}, {Name: "X-A", Value: "1"}}
	if v, ok := hs.Get("content-type"); !ok || v != "application/json" {
		t.Fatalf("case-insensitive Get failed: %v %v", v, ok)
	}
	if _, ok := hs.Get("x-b"); ok {
		t.Fatal("missing header reported present")
	}
}
