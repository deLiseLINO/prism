package conformance

import (
	"encoding/json"
	"fmt"
	"regexp"
)

type OpSpec struct {
	Name string
	Eval func(e *Evaluator, a Assertion) AssertionResult
}

type AssertionResult struct {
	ID              string
	Operator        string
	Required        bool
	Passed          bool
	ObservedSummary string
	Reason          FailReason
}

type Evaluator struct {
	Obs map[string]any
}

func (e *Evaluator) Evaluate(assertions []Assertion) []AssertionResult {
	results := make([]AssertionResult, 0, len(assertions))
	for _, a := range assertions {
		spec, ok := Registry[a.Operator]
		if !ok {
			results = append(results, AssertionResult{
				ID: a.ID, Operator: a.Operator, Required: a.Required, Passed: false,
				ObservedSummary: "unknown operator " + a.Operator, Reason: ReasonUnknownOperator,
			})
			continue
		}
		results = append(results, evalRecover(spec.Eval, e, a))
	}
	return results
}

func evalRecover(fn func(e *Evaluator, a Assertion) AssertionResult, e *Evaluator, a Assertion) (res AssertionResult) {
	defer func() {
		if r := recover(); r != nil {
			res = AssertionResult{
				ID: a.ID, Operator: a.Operator, Required: a.Required, Passed: false,
				ObservedSummary: fmt.Sprint(r), Reason: ReasonEvaluationError,
			}
		}
	}()
	return fn(e, a)
}

var Registry = map[string]OpSpec{
	"http_status_equals":      {Name: "http_status_equals", Eval: evalHTTPStatusEquals},
	"json_path_equals":        {Name: "json_path_equals", Eval: evalJSONPathEquals},
	"json_path_present":       {Name: "json_path_present", Eval: evalPresent},
	"json_path_absent":        {Name: "json_path_absent", Eval: evalAbsent},
	"sse_event_sequence":      {Name: "sse_event_sequence", Eval: evalEventSequence},
	"sse_event_count":         {Name: "sse_event_count", Eval: evalEventCount},
	"terminal_signal_equals":  {Name: "terminal_signal_equals", Eval: evalTerminalSignalEquals},
	"id_matches":              {Name: "id_matches", Eval: evalIDMatches},
	"id_stable_across_events": {Name: "id_stable_across_events", Eval: evalIDStable},
	"id_correlates":           {Name: "id_correlates", Eval: evalIDCorrelates},
	"tool_call_equals":        {Name: "tool_call_equals", Eval: evalJSONPathEquals},
	"tool_result_correlates":  {Name: "tool_result_correlates", Eval: evalToolResultCorrelates},
	"normalized_text_equals":  {Name: "normalized_text_equals", Eval: evalNormalizedTextEquals},
	"verifier_result_equals":  {Name: "verifier_result_equals", Eval: evalJSONPathEquals},
}

var IDGrammars = map[string]*regexp.Regexp{
	"responses_message":   regexp.MustCompile(`^msg_[A-Za-z0-9_-]{1,128}$`),
	"responses_reasoning": regexp.MustCompile(`^rs_[A-Za-z0-9_-]{1,128}$`),
	"responses_call":      regexp.MustCompile(`^call_[A-Za-z0-9_-]{1,128}$`),
	"nonempty_128":        regexp.MustCompile(`^[^\s]{1,128}$`),
}

func failResult(a Assertion, reason FailReason) AssertionResult {
	return AssertionResult{
		ID: a.ID, Operator: a.Operator, Required: a.Required, Passed: false,
		ObservedSummary: string(reason), Reason: reason,
	}
}

func evalEquals(e *Evaluator, a Assertion, pick func(obs map[string]any) any) AssertionResult {
	got := pick(e.Obs)
	passed := Equal(got, decodeJSON(a.Expected))
	reason := FailReason("")
	if !passed {
		reason = ReasonValueMismatch
	}
	return AssertionResult{
		ID: a.ID, Operator: a.Operator, Required: a.Required, Passed: passed,
		ObservedSummary: summarize(got), Reason: reason,
	}
}

func evalHTTPStatusEquals(e *Evaluator, a Assertion) AssertionResult {
	return evalEquals(e, a, func(obs map[string]any) any {
		client, _ := obs["client"].(map[string]any)
		response, _ := client["response"].(map[string]any)
		return response["status"]
	})
}

func evalJSONPathEquals(e *Evaluator, a Assertion) AssertionResult {
	resolved, ok := Resolve(e.Obs, a.Selector)
	if !ok {
		return failResult(a, ReasonSelectorMissing)
	}
	passed := Equal(resolved, decodeJSON(a.Expected))
	reason := FailReason("")
	if !passed {
		reason = ReasonValueMismatch
	}
	return AssertionResult{
		ID: a.ID, Operator: a.Operator, Required: a.Required, Passed: passed,
		ObservedSummary: summarize(resolved), Reason: reason,
	}
}

func evalPresent(e *Evaluator, a Assertion) AssertionResult {
	return evalPresence(e, a, true)
}

func evalAbsent(e *Evaluator, a Assertion) AssertionResult {
	return evalPresence(e, a, false)
}

func evalPresence(e *Evaluator, a Assertion, shouldExist bool) AssertionResult {
	exists := Exists(e.Obs, a.Selector)
	passed := exists == shouldExist
	summary := "absent"
	if exists {
		summary = "present"
	}
	reason := FailReason("")
	if !passed {
		if shouldExist {
			reason = ReasonSelectorMissing
		} else {
			reason = ReasonSelectorPresent
		}
	}
	return AssertionResult{
		ID: a.ID, Operator: a.Operator, Required: a.Required, Passed: passed,
		ObservedSummary: summary, Reason: reason,
	}
}

func eventNames(obs map[string]any) []string {
	client, _ := obs["client"].(map[string]any)
	response, _ := client["response"].(map[string]any)
	raw, _ := response["events"].([]any)
	names := make([]string, 0, len(raw))
	for _, ev := range raw {
		m, _ := ev.(map[string]any)
		name, _ := m["event"].(string)
		names = append(names, name)
	}
	return names
}

func evalEventSequence(e *Evaluator, a Assertion) AssertionResult {
	events := eventNames(e.Obs)
	expected := decodeJSON(a.Expected)
	arr, isArr := expected.([]any)
	passed := isArr && len(events) == len(arr)
	if passed {
		for i := range events {
			name, _ := arr[i].(string)
			if events[i] != name {
				passed = false
			}
		}
	}
	reason := FailReason("")
	if !passed {
		reason = ReasonEventSequenceMismatch
	}
	return AssertionResult{
		ID: a.ID, Operator: a.Operator, Required: a.Required, Passed: passed,
		ObservedSummary: summarize(events), Reason: reason,
	}
}

func evalEventCount(e *Evaluator, a Assertion) AssertionResult {
	spec := decodeJSON(a.Expected)
	var wantEvent string
	var wantCount float64
	if m, ok := spec.(map[string]any); ok {
		wantEvent, _ = m["event"].(string)
		wantCount, _ = m["count"].(float64)
	}
	count := 0
	for _, name := range eventNames(e.Obs) {
		if name == wantEvent {
			count++
		}
	}
	passed := float64(count) == wantCount
	reason := FailReason("")
	if !passed {
		reason = ReasonEventCountMismatch
	}
	return AssertionResult{
		ID: a.ID, Operator: a.Operator, Required: a.Required, Passed: passed,
		ObservedSummary: fmt.Sprint(count), Reason: reason,
	}
}

func evalTerminalSignalEquals(e *Evaluator, a Assertion) AssertionResult {
	return evalEquals(e, a, func(obs map[string]any) any {
		client, _ := obs["client"].(map[string]any)
		response, _ := client["response"].(map[string]any)
		return response["terminal"]
	})
}

func evalIDMatches(e *Evaluator, a Assertion) AssertionResult {
	resolved, ok := Resolve(e.Obs, a.Selector)
	if !ok {
		return failResult(a, ReasonSelectorMissing)
	}
	name, _ := decodeJSON(a.Expected).(string)
	value := ""
	if s, isStr := resolved.(string); isStr {
		value = s
	}
	grammar, has := IDGrammars[name]
	passed := has && grammar.MatchString(value)
	reason := FailReason("")
	if !passed {
		reason = ReasonIDGrammarMismatch
	}
	return AssertionResult{
		ID: a.ID, Operator: a.Operator, Required: a.Required, Passed: passed,
		ObservedSummary: value, Reason: reason,
	}
}

func evalIDStable(e *Evaluator, a Assertion) AssertionResult {
	expected := decodeJSON(a.Expected)
	pointers, isArr := expected.([]any)
	valid := isArr && len(pointers) >= 2
	if valid {
		for _, p := range pointers {
			if _, isStr := p.(string); !isStr {
				valid = false
			}
		}
	}
	if !valid {
		return AssertionResult{
			ID: a.ID, Operator: a.Operator, Required: a.Required, Passed: false,
			ObservedSummary: "expected at least two pointers", Reason: ReasonInvalidExpected,
		}
	}
	values := make([]string, 0, len(pointers))
	for _, p := range pointers {
		resolved, ok := Resolve(e.Obs, p.(string))
		if !ok {
			return failResult(a, ReasonSelectorMissing)
		}
		s, isStr := resolved.(string)
		if !isStr {
			return AssertionResult{
				ID: a.ID, Operator: a.Operator, Required: a.Required, Passed: false,
				ObservedSummary: "identifier must be a string", Reason: ReasonSelectorTypeMismatch,
			}
		}
		values = append(values, s)
	}
	passed := true
	for _, v := range values {
		if v != values[0] {
			passed = false
		}
	}
	reason := FailReason("")
	if !passed {
		reason = ReasonIDNotStable
	}
	return AssertionResult{
		ID: a.ID, Operator: a.Operator, Required: a.Required, Passed: passed,
		ObservedSummary: summarize(values), Reason: reason,
	}
}

func correlatedIDs(left, right any) bool {
	l, lok := left.(string)
	r, rok := right.(string)
	return lok && rok && l != "" && r != "" && l == r
}

func evalIDCorrelates(e *Evaluator, a Assertion) AssertionResult {
	expected := decodeJSON(a.Expected)
	pointers, isArr := expected.([]any)
	valid := isArr && len(pointers) == 2
	if valid {
		for _, p := range pointers {
			if _, isStr := p.(string); !isStr {
				valid = false
			}
		}
	}
	if !valid {
		return AssertionResult{
			ID: a.ID, Operator: a.Operator, Required: a.Required, Passed: false,
			ObservedSummary: "expected two pointers", Reason: ReasonInvalidExpected,
		}
	}
	left, lok := Resolve(e.Obs, pointers[0].(string))
	right, rok := Resolve(e.Obs, pointers[1].(string))
	if !lok || !rok {
		return failResult(a, ReasonSelectorMissing)
	}
	passed := correlatedIDs(left, right)
	reason := FailReason("")
	if !passed {
		reason = ReasonIDCorrelationMismatch
	}
	return AssertionResult{
		ID: a.ID, Operator: a.Operator, Required: a.Required, Passed: passed,
		ObservedSummary: fmt.Sprintf("%s vs %s", summarize(left), summarize(right)), Reason: reason,
	}
}

func evalToolResultCorrelates(e *Evaluator, a Assertion) AssertionResult {
	spec := decodeJSON(a.Expected)
	m, isMap := spec.(map[string]any)
	callPtr, callOK := m["call"].(string)
	resultPtr, resultOK := m["result"].(string)
	if !isMap || !callOK || !resultOK {
		return AssertionResult{
			ID: a.ID, Operator: a.Operator, Required: a.Required, Passed: false,
			ObservedSummary: "expected call/result pointers", Reason: ReasonInvalidExpected,
		}
	}
	call, cok := Resolve(e.Obs, callPtr)
	result, rok := Resolve(e.Obs, resultPtr)
	if !cok || !rok {
		return failResult(a, ReasonSelectorMissing)
	}
	passed := correlatedIDs(call, result)
	reason := FailReason("")
	if !passed {
		reason = ReasonToolResultCorrelationMismatch
	}
	return AssertionResult{
		ID: a.ID, Operator: a.Operator, Required: a.Required, Passed: passed,
		ObservedSummary: fmt.Sprintf("%s -> %s", summarize(call), summarize(result)), Reason: reason,
	}
}

func evalNormalizedTextEquals(e *Evaluator, a Assertion) AssertionResult {
	return evalEquals(e, a, func(obs map[string]any) any {
		client, _ := obs["client"].(map[string]any)
		response, _ := client["response"].(map[string]any)
		return response["normalizedText"]
	})
}

func summarize(v any) string {
	if s, isStr := v.(string); isStr {
		if len(s) > 120 {
			return s[:120] + "…"
		}
		return s
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	if len(raw) > 200 {
		return string(raw[:200]) + "…"
	}
	return string(raw)
}
