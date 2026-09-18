package customresponses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"prism/internal/account"
	"prism/internal/canon"
	"prism/internal/execution"
	"prism/internal/provider"
	"prism/internal/stream"
)

func staticKey(ctx context.Context, target provider.Target, lease account.Lease) (string, error) {
	return "sk-test", nil
}

func testTarget(baseURL string) provider.Target {
	return provider.Target{
		Provider:  "responses",
		Wire:      provider.WireResponses,
		BaseURL:   baseURL,
		APIKeyRef: "key-ref",
	}
}

func testRequest(stream bool) canon.Request {
	params := jsonRaw(`{"type":"object"}`)
	temp := 0.2
	return canon.Request{
		Model:           "gpt-5.2",
		Stream:          stream,
		Instructions:    []canon.Content{canon.TextContent{Text: "be brief"}},
		Input:           []canon.Item{canon.Message{Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "hi"}}}},
		MaxOutputTokens: 128,
		Reasoning:       canon.ReasoningConfig{Effort: canon.EffortHigh},
		Sampling:        canon.Sampling{Temperature: &temp},
		Tools:           []canon.Tool{canon.FunctionTool{Name: "get_weather", Description: "weather", Parameters: params, Strict: true}},
		ToolChoice:      canon.ToolAuto{},
	}
}

func jsonRaw(s string) []byte {
	return []byte(s)
}

func TestExtendedEffortsReachResponsesWire(t *testing.T) {
	for effort, want := range map[canon.ReasoningEffort]string{
		canon.EffortXHigh: "xhigh",
		canon.EffortMax:   "max",
		canon.EffortOff:   "off",
	} {
		req := testRequest(false)
		req.Reasoning = canon.ReasoningConfig{Effort: effort}
		r := New(staticKey, Options{})
		up, err := r.buildUpstream(testTarget("https://example.com/v1/"), "sk-test", req, execution.Facts{})
		if err != nil {
			t.Fatalf("buildUpstream(effort %d): %v", effort, err)
		}
		var got map[string]any
		if err := json.Unmarshal(up.Body, &got); err != nil {
			t.Fatalf("parse body: %v", err)
		}
		reasoning, ok := got["reasoning"].(map[string]any)
		if !ok || reasoning["effort"] != want {
			t.Fatalf("effort %d wire = %#v, want %q", effort, got["reasoning"], want)
		}
	}
}

func TestBuildUpstreamRequest(t *testing.T) {
	r := New(staticKey, Options{ExtraHeaders: []Header{{Name: "X-Custom", Value: "v1"}}})
	up, err := r.buildUpstream(testTarget("https://example.com/v1/"), "sk-test", testRequest(true), execution.Facts{})
	if err != nil {
		t.Fatalf("buildUpstream: %v", err)
	}
	if up.URL != "https://example.com/v1/responses" {
		t.Fatalf("url = %q", up.URL)
	}
	if len(up.Headers) != 3 {
		t.Fatalf("headers = %v", up.Headers)
	}
	if up.Headers[0].Name != "Content-Type" || up.Headers[0].Value != "application/json" {
		t.Fatalf("header 0 = %v", up.Headers[0])
	}
	if up.Headers[1].Name != "Authorization" || up.Headers[1].Value != "Bearer sk-test" {
		t.Fatalf("header 1 = %v", up.Headers[1])
	}
	if up.Headers[2].Name != "X-Custom" || up.Headers[2].Value != "v1" {
		t.Fatalf("header 2 = %v", up.Headers[2])
	}
	want := `{"model":"gpt-5.2","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":true,"instructions":"be brief","max_output_tokens":128,"temperature":0.2,"reasoning":{"effort":"high"},"tools":[{"type":"function","name":"get_weather","description":"weather","parameters":{"type":"object"},"strict":true}],"tool_choice":"auto"}`
	if string(up.Body) != want {
		t.Fatalf("body =\n%s\nwant\n%s", up.Body, want)
	}
}

func TestAssistantHistoryUsesOutputText(t *testing.T) {
	r := New(staticKey, Options{})
	req := testRequest(true)
	req.Input = []canon.Item{
		canon.Message{Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "hi"}}},
		canon.Message{Role: canon.RoleAssistant, Content: []canon.Content{canon.TextContent{Text: "on it"}}},
		canon.Message{Role: canon.RoleAssistant, Content: []canon.Content{canon.TextContent{Text: ""}}},
	}
	up, err := r.buildUpstream(testTarget("https://example.com/v1/"), "sk-test", req, execution.Facts{})
	if err != nil {
		t.Fatalf("buildUpstream: %v", err)
	}
	var raw struct {
		Input []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(up.Body, &raw); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if len(raw.Input) != 2 {
		t.Fatalf("input has %d items, want 2", len(raw.Input))
	}
	got := raw.Input[1].Content
	if len(got) != 1 || got[0].Type != "output_text" {
		t.Fatalf("assistant content = %+v, want single output_text", got)
	}
}

func TestResponsesURLVariants(t *testing.T) {
	cases := map[string]string{
		"https://example.com":              "https://example.com/v1/responses",
		"https://example.com/":             "https://example.com/v1/responses",
		"https://example.com/v1":           "https://example.com/v1/responses",
		"https://example.com/v1/":          "https://example.com/v1/responses",
		"https://example.com/v1/responses": "https://example.com/v1/responses",
	}
	for base, want := range cases {
		if got := responsesURL(base); got != want {
			t.Fatalf("responsesURL(%q) = %q, want %q", base, got, want)
		}
	}
	if got := modelsURL("https://example.com/v1/"); got != "https://example.com/v1/models" {
		t.Fatalf("modelsURL = %q", got)
	}
}

func TestStreamToCanonEvents(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotAuth = req.Header.Get("Authorization")
		if req.URL.Path != "/v1/responses" {
			t.Errorf("path = %q", req.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": ping\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.created\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\",\"id\":\"msg_1\",\"role\":\"assistant\",\"content\":[]}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":\"he\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":\"llo\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"id\":\"msg_1\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"reasoning\",\"id\":\"rs_1\",\"summary\":[]}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.reasoning_summary_text.delta\",\"item_id\":\"rs_1\",\"delta\":\"thinking\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"reasoning\",\"id\":\"rs_1\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"thinking\"}]}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"get_weather\",\"arguments\":\"\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"delta\":\"{\\\"city\\\":\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"delta\":\"\\\"sf\\\"}\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"get_weather\",\"arguments\":\"{\\\"city\\\":\\\"sf\\\"}\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":3,\"output_tokens\":5,\"total_tokens\":8,\"input_tokens_details\":{\"cached_tokens\":1},\"output_tokens_details\":{\"reasoning_tokens\":2}}}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":3,\"output_tokens\":5,\"total_tokens\":8}}}\n\n")
	}))
	defer srv.Close()

	events := &collector{}
	r := New(staticKey, Options{})
	err := r.Run(context.Background(), provider.RunRequest{Request: testRequest(true), Target: testTarget(srv.URL)}, events)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("auth = %q", gotAuth)
	}
	if events.TerminalCount() != 1 {
		t.Fatalf("terminals = %d", events.TerminalCount())
	}
	var kinds []string
	for _, ev := range events.All() {
		kinds = append(kinds, fmt.Sprintf("%T", ev))
	}
	want := strings.Join([]string{
		"canon.ItemStarted", "canon.TextDelta", "canon.TextDelta", "canon.ItemFinished",
		"canon.ItemStarted", "canon.ReasoningDelta", "canon.ItemFinished",
		"canon.ItemStarted", "canon.ToolArgumentsDelta", "canon.ToolArgumentsDelta", "canon.ItemFinished",
		"canon.TurnFinished",
	}, ",")
	if strings.Join(kinds, ",") != want {
		t.Fatalf("events =\n%s\nwant\n%s", strings.Join(kinds, ","), want)
	}
	finish := events.All()[len(events.All())-1].(canon.TurnFinished)
	if finish.Status.Kind() != canon.StatusCompleted {
		t.Fatalf("status = %d", finish.Status.Kind())
	}
	if finish.Usage != (canon.Usage{InputTokens: 3, OutputTokens: 5, CachedInputTokens: 1, ReasoningTokens: 2, TotalTokens: 8}) {
		t.Fatalf("usage = %+v", finish.Usage)
	}
	msg := events.All()[3].(canon.ItemFinished).Item.(canon.Message)
	if len(msg.Content) != 1 || msg.Content[0].(canon.TextContent).Text != "hello" {
		t.Fatalf("message = %+v", msg)
	}
	fc := events.All()[10].(canon.ItemFinished).Item.(canon.FunctionCall)
	if fc.CallID != "call_1" || fc.Name != "get_weather" || string(fc.Arguments) != `{"city":"sf"}` {
		t.Fatalf("function call = %+v", fc)
	}
	tracker := stream.NewTracker()
	for _, ev := range events.All() {
		if err := tracker.Apply(ev); err != nil {
			t.Fatalf("tracker apply %T: %v", ev, err)
		}
	}
	if _, ok := tracker.Terminal(); !ok {
		t.Fatal("tracker has no terminal")
	}
}

func TestStreamCustomToolDelta(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"custom_tool_call\",\"id\":\"ct_1\",\"call_id\":\"call_9\",\"name\":\"render\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.custom_tool_call_input.delta\",\"item_id\":\"ct_1\",\"delta\":\"<x>\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"custom_tool_call\",\"id\":\"ct_1\",\"call_id\":\"call_9\",\"name\":\"render\",\"input\":\"<x>y</x>\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{}}}\n\n")
	}))
	defer srv.Close()
	events := &collector{}
	r := New(staticKey, Options{})
	if err := r.Run(context.Background(), provider.RunRequest{Request: testRequest(true), Target: testTarget(srv.URL)}, events); err != nil {
		t.Fatalf("Run: %v", err)
	}
	ct := events.All()[2].(canon.ItemFinished).Item.(canon.CustomToolCall)
	if ct.Input != "<x>y</x>" || ct.CallID != "call_9" || ct.Name != "render" {
		t.Fatalf("custom tool call = %+v", ct)
	}
}

func TestStreamIncompleteAndFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"},\"usage\":{\"input_tokens\":1,\"output_tokens\":2,\"total_tokens\":3}}}\n\n")
	}))
	defer srv.Close()
	events := &collector{}
	r := New(staticKey, Options{})
	if err := r.Run(context.Background(), provider.RunRequest{Request: testRequest(true), Target: testTarget(srv.URL)}, events); err != nil {
		t.Fatalf("Run: %v", err)
	}
	finish := events.All()[0].(canon.TurnFinished)
	reason, ok := finish.Status.Reason()
	if !ok || reason != canon.IncompleteMaxOutputTokens {
		t.Fatalf("status = %+v", finish.Status)
	}

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprint(w, "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"boom\",\"code\":\"server_error\"}}}\n\n")
	}))
	defer srv2.Close()
	events2 := &collector{}
	r2 := New(staticKey, Options{})
	err := r2.Run(context.Background(), provider.RunRequest{Request: testRequest(true), Target: testTarget(srv2.URL)}, events2)
	var runErr provider.RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("want RunError, got %v", err)
	}
	if runErr.Kind != provider.TerminalEmitted || !runErr.Accepted {
		t.Fatalf("runErr = %+v", runErr)
	}
	if events2.TerminalCount() != 1 {
		t.Fatalf("terminals = %d", events2.TerminalCount())
	}
	failed := events2.All()[0].(canon.TurnFailed)
	if failed.Failure.Message != "boom" {
		t.Fatalf("failure = %+v", failed)
	}
}

func TestStreamEOFWithoutTerminal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\",\"id\":\"msg_1\",\"role\":\"assistant\",\"content\":[]}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":\"partial\"}\n\n")
	}))
	defer srv.Close()
	events := &collector{}
	r := New(staticKey, Options{})
	err := r.Run(context.Background(), provider.RunRequest{Request: testRequest(true), Target: testTarget(srv.URL)}, events)
	var runErr provider.RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("want RunError, got %v", err)
	}
	if runErr.Kind != provider.TerminalOmitted || runErr.Class != provider.ClassTransport {
		t.Fatalf("runErr = %+v", runErr)
	}
}

func TestNonStreamingAggregation(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ = ioReadAll(req.Body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"resp_1","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"hello there"}]}],"usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14,"input_tokens_details":{"cached_tokens":2},"output_tokens_details":{"reasoning_tokens":1}}}`)
	}))
	defer srv.Close()
	events := &collector{}
	r := New(staticKey, Options{})
	if err := r.Run(context.Background(), provider.RunRequest{Request: testRequest(false), Target: testTarget(srv.URL)}, events); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(string(body), `"stream":false`) {
		t.Fatalf("body = %s", body)
	}
	kinds := eventKinds(events)
	want := []string{"canon.ItemStarted", "canon.ItemFinished", "canon.TurnFinished"}
	if fmt.Sprint(kinds) != fmt.Sprint(want) {
		t.Fatalf("kinds = %v", kinds)
	}
	msg := events.All()[1].(canon.ItemFinished).Item.(canon.Message)
	if msg.Content[0].(canon.TextContent).Text != "hello there" {
		t.Fatalf("message = %+v", msg)
	}
	finish := events.All()[2].(canon.TurnFinished)
	if finish.Usage != (canon.Usage{InputTokens: 10, OutputTokens: 4, CachedInputTokens: 2, ReasoningTokens: 1, TotalTokens: 14}) {
		t.Fatalf("usage = %+v", finish.Usage)
	}
}

func TestNonStreamingAggregationWithoutUsageDetails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"resp_2","status":"completed","output":[{"type":"message","id":"msg_2","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}`)
	}))
	defer srv.Close()
	events := &collector{}
	r := New(staticKey, Options{})
	if err := r.Run(context.Background(), provider.RunRequest{Request: testRequest(false), Target: testTarget(srv.URL)}, events); err != nil {
		t.Fatalf("Run: %v", err)
	}
	finish := events.All()[2].(canon.TurnFinished)
	if finish.Usage != (canon.Usage{InputTokens: 3, OutputTokens: 1, TotalTokens: 4}) {
		t.Fatalf("usage = %+v", finish.Usage)
	}
}

func TestUpstreamErrorMapping(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		retryAfter string
		class      provider.ErrorClass
		retry      time.Duration
	}{
		{"unauthorized", 401, `{"error":{"message":"bad key","code":"invalid_api_key"}}`, "", provider.ClassUnauthorized, 0},
		{"rate limited", 429, `{"error":{"message":"slow down","code":"rate_limit_exceeded"}}`, "7", provider.ClassRateLimited, 7 * time.Second},
		{"rate limited date", 429, `{"error":{"message":"slow down"}}`, "", provider.ClassRateLimited, 0},
		{"context length", 400, `{"error":{"message":"too long","code":"context_length_exceeded"}}`, "", provider.ClassContextLength, 0},
		{"invalid request", 400, `{"error":{"message":"bad"}}`, "", provider.ClassInvalidRequest, 0},
		{"not found", 404, `{"error":{"message":"no model"}}`, "", provider.ClassNotFound, 0},
		{"server", 503, `{"error":{"message":"overloaded","code":"server_is_overloaded"}}`, "2", provider.ClassServer, 2 * time.Second},
		{"malformed body", 500, `<html>oops</html>`, "", provider.ClassServer, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if tc.retryAfter != "" {
					w.Header().Set("Retry-After", tc.retryAfter)
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			r := New(staticKey, Options{})
			err := r.Run(context.Background(), provider.RunRequest{Request: testRequest(false), Target: testTarget(srv.URL)}, &collector{})
			var runErr provider.RunError
			if !errors.As(err, &runErr) {
				t.Fatalf("want RunError, got %v", err)
			}
			if runErr.Class != tc.class {
				t.Fatalf("class = %d, want %d", runErr.Class, tc.class)
			}
			if runErr.Kind != provider.Retryable || !runErr.ReplaySafe {
				t.Fatalf("runErr = %+v", runErr)
			}
			if runErr.RetryAfter != tc.retry {
				t.Fatalf("retryAfter = %s, want %s", runErr.RetryAfter, tc.retry)
			}
		})
	}
}

func TestHostedToolDeclarationsNotRejected(t *testing.T) {
	r := New(staticKey, Options{})
	req := canon.Request{
		Model:  "gpt-5.2",
		Stream: true,
		Input:  []canon.Item{canon.Message{Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "hi"}}}},
		Tools: []canon.Tool{
			canon.FunctionTool{Name: "f"},
			canon.CustomToolDef{Name: "c", Format: canon.FormatGrammar, Grammar: &canon.ToolGrammar{Syntax: "lark", Definition: "start: WORD"}},
		},
		ToolChoice: canon.ToolNamed{Name: "f"},
	}
	up, err := r.buildUpstream(testTarget("https://example.com/v1"), "sk", req, execution.Facts{})
	if err != nil {
		t.Fatalf("buildUpstream: %v", err)
	}
	want := `{"model":"gpt-5.2","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":true,"tools":[{"type":"function","name":"f"},{"type":"custom","name":"c","format":{"type":"grammar","syntax":"lark","definition":"start: WORD"}}],"tool_choice":{"name":"f","type":"function"}}`
	if string(up.Body) != want {
		t.Fatalf("body =\n%s\nwant\n%s", up.Body, want)
	}
}
func TestExtraHeadersApplied(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		got = req.Header.Clone()
		w.WriteHeader(500)
	}))
	defer srv.Close()
	r := New(staticKey, Options{ExtraHeaders: []Header{{Name: "X-Org", Value: "acme"}, {Name: "X-Trace", Value: "t-1"}}})
	r.Run(context.Background(), provider.RunRequest{Request: testRequest(false), Target: testTarget(srv.URL)}, &collector{})
	if got.Get("X-Org") != "acme" || got.Get("X-Trace") != "t-1" {
		t.Fatalf("headers = %v", got)
	}
	if got.Get("Authorization") != "Bearer sk-test" {
		t.Fatalf("auth = %q", got.Get("Authorization"))
	}
}

func TestCatalog(t *testing.T) {
	r := New(staticKey, Options{Models: []provider.Model{{ID: "gpt-5.2"}}})
	models, err := r.Catalog(testTarget("https://example.com/v1"), account.Lease{}).Models(context.Background())
	if err != nil {
		t.Fatalf("static models: %v", err)
	}
	if len(models) != 1 || models[0].ID != "gpt-5.2" {
		t.Fatalf("models = %v", models)
	}

	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotAuth = req.Header.Get("Authorization")
		gotPath = req.URL.Path
		fmt.Fprint(w, `{"object":"list","data":[{"id":"m-1"},{"id":"m-2"}]}`)
	}))
	defer srv.Close()
	live := New(staticKey, Options{})
	models, err = live.Catalog(testTarget(srv.URL), account.Lease{}).Models(context.Background())
	if err != nil {
		t.Fatalf("live models: %v", err)
	}
	if gotPath != "/v1/models" || gotAuth != "Bearer sk-test" {
		t.Fatalf("path=%q auth=%q", gotPath, gotAuth)
	}
	if len(models) != 2 || models[1].ID != "m-2" {
		t.Fatalf("models = %v", models)
	}

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprint(w, `not json`)
	}))
	defer srv2.Close()
	_, err = New(staticKey, Options{}).Catalog(testTarget(srv2.URL), account.Lease{}).Models(context.Background())
	if err == nil {
		t.Fatal("want error for malformed models response")
	}
}

func TestRegistryRegistration(t *testing.T) {
	registry := provider.NewRegistry()
	r := New(staticKey, Options{})
	if err := registry.Register("responses", r); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := registry.Register("responses", r); err == nil {
		t.Fatal("want duplicate registration error")
	}
	got, ok := registry.Lookup("responses")
	if !ok || got != provider.Runner(r) {
		t.Fatalf("lookup = %v %v", got, ok)
	}
}

func TestTargetValidation(t *testing.T) {
	r := New(staticKey, Options{})
	bad := testTarget("https://example.com")
	bad.Wire = provider.WireCodex
	err := r.Run(context.Background(), provider.RunRequest{Request: testRequest(false), Target: bad}, &collector{})
	var runErr provider.RunError
	if !errors.As(err, &runErr) || runErr.Class != provider.ClassInvalidRequest {
		t.Fatalf("err = %v", err)
	}
	noURL := testTarget("")
	err = r.Run(context.Background(), provider.RunRequest{Request: testRequest(false), Target: noURL}, &collector{})
	if !errors.As(err, &runErr) || runErr.Class != provider.ClassInvalidRequest {
		t.Fatalf("err = %v", err)
	}
}

func TestUnsupportedItemIsTypedError(t *testing.T) {
	r := New(staticKey, Options{})
	_, err := buildBody(canon.Request{Input: []canon.Item{canon.CompactionMarker{ID: "c1"}}})
	if err == nil || !strings.Contains(err.Error(), "unsupported canonical item") {
		t.Fatalf("err = %v", err)
	}
	_ = r
}

type collector struct {
	events    []canon.Event
	terminals int
}

func (c *collector) Emit(ev canon.Event) error {
	switch ev.(type) {
	case canon.TurnFinished, canon.TurnFailed:
		c.terminals++
	}
	c.events = append(c.events, ev)
	return nil
}

func (c *collector) All() []canon.Event { return c.events }
func (c *collector) TerminalCount() int { return c.terminals }

func eventKinds(c *collector) []string {
	var out []string
	for _, ev := range c.All() {
		out = append(out, fmt.Sprintf("%T", ev))
	}
	return out
}

func ioReadAll(r io.Reader) ([]byte, error) {
	return io.ReadAll(r)
}

func TestEmptyAPIKeyRefSendsRequestWithoutAuthorization(t *testing.T) {
	calls := 0
	noKey := func(ctx context.Context, target provider.Target, lease account.Lease) (string, error) {
		calls++
		return "", nil
	}
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotAuth = req.Header.Get("Authorization")
		gotPath = req.URL.Path
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"resp_noauth","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	}))
	defer srv.Close()
	target := testTarget(srv.URL)
	target.APIKeyRef = ""
	r := New(noKey, Options{})
	if err := r.Run(context.Background(), provider.RunRequest{Request: testRequest(false), Target: target}, &collector{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if calls != 0 {
		t.Fatalf("resolver called %d times for empty api key ref", calls)
	}
	if gotAuth != "" {
		t.Fatalf("authorization header sent: %q", gotAuth)
	}
	if gotPath != "/v1/responses" {
		t.Fatalf("path = %q", gotPath)
	}
}

func TestEmptyResolvedKeyWithRefFailsLoud(t *testing.T) {
	calls := 0
	emptyKey := func(ctx context.Context, target provider.Target, lease account.Lease) (string, error) {
		calls++
		return "", nil
	}
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"resp_empty","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	}))
	defer srv.Close()
	r := New(emptyKey, Options{})
	err := r.Run(context.Background(), provider.RunRequest{Request: testRequest(false), Target: testTarget(srv.URL)}, &collector{})
	var runErr provider.RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("want RunError, got %v", err)
	}
	if runErr.Class != provider.ClassInvalidRequest || runErr.Kind != provider.TerminalOmitted {
		t.Fatalf("runErr = %+v", runErr)
	}
	if calls != 1 {
		t.Fatalf("resolver called %d times, want 1", calls)
	}
	if requests != 0 {
		t.Fatalf("dispatched %d requests, want 0", requests)
	}
}

func TestForwardHeadersReachResponsesUpstream(t *testing.T) {
	h := http.Header{}
	h.Set("x-session-id", "sess-1")
	h.Set("originator", "omp")
	h.Set("user-agent", "pi/1.0")
	fwd, err := execution.NewForwardSet(h)
	if err != nil {
		t.Fatalf("NewForwardSet: %v", err)
	}
	r := New(staticKey, Options{})
	up, err := r.buildUpstream(testTarget("https://example.com/v1/"), "sk-test", testRequest(true), execution.Facts{Forward: fwd})
	if err != nil {
		t.Fatalf("buildUpstream: %v", err)
	}
	want := map[string]string{
		"x-session-id": "sess-1",
		"originator":   "omp",
		"user-agent":   "pi/1.0",
	}
	for name, v := range want {
		found := false
		for _, h := range up.Headers {
			if h.Name == name {
				found = true
				if h.Value != v {
					t.Fatalf("header %s = %q, want %q", name, h.Value, v)
				}
			}
		}
		if !found {
			t.Fatalf("header %s missing from %v", name, up.Headers)
		}
	}
}
