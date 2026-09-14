package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"prism/internal/account"
	"prism/internal/canon"
	"prism/internal/provider"
	"prism/internal/routing"
)

func singlePlan(providerID string) routing.Plan {
	return routing.Plan{
		Targets: []provider.Target{{Provider: account.ProviderID(providerID), Model: "m1"}},
	}
}

func postJSON(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func messageAssistant(id canon.ItemID, text string) canon.Message {
	return canon.Message{ID: id, Role: canon.RoleAssistant, Content: []canon.Content{canon.TextContent{Text: text}}}
}

func TestResponsesRouteHappyPath(t *testing.T) {
	runner := &fakeRunner{scripts: []fakeScript{{
		events: []canon.Event{
			canon.ItemStarted{Item: messageAssistant("m1", "Hello")},
			canon.TextDelta{ItemID: "m1", Text: "Hello"},
			canon.ItemFinished{Item: messageAssistant("m1", "Hello")},
			canon.TurnFinished{Status: canon.Completed(), Usage: canon.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}},
		},
	}}}
	h := newTestServer(t, nil, map[canon.ModelID]routing.Plan{"test-model": singlePlan("p1")}, func(reg *provider.Registry) {
		if err := reg.Register("p1", runner); err != nil {
			t.Fatal(err)
		}
	})
	rec := postJSON(t, h, "/v1/responses", `{"model":"test-model","stream":true,"input":"hi"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type = %q", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"event: response.created",
		"event: response.in_progress",
		"event: response.output_item.added",
		"event: response.output_text.delta",
		"event: response.output_item.done",
		"event: response.completed",
		"data: [DONE]",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
	if got := strings.Count(body, "event: response.created"); got != 1 {
		t.Fatalf("response.created count = %d", got)
	}
	if !strings.Contains(body, `"input_tokens":3`) {
		t.Fatalf("usage missing:\n%s", body)
	}
}

func TestResponsesRouteToolCallRoundTrip(t *testing.T) {
	call := canon.FunctionCall{ID: "fc1", CallID: "call1", Name: "get_weather", Arguments: []byte(`{"city":"SF"}`)}
	runner := &fakeRunner{scripts: []fakeScript{{
		events: []canon.Event{
			canon.ItemStarted{Item: call},
			canon.ToolArgumentsDelta{ItemID: "fc1", Bytes: []byte(`{"city"`)},
			canon.ToolArgumentsDelta{ItemID: "fc1", Bytes: []byte(`:"SF"}`)},
			canon.ItemFinished{Item: call},
			canon.TurnFinished{Status: canon.Completed(), Usage: canon.Usage{OutputTokens: 4, TotalTokens: 4}},
		},
	}}}
	h := newTestServer(t, nil, map[canon.ModelID]routing.Plan{"test-model": singlePlan("p1")}, func(reg *provider.Registry) {
		if err := reg.Register("p1", runner); err != nil {
			t.Fatal(err)
		}
	})
	rec := postJSON(t, h, "/v1/responses", `{"model":"test-model","stream":true,"input":"weather?"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"event: response.output_item.added",
		"function_call",
		"event: response.function_call_arguments.delta",
		"event: response.function_call_arguments.done",
		"event: response.completed",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
}

func TestChatRouteStream(t *testing.T) {
	runner := &fakeRunner{scripts: []fakeScript{{
		events: []canon.Event{
			canon.ItemStarted{Item: messageAssistant("m1", "Hello world")},
			canon.TextDelta{ItemID: "m1", Text: "Hello world"},
			canon.ItemFinished{Item: messageAssistant("m1", "Hello world")},
			canon.TurnFinished{Status: canon.Completed(), Usage: canon.Usage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5}},
		},
	}}}
	h := newTestServer(t, nil, map[canon.ModelID]routing.Plan{"prov/m1": singlePlan("p1")}, func(reg *provider.Registry) {
		if err := reg.Register("p1", runner); err != nil {
			t.Fatal(err)
		}
	})
	rec := postJSON(t, h, "/v1/chat/completions", `{"model":"prov/m1","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type = %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"object":"chat.completion.chunk"`) {
		t.Fatalf("chunks missing:\n%s", body)
	}
	if !strings.Contains(body, `"content":"Hello world"`) {
		t.Fatalf("delta content missing:\n%s", body)
	}
	if !strings.Contains(body, `"finish_reason":"stop"`) {
		t.Fatalf("finish reason missing:\n%s", body)
	}
	if !strings.HasSuffix(strings.TrimRight(body, "\n"), "data: [DONE]") && !strings.Contains(body, "data: [DONE]\n\n") {
		t.Fatalf("[DONE] missing:\n%s", body)
	}
}

func TestChatRouteNonStream(t *testing.T) {
	runner := &fakeRunner{scripts: []fakeScript{{
		events: []canon.Event{
			canon.ItemStarted{Item: messageAssistant("m1", "Hello world")},
			canon.TextDelta{ItemID: "m1", Text: "Hello world"},
			canon.ItemFinished{Item: messageAssistant("m1", "Hello world")},
			canon.TurnFinished{Status: canon.Completed(), Usage: canon.Usage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5}},
		},
	}}}
	h := newTestServer(t, nil, map[canon.ModelID]routing.Plan{"prov/m1": singlePlan("p1")}, func(reg *provider.Registry) {
		if err := reg.Register("p1", runner); err != nil {
			t.Fatal(err)
		}
	})
	rec := postJSON(t, h, "/v1/chat/completions", `{"model":"prov/m1","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content type = %q", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{`"object":"chat.completion"`, `"content":"Hello world"`, `"finish_reason":"stop"`, `"prompt_tokens":2`} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
}

func TestMessagesRouteHappyPath(t *testing.T) {
	runner := &fakeRunner{scripts: []fakeScript{{
		events: []canon.Event{
			canon.ItemStarted{Item: messageAssistant("m1", "Hi there")},
			canon.TextDelta{ItemID: "m1", Text: "Hi there"},
			canon.ItemFinished{Item: messageAssistant("m1", "Hi there")},
			canon.TurnFinished{Status: canon.Completed(), Usage: canon.Usage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5}},
		},
	}}}
	h := newTestServer(t, nil, map[canon.ModelID]routing.Plan{"claude-p1--m1": singlePlan("p1")}, func(reg *provider.Registry) {
		if err := reg.Register("p1", runner); err != nil {
			t.Fatal(err)
		}
	})
	rec := postJSON(t, h, "/v1/messages", `{"model":"claude-p1--m1","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type = %q", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"event: message_start",
		"event: content_block_start",
		"event: content_block_delta",
		"\"text_delta\"",
		"event: content_block_stop",
		"event: message_delta",
		"event: message_stop",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
}

func TestUnknownRouteNotFound(t *testing.T) {
	h := newTestServer(t, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/nope", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"not_found"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestWrongMethod(t *testing.T) {
	h := newTestServer(t, nil, nil, nil)
	for _, tc := range []struct{ path, method string }{
		{"/v1/responses", http.MethodGet},
		{"/v1/chat/completions", http.MethodDelete},
		{"/v1/models", http.MethodPost},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.RemoteAddr = "127.0.0.1:1234"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s status = %d", tc.method, tc.path, rec.Code)
		}
		want := http.MethodPost
		if tc.path == "/v1/models" {
			want = http.MethodGet
		}
		if allow := rec.Header().Get("Allow"); allow != want {
			t.Fatalf("allow = %q want %q", allow, want)
		}
	}
}

func TestIngressParseErrors(t *testing.T) {
	h := newTestServer(t, nil, nil, nil)

	rec := postJSON(t, h, "/v1/responses", "{not json")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("responses status = %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"code":"invalid_json"`) || !strings.Contains(body, `"param":"body"`) {
		t.Fatalf("responses error body = %s", body)
	}

	rec = postJSON(t, h, "/v1/chat/completions", `{"messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("chat status = %d body %s", rec.Code, rec.Body.String())
	}
	body = rec.Body.String()
	if !strings.Contains(body, `"type":"invalid_request_error"`) || !strings.Contains(body, `"code":"missing_field"`) || !strings.Contains(body, "model") {
		t.Fatalf("chat error body = %s", body)
	}

	rec = postJSON(t, h, "/v1/messages", `{"model":"claude-p1--m1","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("messages status = %d body %s", rec.Code, rec.Body.String())
	}
	body = rec.Body.String()
	if !strings.Contains(body, `"type":"error"`) || !strings.Contains(body, "max_tokens") {
		t.Fatalf("messages error body = %s", body)
	}
}

func TestPreCommitFailoverInvisible(t *testing.T) {
	failing := &fakeRunner{scripts: []fakeScript{{
		err: provider.RunError{Kind: provider.Retryable, Class: provider.ClassServer},
	}}}
	ok := &fakeRunner{scripts: []fakeScript{{
		events: []canon.Event{
			canon.ItemStarted{Item: messageAssistant("m1", "from second")},
			canon.TextDelta{ItemID: "m1", Text: "from second"},
			canon.ItemFinished{Item: messageAssistant("m1", "from second")},
			canon.TurnFinished{Status: canon.Completed(), Usage: canon.Usage{OutputTokens: 2, TotalTokens: 2}},
		},
	}}}
	plan := routing.Plan{
		Targets: []provider.Target{
			{Provider: "p1", Model: "m1"},
			{Provider: "p2", Model: "m1"},
		},
		Policy: routing.TurnPolicy{MaxAccountFailovers: 1, MaxTargetFailovers: 1},
	}
	h := newTestServer(t, nil, map[canon.ModelID]routing.Plan{"test-model": plan}, func(reg *provider.Registry) {
		if err := reg.Register("p1", failing); err != nil {
			t.Fatal(err)
		}
		if err := reg.Register("p2", ok); err != nil {
			t.Fatal(err)
		}
	})
	rec := postJSON(t, h, "/v1/responses", `{"model":"test-model","stream":true,"input":"hi"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if got := strings.Count(body, "event: response.created"); got != 1 {
		t.Fatalf("response.created count = %d:\n%s", got, body)
	}
	if !strings.Contains(body, "from second") {
		t.Fatalf("second runner output missing:\n%s", body)
	}
	if strings.Contains(body, "response.failed") {
		t.Fatalf("failure leaked to client:\n%s", body)
	}
	if !strings.Contains(body, "event: response.completed") {
		t.Fatalf("completed missing:\n%s", body)
	}
	if failing.callsMade() != 1 || ok.callsMade() != 1 {
		t.Fatalf("calls failing=%d ok=%d", failing.callsMade(), ok.callsMade())
	}
}

func TestPostCommitFailureSurfacesTerminal(t *testing.T) {
	runner := &fakeRunner{scripts: []fakeScript{{
		events: []canon.Event{
			canon.ItemStarted{Item: messageAssistant("m1", "partial")},
			canon.TextDelta{ItemID: "m1", Text: "partial"},
		},
		err: provider.RunError{Kind: provider.UnsafeReplay, Class: provider.ClassTransport},
	}}}
	h := newTestServer(t, nil, map[canon.ModelID]routing.Plan{"test-model": singlePlan("p1")}, func(reg *provider.Registry) {
		if err := reg.Register("p1", runner); err != nil {
			t.Fatal(err)
		}
	})
	rec := postJSON(t, h, "/v1/responses", `{"model":"test-model","stream":true,"input":"hi"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if got := strings.Count(body, "event: response.created"); got != 1 {
		t.Fatalf("response.created count = %d", got)
	}
	if !strings.Contains(body, "event: response.failed") || !strings.Contains(body, "upstream_transport") {
		t.Fatalf("terminal failure missing:\n%s", body)
	}
	if strings.Contains(body, "response.completed") {
		t.Fatalf("completed after failure:\n%s", body)
	}
	if strings.Contains(body, "partial output restart") {
		t.Fatalf("unexpected restart marker")
	}
	if runner.callsMade() != 1 {
		t.Fatalf("calls = %d", runner.callsMade())
	}
}

func TestStallWatchdogWithFakeClock(t *testing.T) {
	clock := newFakeClock()
	runner := &fakeRunner{scripts: []fakeScript{{
		events: []canon.Event{
			canon.ItemStarted{Item: messageAssistant("m1", "partial")},
			canon.TextDelta{ItemID: "m1", Text: "partial"},
		},
		block: true,
	}}}
	h := newTestServer(t, clock, map[canon.ModelID]routing.Plan{"test-model": singlePlan("p1")}, func(reg *provider.Registry) {
		if err := reg.Register("p1", runner); err != nil {
			t.Fatal(err)
		}
	})
	ts := httptest.NewServer(h)
	defer ts.Close()
	resp, err := http.Post(ts.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"test-model","stream":true,"input":"hi"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	type readResult struct {
		body string
		err  error
	}
	done := make(chan readResult, 1)
	go func() {
		b, err := io.ReadAll(resp.Body)
		done <- readResult{body: string(b), err: err}
	}()
	var s string
	for range 120 {
		select {
		case r := <-done:
			if r.err != nil {
				t.Fatalf("read body: %v", r.err)
			}
			s = r.body
		default:
		}
		if s != "" {
			break
		}
		clock.Advance(60 * time.Second)
		time.Sleep(time.Millisecond)
	}
	if s == "" {
		select {
		case r := <-done:
			s = r.body
		case <-time.After(5 * time.Second):
			t.Fatal("stall watchdog did not complete the turn")
		}
	}
	if !strings.Contains(s, "event: response.incomplete") {
		t.Fatalf("incomplete terminal missing:\n%s", s)
	}
	if !strings.Contains(s, "upstream_stall") {
		t.Fatalf("stall reason missing:\n%s", s)
	}
	if strings.Contains(s, "event: response.completed") {
		t.Fatalf("completed after stall:\n%s", s)
	}
}

func TestIncompleteEOF(t *testing.T) {
	runner := &fakeRunner{scripts: []fakeScript{{
		events: []canon.Event{
			canon.ItemStarted{Item: messageAssistant("m1", "truncated")},
			canon.TextDelta{ItemID: "m1", Text: "truncated"},
			canon.ItemFinished{Item: messageAssistant("m1", "truncated")},
			canon.TurnFinished{Status: canon.Incomplete(canon.IncompleteAdapterEOF), Usage: canon.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}},
		},
	}}}
	h := newTestServer(t, nil, map[canon.ModelID]routing.Plan{"test-model": singlePlan("p1")}, func(reg *provider.Registry) {
		if err := reg.Register("p1", runner); err != nil {
			t.Fatal(err)
		}
	})
	rec := postJSON(t, h, "/v1/responses", `{"model":"test-model","stream":true,"input":"hi"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: response.incomplete") || !strings.Contains(body, "adapter_eof") {
		t.Fatalf("adapter eof terminal missing:\n%s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("[DONE] missing:\n%s", body)
	}
}

func TestResponsesRouteNonStreamJSON(t *testing.T) {
	runner := &fakeRunner{scripts: []fakeScript{{
		events: []canon.Event{
			canon.ItemStarted{Item: messageAssistant("m1", "Hello world")},
			canon.TextDelta{ItemID: "m1", Text: "Hello world"},
			canon.ItemFinished{Item: messageAssistant("m1", "Hello world")},
			canon.TurnFinished{Status: canon.Completed(), Usage: canon.Usage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5}},
		},
	}}}
	h := newTestServer(t, nil, map[canon.ModelID]routing.Plan{"test-model": singlePlan("p1")}, func(reg *provider.Registry) {
		if err := reg.Register("p1", runner); err != nil {
			t.Fatal(err)
		}
	})
	rec := postJSON(t, h, "/v1/responses", `{"model":"test-model","stream":false,"input":"hi"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content type = %q", ct)
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("body is not one JSON response object: %v\n%s", err, rec.Body.String())
	}
	if resp["object"] != "response" || resp["status"] != "completed" || resp["model"] != "test-model" {
		t.Fatalf("response envelope = %v", resp)
	}
	if id, _ := resp["id"].(string); id == "" {
		t.Fatalf("response id missing: %v", resp["id"])
	}
	output, ok := resp["output"].([]any)
	if !ok || len(output) != 1 {
		t.Fatalf("output = %v", resp["output"])
	}
	item := output[0].(map[string]any)
	if item["type"] != "message" {
		t.Fatalf("output item = %v", item)
	}
	if text := item["content"].([]any)[0].(map[string]any)["text"]; text != "Hello world" {
		t.Fatalf("output text = %v", text)
	}
	usage := resp["usage"].(map[string]any)
	if usage["input_tokens"] != float64(2) || usage["total_tokens"] != float64(5) {
		t.Fatalf("usage = %v", usage)
	}
	if strings.Contains(rec.Body.String(), "data: [DONE]") {
		t.Fatalf("non-stream response must not carry SSE framing:\n%s", rec.Body.String())
	}
}

func TestResponsesRouteNonStreamDefaultJSON(t *testing.T) {
	runner := &fakeRunner{scripts: []fakeScript{{
		events: []canon.Event{
			canon.ItemStarted{Item: messageAssistant("m1", "Hi")},
			canon.TextDelta{ItemID: "m1", Text: "Hi"},
			canon.ItemFinished{Item: messageAssistant("m1", "Hi")},
			canon.TurnFinished{Status: canon.Completed()},
		},
	}}}
	h := newTestServer(t, nil, map[canon.ModelID]routing.Plan{"test-model": singlePlan("p1")}, func(reg *provider.Registry) {
		if err := reg.Register("p1", runner); err != nil {
			t.Fatal(err)
		}
	})
	rec := postJSON(t, h, "/v1/responses", `{"model":"test-model","input":"hi"}`)
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content type = %q, want application/json for absent stream field", ct)
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("body is not one JSON response object: %v\n%s", err, rec.Body.String())
	}
	if resp["status"] != "completed" {
		t.Fatalf("status = %v", resp["status"])
	}
}

func TestWrongMethodWebSocketUpgrade(t *testing.T) {
	h := newTestServer(t, nil, nil, nil)
	for _, tc := range []struct {
		name    string
		upgrade string
		want    int
	}{
		{"websocket upgrade", "websocket", http.StatusUpgradeRequired},
		{"plain get", "", http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			req.RemoteAddr = "127.0.0.1:1234"
			if tc.upgrade != "" {
				req.Header.Set("Upgrade", tc.upgrade)
				req.Header.Set("Connection", "Upgrade")
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			var env struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("error body is not JSON: %v\n%s", err, rec.Body.String())
			}
			if tc.want == http.StatusUpgradeRequired && env.Error.Code != "upgrade_required" {
				t.Fatalf("code = %q, want upgrade_required", env.Error.Code)
			}
		})
	}
}
