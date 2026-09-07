package customchat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"prism/internal/account"
	"prism/internal/canon"
	egresschat "prism/internal/egress/chat"
	ingresschat "prism/internal/ingress/chat"
	"prism/internal/provider"
	"prism/internal/stream"
)

func staticKey(ctx context.Context, target provider.Target, lease account.Lease) (string, error) {
	return "sk-test", nil
}

func secretKey(ctx context.Context, target provider.Target, lease account.Lease) (string, error) {
	return "sk-super-secret-value-42", nil
}

func failingKey(ctx context.Context, target provider.Target, lease account.Lease) (string, error) {
	return "", errors.New("no credential for account")
}

func testTarget(baseURL string) provider.Target {
	return provider.Target{
		Provider:  "chat",
		Wire:      provider.WireChat,
		BaseURL:   baseURL,
		APIKeyRef: "key-ref",
	}
}

func testRequest(stream bool) canon.Request {
	params := json.RawMessage(`{"type":"object"}`)
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

type collector struct {
	events []canon.Event
}

func (c *collector) Emit(ev canon.Event) error {
	c.events = append(c.events, ev)
	return nil
}

func (c *collector) All() []canon.Event { return c.events }

func (c *collector) TerminalCount() int {
	n := 0
	for _, ev := range c.events {
		switch ev.(type) {
		case canon.TurnFinished, canon.TurnFailed:
			n++
		}
	}
	return n
}

func eventKinds(events []canon.Event) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		out = append(out, fmt.Sprintf("%T", ev))
	}
	return out
}

func TestChatURLVariants(t *testing.T) {
	cases := map[string]string{
		"https://example.com":                      "https://example.com/v1/chat/completions",
		"https://example.com/":                     "https://example.com/v1/chat/completions",
		"https://example.com/v1":                   "https://example.com/v1/chat/completions",
		"https://example.com/v1/":                  "https://example.com/v1/chat/completions",
		"https://example.com/v1/chat/completions":  "https://example.com/v1/chat/completions",
		"https://gateway.internal:9000/openai/v1/": "https://gateway.internal:9000/openai/v1/chat/completions",
	}
	for base, want := range cases {
		if got := chatURL(base); got != want {
			t.Fatalf("chatURL(%q) = %q, want %q", base, got, want)
		}
	}
}

func TestBuildUpstreamRequest(t *testing.T) {
	r := New(staticKey, Options{ExtraHeaders: []Header{{Name: "X-Custom", Value: "v1"}}})
	up, err := r.buildUpstream(testTarget("https://example.com/v1/"), "sk-test", testRequest(true))
	if err != nil {
		t.Fatalf("buildUpstream: %v", err)
	}
	if up.URL != "https://example.com/v1/chat/completions" {
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
	want := `{"model":"gpt-5.2","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hi"}],"stream":true,"stream_options":{"include_usage":true},"max_completion_tokens":128,"temperature":0.2,"reasoning_effort":"high","tools":[{"type":"function","function":{"name":"get_weather","description":"weather","parameters":{"type":"object"},"strict":true}}],"tool_choice":"auto"}`
	if string(up.Body) != want {
		t.Fatalf("body =\n%s\nwant\n%s", up.Body, want)
	}
}

func TestBuildUpstreamRequestFullMapping(t *testing.T) {
	params := json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`)
	temp := 0.7
	topP := 0.9
	presence := -0.5
	frequency := 1.5
	parallel := false
	strict := true
	req := canon.Request{
		Model: "gpt-5.2",
		Input: []canon.Item{
			canon.Message{
				Role: canon.RoleUser,
				Content: []canon.Content{
					canon.TextContent{Text: "what is this"},
					canon.ImageContent{MIMEType: "image/png", Data: []byte{0x89, 0x50}, Detail: "high"},
				},
			},
			canon.Message{Role: canon.RoleAssistant, Content: []canon.Content{canon.TextContent{Text: "Let me check"}}},
			canon.FunctionCall{CallID: "call_1", Name: "get_weather", Arguments: []byte(`{"city":"sf"}`)},
			canon.FunctionOutput{CallID: "call_1", Output: []canon.Content{canon.TextContent{Text: "sunny"}}},
			canon.Message{Role: canon.RoleDeveloper, Content: []canon.Content{canon.TextContent{Text: "dev note"}}},
		},
		MaxOutputTokens: 64,
		Reasoning:       canon.ReasoningConfig{Effort: canon.EffortLow},
		Sampling: canon.Sampling{
			Temperature:       &temp,
			TopP:              &topP,
			Stop:              []string{"END"},
			ParallelToolCalls: &parallel,
			PresencePenalty:   &presence,
			FrequencyPenalty:  &frequency,
			ServiceTier:       canon.TierFlex,
		},
		Text: canon.TextOutput{
			Verbosity: canon.VerbosityHigh,
			Format:    &canon.TextFormat{Type: "json_schema", Name: "out", Schema: json.RawMessage(`{"type":"object"}`), Strict: &strict},
		},
		Tools:      []canon.Tool{canon.FunctionTool{Name: "get_weather", Description: "weather", Parameters: params}},
		ToolChoice: canon.ToolNamed{Name: "get_weather"},
	}
	r := New(staticKey, Options{})
	up, err := r.buildUpstream(testTarget("https://example.com"), "sk-test", req)
	if err != nil {
		t.Fatalf("buildUpstream: %v", err)
	}
	want := `{"model":"gpt-5.2","messages":[` +
		`{"role":"user","content":[{"type":"text","text":"what is this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,iVA=","detail":"high"}}]},` +
		`{"role":"assistant","content":"Let me check","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"sf\"}"}}]},` +
		`{"role":"tool","content":"sunny","tool_call_id":"call_1"},` +
		`{"role":"developer","content":"dev note"}` +
		`],"stream":false,"max_completion_tokens":64,"temperature":0.7,"top_p":0.9,"stop":["END"],"parallel_tool_calls":false,"presence_penalty":-0.5,"frequency_penalty":1.5,"service_tier":"flex","reasoning_effort":"low","verbosity":"high","response_format":{"type":"json_schema","json_schema":{"name":"out","schema":{"type":"object"},"strict":true}},"tools":[{"type":"function","function":{"name":"get_weather","description":"weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}],"tool_choice":{"type":"function","function":{"name":"get_weather"}}}`
	if string(up.Body) != want {
		t.Fatalf("body =\n%s\nwant\n%s", up.Body, want)
	}
}

func TestBuildUpstreamAssistantToolCallsWithoutText(t *testing.T) {
	req := canon.Request{
		Model: "m",
		Input: []canon.Item{
			canon.FunctionCall{CallID: "call_a", Name: "f"},
			canon.FunctionCall{CallID: "call_b", Name: "g", Arguments: []byte(`{}`)},
		},
	}
	r := New(staticKey, Options{})
	up, err := r.buildUpstream(testTarget("https://example.com"), "sk-test", req)
	if err != nil {
		t.Fatalf("buildUpstream: %v", err)
	}
	want := `{"model":"m","messages":[{"role":"assistant","tool_calls":[{"id":"call_a","type":"function","function":{"name":"f","arguments":""}},{"id":"call_b","type":"function","function":{"name":"g","arguments":"{}"}}]}],"stream":false}`
	if string(up.Body) != want {
		t.Fatalf("body =\n%s\nwant\n%s", up.Body, want)
	}
}

func TestUnsupportedCanonicalContentFailsPreDispatch(t *testing.T) {
	cases := []struct {
		name string
		req  canon.Request
	}{
		{"image in assistant message", canon.Request{Model: "m", Input: []canon.Item{canon.Message{Role: canon.RoleAssistant, Content: []canon.Content{canon.ImageContent{MIMEType: "image/png", Data: []byte{1}}}}}}},
		{"image in instructions", canon.Request{Model: "m", Instructions: []canon.Content{canon.ImageContent{MIMEType: "image/png", Data: []byte{1}}}}},
		{"image in tool result", canon.Request{Model: "m", Input: []canon.Item{canon.FunctionOutput{CallID: "call", Output: []canon.Content{canon.ImageContent{MIMEType: "image/png", Data: []byte{1}}}}}}},
		{"function call without call id", canon.Request{Model: "m", Input: []canon.Item{canon.FunctionCall{Name: "f"}}}},
		{"tool result without call id", canon.Request{Model: "m", Input: []canon.Item{canon.FunctionOutput{Output: []canon.Content{canon.TextContent{Text: "x"}}}}}},
		{"allowed tool choice", canon.Request{Model: "m", ToolChoice: canon.ToolAllowed{Mode: canon.AllowedAuto, Tools: []canon.ToolName{"f"}}}},
		{"unknown text format", canon.Request{Model: "m", Text: canon.TextOutput{Format: &canon.TextFormat{Type: "pterodactyl"}}}},
	}
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests++
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"x"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := New(staticKey, Options{})
			err := r.Run(context.Background(), provider.RunRequest{Request: tc.req, Target: testTarget(srv.URL)}, &collector{})
			var runErr provider.RunError
			if !errors.As(err, &runErr) {
				t.Fatalf("want RunError, got %v", err)
			}
			if runErr.Class != provider.ClassInvalidRequest || runErr.Kind != provider.TerminalOmitted {
				t.Fatalf("runErr = %+v", runErr)
			}
			if runErr.Kind.FailoverAllowed() && runErr.Class.FailoverAllowed() {
				t.Fatalf("invalid request must not allow failover: %+v", runErr)
			}
			if runErr.Cause == nil || runErr.Cause.Error() == "" {
				t.Fatalf("invalid request error should carry a cause: %+v", runErr)
			}
		})
	}
	if requests != 0 {
		t.Fatalf("unsupported content dispatched %d upstream requests", requests)
	}
}

func TestChatWireDegradesNonFunctionTools(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw, _ := io.ReadAll(req.Body)
		body = string(raw)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"x","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	req := canon.Request{
		Model:  "m",
		Stream: true,
		Input:  []canon.Item{canon.Message{Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "hi"}}}},
		Tools: []canon.Tool{
			canon.FunctionTool{Name: "get_weather", Description: "weather"},
			canon.CustomToolDef{Name: "apply_patch", Description: "patch"},
			canon.LocalShellToolDef{},
			canon.ToolSearchToolDef{Limit: 5},
		},
	}
	r := New(staticKey, Options{})
	if err := r.Run(context.Background(), provider.RunRequest{Request: req, Target: testTarget(srv.URL)}, &collector{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	var decoded struct {
		Tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("decode body: %v\n%s", err, body)
	}
	names := make([]string, 0, len(decoded.Tools))
	for _, tool := range decoded.Tools {
		if tool.Type != "function" {
			t.Fatalf("tool type = %q, want function:\n%s", tool.Type, body)
		}
		names = append(names, tool.Function.Name)
	}
	want := []string{"get_weather", "apply_patch"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("tool names = %v, want %v:\n%s", names, want, body)
	}
}

func TestChatWireToleratesSessionHistoryItems(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw, _ := io.ReadAll(req.Body)
		body = string(raw)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"x","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	req := canon.Request{
		Model:  "m",
		Stream: true,
		Input: []canon.Item{
			canon.Message{Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "hi"}}},
			canon.ReasoningItem{ID: "rs", Content: "thinking about it"},
			canon.Message{Role: canon.RoleAssistant, Content: []canon.Content{canon.TextContent{Text: "let me patch"}}},
			canon.CustomToolCall{ID: "c1", CallID: "call_1", Name: "apply_patch", Input: "*** Begin Patch"},
			canon.CustomToolOutput{CallID: "call_1", Output: "done"},
			canon.Message{Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "thanks"}}},
		},
	}
	r := New(staticKey, Options{})
	if err := r.Run(context.Background(), provider.RunRequest{Request: req, Target: testTarget(srv.URL)}, &collector{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	var decoded struct {
		Messages []struct {
			Role       string         `json:"role"`
			Content    string         `json:"content"`
			ToolCallID string         `json:"tool_call_id"`
			ToolCalls  []wireToolCall `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("decode body: %v\n%s", err, body)
	}
	if strings.Contains(body, "thinking about it") {
		t.Fatalf("reasoning text leaked into chat history:\n%s", body)
	}
	var callNames []string
	for _, m := range decoded.Messages {
		for _, c := range m.ToolCalls {
			callNames = append(callNames, c.Function.Name)
		}
	}
	if strings.Join(callNames, ",") != "apply_patch" {
		t.Fatalf("tool calls = %v, want apply_patch:\n%s", callNames, body)
	}
	toolMsgs := 0
	for _, m := range decoded.Messages {
		if m.Role == "tool" {
			toolMsgs++
			if m.ToolCallID != "call_1" || m.Content != "done" {
				t.Fatalf("tool message = %+v", m)
			}
		}
	}
	if toolMsgs != 1 {
		t.Fatalf("tool messages = %d", toolMsgs)
	}
}

type wireToolCall struct {
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func TestStreamToCanonEvents(t *testing.T) {
	var gotAuth, gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotAuth = req.Header.Get("Authorization")
		gotPath = req.URL.Path
		raw, _ := io.ReadAll(req.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": ping\n\n")
		fmt.Fprint(w, `data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"Hel"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"lo"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"get_weather","arguments":""}}]}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\""}}]}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"get_time","arguments":""}}]}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"sf\"}"}}]}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"chatcmpl-1","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":5,"total_tokens":8}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
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
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q", gotPath)
	}
	if !strings.Contains(gotBody, `"stream":true`) || !strings.Contains(gotBody, `"stream_options":{"include_usage":true}`) {
		t.Fatalf("request body lost streaming fields: %s", gotBody)
	}
	if events.TerminalCount() != 1 {
		t.Fatalf("terminals = %d", events.TerminalCount())
	}
	want := strings.Join([]string{
		"canon.ItemStarted", "canon.TextDelta", "canon.TextDelta",
		"canon.ItemStarted", "canon.ToolArgumentsDelta",
		"canon.ItemStarted", "canon.ToolArgumentsDelta",
		"canon.ItemFinished", "canon.ItemFinished", "canon.ItemFinished",
		"canon.TurnFinished",
	}, ",")
	if got := strings.Join(eventKinds(events.All()), ","); got != want {
		t.Fatalf("events =\n%s\nwant\n%s", got, want)
	}
	msg := events.All()[0].(canon.ItemStarted).Item.(canon.Message)
	if msg.ID != "chatcmpl-1" || msg.Role != canon.RoleAssistant {
		t.Fatalf("message item = %+v", msg)
	}
	finished := events.All()[7].(canon.ItemFinished).Item.(canon.Message)
	if len(finished.Content) != 1 || finished.Content[0].(canon.TextContent).Text != "Hello" {
		t.Fatalf("finished message = %+v", finished)
	}
	first := events.All()[8].(canon.ItemFinished).Item.(canon.FunctionCall)
	if first.CallID != "call_a" || first.Name != "get_weather" || string(first.Arguments) != `{"city":"sf"}` {
		t.Fatalf("first tool call = %+v", first)
	}
	second := events.All()[9].(canon.ItemFinished).Item.(canon.FunctionCall)
	if second.CallID != "call_b" || second.Name != "get_time" || len(second.Arguments) != 0 {
		t.Fatalf("second tool call = %+v", second)
	}
	finish := events.All()[10].(canon.TurnFinished)
	if finish.Status.Kind() != canon.StatusCompleted {
		t.Fatalf("status = %d", finish.Status.Kind())
	}
	if finish.Usage != (canon.Usage{InputTokens: 3, OutputTokens: 5, TotalTokens: 8}) {
		t.Fatalf("usage = %+v", finish.Usage)
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

func TestStreamIncompleteLength(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"chatcmpl-2","choices":[{"index":0,"delta":{"content":"part"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"chatcmpl-2","choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	events := &collector{}
	r := New(staticKey, Options{})
	if err := r.Run(context.Background(), provider.RunRequest{Request: testRequest(true), Target: testTarget(srv.URL)}, events); err != nil {
		t.Fatalf("Run: %v", err)
	}
	finish := events.All()[len(events.All())-1].(canon.TurnFinished)
	reason, ok := finish.Status.Reason()
	if !ok || reason != canon.IncompleteMaxOutputTokens {
		t.Fatalf("status = %+v", finish.Status)
	}
}

func TestStreamUsageInFinishChunk(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"chatcmpl-3","choices":[{"index":0,"delta":{"content":"hi"}}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"chatcmpl-3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	events := &collector{}
	r := New(staticKey, Options{})
	if err := r.Run(context.Background(), provider.RunRequest{Request: testRequest(true), Target: testTarget(srv.URL)}, events); err != nil {
		t.Fatalf("Run: %v", err)
	}
	finish := events.All()[len(events.All())-1].(canon.TurnFinished)
	if finish.Usage.TotalTokens != 3 {
		t.Fatalf("usage = %+v", finish.Usage)
	}
}

func TestStreamDuplicateFinishFramesTolerated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"x","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"x","choices":[{"index":0,"delta":{"reasoning_content":null},"finish_reason":"stop","logprobs":null,"matched_stop":154827}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":7514,"completion_tokens":37,"total_tokens":7551}}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":7514,"completion_tokens":37,"total_tokens":7551}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	events := &collector{}
	r := New(staticKey, Options{})
	err := r.Run(context.Background(), provider.RunRequest{Request: testRequest(true), Target: testTarget(srv.URL)}, events)
	if err != nil {
		t.Fatalf("run = %v", err)
	}
	if events.TerminalCount() != 1 {
		t.Fatalf("terminals = %d", events.TerminalCount())
	}
	var finish canon.TurnFinished
	for _, ev := range events.All() {
		if f, ok := ev.(canon.TurnFinished); ok {
			finish = f
		}
	}
	if finish.Status != canon.Completed() {
		t.Fatalf("terminal = %#v", finish)
	}
	if finish.Usage.TotalTokens != 7551 {
		t.Fatalf("usage = %+v", finish.Usage)
	}
}

func TestStreamMalformedFrameIsTypedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {not-json\n\n")
	}))
	defer srv.Close()
	r := New(staticKey, Options{})
	err := r.Run(context.Background(), provider.RunRequest{Request: testRequest(true), Target: testTarget(srv.URL)}, &collector{})
	var runErr provider.RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("want RunError, got %v", err)
	}
	if runErr.Kind != provider.TerminalOmitted || runErr.Class != provider.ClassTransport {
		t.Fatalf("runErr = %+v", runErr)
	}
}

func TestStreamEOFWithoutTerminal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"x","choices":[{"index":0,"delta":{"content":"hi"}}]}`+"\n\n")
	}))
	defer srv.Close()
	r := New(staticKey, Options{})
	err := r.Run(context.Background(), provider.RunRequest{Request: testRequest(true), Target: testTarget(srv.URL)}, &collector{})
	var runErr provider.RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("want RunError, got %v", err)
	}
	if runErr.Kind != provider.TerminalOmitted || !runErr.Accepted || runErr.ReplaySafe {
		t.Fatalf("runErr = %+v", runErr)
	}
}

func TestStreamDoneWithoutFinishReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"x","choices":[{"index":0,"delta":{"content":"hi"}}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	r := New(staticKey, Options{})
	err := r.Run(context.Background(), provider.RunRequest{Request: testRequest(true), Target: testTarget(srv.URL)}, &collector{})
	var runErr provider.RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("want RunError, got %v", err)
	}
	if runErr.Kind != provider.TerminalOmitted || runErr.Class != provider.ClassTransport {
		t.Fatalf("runErr = %+v", runErr)
	}
}

func TestStreamUnknownFinishReasonFailsTurn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"warp_speed"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	events := &collector{}
	r := New(staticKey, Options{})
	err := r.Run(context.Background(), provider.RunRequest{Request: testRequest(true), Target: testTarget(srv.URL)}, events)
	var runErr provider.RunError
	if !errors.As(err, &runErr) || runErr.Kind != provider.TerminalEmitted {
		t.Fatalf("want TerminalEmitted RunError, got %v", err)
	}
	if events.TerminalCount() != 1 {
		t.Fatalf("terminals = %d", events.TerminalCount())
	}
}

func TestStreamReasoningContentDelta(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"chatcmpl-4","choices":[{"index":0,"delta":{"reasoning_content":"thinking"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"chatcmpl-4","choices":[{"index":0,"delta":{"content":"answer"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"chatcmpl-4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	events := &collector{}
	r := New(staticKey, Options{})
	if err := r.Run(context.Background(), provider.RunRequest{Request: testRequest(true), Target: testTarget(srv.URL)}, events); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := strings.Join([]string{
		"canon.ItemStarted", "canon.ReasoningDelta",
		"canon.ItemStarted", "canon.TextDelta",
		"canon.ItemFinished", "canon.ItemFinished", "canon.TurnFinished",
	}, ",")
	if got := strings.Join(eventKinds(events.All()), ","); got != want {
		t.Fatalf("events =\n%s\nwant\n%s", got, want)
	}
	reasoning := events.All()[4].(canon.ItemFinished).Item.(canon.ReasoningItem)
	if reasoning.Content != "thinking" {
		t.Fatalf("reasoning = %+v", reasoning)
	}
}

func TestAggregateToCanonEvents(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotPath = req.URL.Path
		fmt.Fprint(w, `{"id":"chatcmpl-9","choices":[{"index":0,"message":{"role":"assistant","content":"Hello","tool_calls":[{"id":"call_a","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"sf\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":5,"total_tokens":8,"prompt_tokens_details":{"cached_tokens":1},"completion_tokens_details":{"reasoning_tokens":2}}}`)
	}))
	defer srv.Close()
	events := &collector{}
	r := New(staticKey, Options{})
	if err := r.Run(context.Background(), provider.RunRequest{Request: testRequest(false), Target: testTarget(srv.URL)}, events); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q", gotPath)
	}
	want := strings.Join([]string{
		"canon.ItemStarted", "canon.TextDelta", "canon.ItemFinished",
		"canon.ItemStarted", "canon.ToolArgumentsDelta", "canon.ItemFinished",
		"canon.TurnFinished",
	}, ",")
	if got := strings.Join(eventKinds(events.All()), ","); got != want {
		t.Fatalf("events =\n%s\nwant\n%s", got, want)
	}
	delta := events.All()[1].(canon.TextDelta)
	if delta.Text != "Hello" {
		t.Fatalf("text delta = %+v", delta)
	}
	msg := events.All()[2].(canon.ItemFinished).Item.(canon.Message)
	if len(msg.Content) != 1 || msg.Content[0].(canon.TextContent).Text != "Hello" {
		t.Fatalf("message = %+v", msg)
	}
	fc := events.All()[5].(canon.ItemFinished).Item.(canon.FunctionCall)
	if fc.CallID != "call_a" || fc.Name != "get_weather" || string(fc.Arguments) != `{"city":"sf"}` {
		t.Fatalf("tool call = %+v", fc)
	}
	finish := events.All()[6].(canon.TurnFinished)
	if finish.Status.Kind() != canon.StatusCompleted {
		t.Fatalf("status = %d", finish.Status.Kind())
	}
	if finish.Usage != (canon.Usage{InputTokens: 3, CachedInputTokens: 1, OutputTokens: 5, ReasoningTokens: 2, TotalTokens: 8}) {
		t.Fatalf("usage = %+v", finish.Usage)
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

func TestAggregateMatchesStreamEvents(t *testing.T) {
	streamSSE := strings.Join([]string{
		`data: {"id":"chatcmpl-e","choices":[{"index":0,"delta":{"content":"Hi "}}]}`,
		`data: {"id":"chatcmpl-e","choices":[{"index":0,"delta":{"content":"there"}}]}`,
		`data: {"id":"chatcmpl-e","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"chatcmpl-e","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`,
		`data: [DONE]`,
		"",
	}, "\n\n")
	aggregateBody := `{"id":"chatcmpl-e","choices":[{"index":0,"message":{"role":"assistant","content":"Hi there"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`

	run := func(body string, stream bool) []canon.Event {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if stream {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, body)
				return
			}
			fmt.Fprint(w, body)
		}))
		defer srv.Close()
		events := &collector{}
		r := New(staticKey, Options{})
		if err := r.Run(context.Background(), provider.RunRequest{Request: testRequest(stream), Target: testTarget(srv.URL)}, events); err != nil {
			t.Fatalf("Run: %v", err)
		}
		var out []canon.Event
		for _, ev := range events.All() {
			switch ev.(type) {
			case canon.TextDelta, canon.ReasoningDelta, canon.ToolArgumentsDelta, canon.CustomToolInputDelta:
				continue
			}
			out = append(out, ev)
		}
		return out
	}

	streamEvents := run(streamSSE, true)
	aggregateEvents := run(aggregateBody, false)
	if got, want := strings.Join(eventKinds(streamEvents), ","), strings.Join(eventKinds(aggregateEvents), ","); got != want {
		t.Fatalf("event kinds:\nstream    %s\naggregate %s", got, want)
	}
	for i := range streamEvents {
		switch s := streamEvents[i].(type) {
		case canon.ItemFinished:
			a, ok := aggregateEvents[i].(canon.ItemFinished)
			if !ok || fmt.Sprint(s.Item) != fmt.Sprint(a.Item) {
				t.Fatalf("item finished %d: stream %+v aggregate %+v", i, s.Item, aggregateEvents[i])
			}
		case canon.TurnFinished:
			a, ok := aggregateEvents[i].(canon.TurnFinished)
			if !ok || s.Status.Kind() != a.Status.Kind() || s.Usage != a.Usage {
				t.Fatalf("turn finished: stream %+v aggregate %+v", s, a)
			}
		}
	}
}

func TestAggregateContentFilter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprint(w, `{"id":"c","choices":[{"index":0,"message":{"role":"assistant","content":"no"},"finish_reason":"content_filter"}],"usage":{}}`)
	}))
	defer srv.Close()
	events := &collector{}
	r := New(staticKey, Options{})
	if err := r.Run(context.Background(), provider.RunRequest{Request: testRequest(false), Target: testTarget(srv.URL)}, events); err != nil {
		t.Fatalf("Run: %v", err)
	}
	finish := events.All()[len(events.All())-1].(canon.TurnFinished)
	reason, ok := finish.Status.Reason()
	if !ok || reason != canon.IncompleteContentFilter {
		t.Fatalf("status = %+v", finish.Status)
	}
}

func TestAggregateErrorObjectFailsTurn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprint(w, `{"error":{"message":"model exploded","code":"boom"}}`)
	}))
	defer srv.Close()
	events := &collector{}
	r := New(staticKey, Options{})
	err := r.Run(context.Background(), provider.RunRequest{Request: testRequest(false), Target: testTarget(srv.URL)}, events)
	var runErr provider.RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("want RunError, got %v", err)
	}
	if runErr.Kind != provider.TerminalEmitted || runErr.Class != provider.ClassServer {
		t.Fatalf("runErr = %+v", runErr)
	}
	if events.TerminalCount() != 1 {
		t.Fatalf("terminals = %d", events.TerminalCount())
	}
}

func TestAggregateMalformedJSONIsTypedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprint(w, `<html>not json</html>`)
	}))
	defer srv.Close()
	r := New(staticKey, Options{})
	err := r.Run(context.Background(), provider.RunRequest{Request: testRequest(false), Target: testTarget(srv.URL)}, &collector{})
	var runErr provider.RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("want RunError, got %v", err)
	}
	if runErr.Kind != provider.Retryable || runErr.Class != provider.ClassTransport || !runErr.ReplaySafe {
		t.Fatalf("runErr = %+v", runErr)
	}
}

func TestAggregateNoChoicesIsTypedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprint(w, `{"id":"c","choices":[],"usage":{}}`)
	}))
	defer srv.Close()
	r := New(staticKey, Options{})
	err := r.Run(context.Background(), provider.RunRequest{Request: testRequest(false), Target: testTarget(srv.URL)}, &collector{})
	var runErr provider.RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("want RunError, got %v", err)
	}
	if runErr.Class != provider.ClassTransport || !runErr.ReplaySafe {
		t.Fatalf("runErr = %+v", runErr)
	}
}

func TestUpstreamHTTPErrorMapping(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		retryAfter string
		class      provider.ErrorClass
		retry      time.Duration
		accepted   bool
	}{
		{"unauthorized", 401, `{"error":{"message":"bad key","code":"invalid_api_key"}}`, "", provider.ClassUnauthorized, 0, false},
		{"forbidden", 403, `{"error":{"message":"denied"}}`, "", provider.ClassUnauthorized, 0, false},
		{"rate limited", 429, `{"error":{"message":"slow down","code":"rate_limit_exceeded"}}`, "7", provider.ClassRateLimited, 7 * time.Second, false},
		{"context length", 400, `{"error":{"message":"too long","code":"context_length_exceeded"}}`, "", provider.ClassContextLength, 0, false},
		{"invalid request", 400, `{"error":{"message":"bad"}}`, "", provider.ClassInvalidRequest, 0, false},
		{"not found", 404, `{"error":{"message":"no model"}}`, "", provider.ClassNotFound, 0, false},
		{"timeout status", 408, `{"error":{"message":"late"}}`, "", provider.ClassTimeout, 0, false},
		{"server", 503, `{"error":{"message":"overloaded"}}`, "2", provider.ClassServer, 2 * time.Second, true},
		{"malformed body", 500, `<html>oops</html>`, "", provider.ClassServer, 0, true},
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
			r := New(secretKey, Options{})
			err := r.Run(context.Background(), provider.RunRequest{Request: testRequest(false), Target: testTarget(srv.URL)}, &collector{})
			var runErr provider.RunError
			if !errors.As(err, &runErr) {
				t.Fatalf("want RunError, got %v", err)
			}
			if runErr.Class != tc.class {
				t.Fatalf("class = %d, want %d", runErr.Class, tc.class)
			}
			if runErr.Kind != provider.Retryable || !runErr.ReplaySafe || runErr.Accepted != tc.accepted {
				t.Fatalf("runErr = %+v", runErr)
			}
			if runErr.RetryAfter != tc.retry {
				t.Fatalf("retryAfter = %s, want %s", runErr.RetryAfter, tc.retry)
			}
			if strings.Contains(err.Error(), "sk-super-secret-value-42") {
				t.Fatal("error leaks the resolved key")
			}
			if runErr.Cause != nil && strings.Contains(runErr.Cause.Error(), "sk-super-secret-value-42") {
				t.Fatal("error cause leaks the resolved key")
			}
		})
	}
}

func TestTargetTimeoutCancelsRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		time.Sleep(200 * time.Millisecond)
		fmt.Fprint(w, `{"choices":[]}`)
	}))
	defer srv.Close()
	target := testTarget(srv.URL)
	target.Timeout = 10 * time.Millisecond
	r := New(staticKey, Options{})
	err := r.Run(context.Background(), provider.RunRequest{Request: testRequest(false), Target: target}, &collector{})
	var runErr provider.RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("want RunError, got %v", err)
	}
	if runErr.Class != provider.ClassTimeout || !runErr.ReplaySafe {
		t.Fatalf("runErr = %+v", runErr)
	}
}

func TestContextCancellationIsTyped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		time.Sleep(200 * time.Millisecond)
		fmt.Fprint(w, `{"choices":[]}`)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := New(staticKey, Options{})
	err := r.Run(ctx, provider.RunRequest{Request: testRequest(false), Target: testTarget(srv.URL)}, &collector{})
	var runErr provider.RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("want RunError, got %v", err)
	}
	if runErr.Kind != provider.Retryable || runErr.Class != provider.ClassTransport || !runErr.ReplaySafe {
		t.Fatalf("runErr = %+v", runErr)
	}
}

func TestKeyResolverFailureIsTyped(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests++
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	r := New(failingKey, Options{})
	err := r.Run(context.Background(), provider.RunRequest{Request: testRequest(false), Target: testTarget(srv.URL)}, &collector{})
	var runErr provider.RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("want RunError, got %v", err)
	}
	if runErr.Class != provider.ClassTransport || runErr.Kind != provider.TerminalOmitted {
		t.Fatalf("runErr = %+v", runErr)
	}
	if requests != 0 {
		t.Fatalf("resolver failure dispatched %d requests", requests)
	}
}

func TestTargetValidation(t *testing.T) {
	cases := []provider.Target{
		{Provider: "chat", Wire: provider.WireResponses, BaseURL: "https://x", APIKeyRef: "k"},
		{Provider: "chat", Wire: provider.WireChat, APIKeyRef: "k"},
	}
	for _, target := range cases {
		r := New(staticKey, Options{})
		err := r.Run(context.Background(), provider.RunRequest{Request: testRequest(false), Target: target}, &collector{})
		var runErr provider.RunError
		if !errors.As(err, &runErr) {
			t.Fatalf("target %+v: want RunError, got %v", target, err)
		}
		if runErr.Class != provider.ClassInvalidRequest || runErr.Kind != provider.TerminalOmitted {
			t.Fatalf("target %+v: runErr = %+v", target, runErr)
		}
	}
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
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"x"},"finish_reason":"stop"}]}`)
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
	if gotPath != "/v1/chat/completions" {
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
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"x"},"finish_reason":"stop"}]}`)
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
func TestRegistryRegistration(t *testing.T) {
	registry := provider.NewRegistry()
	if err := registry.Register("chat", New(staticKey, Options{})); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := registry.Register("chat", New(staticKey, Options{})); !errors.Is(err, provider.ErrDuplicateProvider) {
		t.Fatalf("want duplicate error, got %v", err)
	}
	if _, ok := registry.Lookup("chat"); !ok {
		t.Fatal("lookup failed")
	}
}

func TestCatalog(t *testing.T) {
	r := New(staticKey, Options{Models: []provider.Model{{ID: "gpt-5.2"}}})
	models, err := r.Catalog(testTarget("https://example.com/v1"), account.Lease{}).Models(context.Background())
	if err != nil {
		t.Fatalf("static models: %v", err)
	}
	if len(models) != 1 || models[0].ID != "gpt-5.2" {
		t.Fatalf("models = %+v", models)
	}
}

func TestStreamToolCallWithoutIDMintsCallID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"chatcmpl-mint","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"type":"function","function":{"name":"get_weather","arguments":""}}]}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"chatcmpl-mint","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":\"sf\"}"}}]}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"chatcmpl-mint","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	events := &collector{}
	r := New(staticKey, Options{})
	if err := r.Run(context.Background(), provider.RunRequest{Request: testRequest(true), Target: testTarget(srv.URL)}, events); err != nil {
		t.Fatalf("Run: %v", err)
	}
	started := canon.FunctionCall{}
	finished := canon.FunctionCall{}
	for _, ev := range events.All() {
		switch e := ev.(type) {
		case canon.ItemStarted:
			if fc, ok := e.Item.(canon.FunctionCall); ok {
				started = fc
			}
		case canon.ItemFinished:
			if fc, ok := e.Item.(canon.FunctionCall); ok {
				finished = fc
			}
		}
	}
	if started.CallID == "" || finished.CallID == "" {
		t.Fatalf("id-less upstream tool call must mint a call id: started=%+v finished=%+v", started, finished)
	}
	if started.CallID != finished.CallID {
		t.Fatalf("call id changed mid-turn: %q vs %q", started.CallID, finished.CallID)
	}
	if finished.Name != "get_weather" || string(finished.Arguments) != `{"city":"sf"}` {
		t.Fatalf("minted tool call = %+v", finished)
	}
}

func TestAggregateToolCallWithoutIDMintsCallID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprint(w, `{"id":"c","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"type":"function","function":{"name":"get_weather","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
	}))
	defer srv.Close()
	events := &collector{}
	r := New(staticKey, Options{})
	if err := r.Run(context.Background(), provider.RunRequest{Request: testRequest(false), Target: testTarget(srv.URL)}, events); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, ev := range events.All() {
		if fin, ok := ev.(canon.ItemFinished); ok {
			if fc, ok := fin.Item.(canon.FunctionCall); ok {
				if fc.CallID == "" {
					t.Fatalf("id-less aggregate tool call must mint a call id: %+v", fc)
				}
				return
			}
		}
	}
	t.Fatal("no FunctionCall item in events")
}

func TestToolCallIDRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"chatcmpl-rt","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"type":"function","function":{"name":"get_weather","arguments":""}}]}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"chatcmpl-rt","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	events := &collector{}
	r := New(staticKey, Options{})
	if err := r.Run(context.Background(), provider.RunRequest{Request: testRequest(true), Target: testTarget(srv.URL)}, events); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var buf bytes.Buffer
	eg := egresschat.New(&buf, true)
	if err := eg.Begin(egresschat.ResponseHeader{ID: "resp_rt", Model: "gpt-5.2", CreatedAt: time.Unix(1700000000, 0)}); err != nil {
		t.Fatalf("begin: %v", err)
	}
	for _, ev := range events.All() {
		if err := eg.Frame(ev); err != nil {
			t.Fatalf("frame %T: %v", ev, ev)
		}
	}
	if err := eg.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	wireID := ""
	for _, part := range strings.Split(buf.String(), "\n\n") {
		if !strings.HasPrefix(strings.TrimSpace(part), "data: ") {
			continue
		}
		if strings.TrimSpace(part) == "data: [DONE]" {
			continue
		}
		var frame struct {
			Choices []struct {
				Delta struct {
					ToolCalls []struct {
						ID string `json:"id"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimSpace(part), "data: ")), &frame); err != nil {
			t.Fatalf("decode frame %q: %v", part, err)
		}
		if len(frame.Choices) > 0 && len(frame.Choices[0].Delta.ToolCalls) > 0 && frame.Choices[0].Delta.ToolCalls[0].ID != "" {
			wireID = frame.Choices[0].Delta.ToolCalls[0].ID
		}
	}
	if wireID == "" {
		t.Fatalf("wire tool_calls.id must not be empty: %q", buf.String())
	}

	args, err := json.Marshal(map[string]string{"city": "sf"})
	if err != nil {
		t.Fatal(err)
	}
	argsString, err := json.Marshal(string(args))
	if err != nil {
		t.Fatal(err)
	}
	nextBody := fmt.Sprintf(`{"model":"a/b","messages":[`+
		`{"role":"user","content":"weather?"},`+
		`{"role":"assistant","content":null,"tool_calls":[{"id":%q,"type":"function","function":{"name":"get_weather","arguments":%s}}]},`+
		`{"role":"tool","tool_call_id":%q,"content":"sunny"}]}`, wireID, argsString, wireID)
	hr, err := http.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(nextBody))
	if err != nil {
		t.Fatal(err)
	}
	parsed, _, err := ingresschat.Ingress{}.Parse(context.Background(), hr)
	if err != nil {
		t.Fatalf("next turn parse: %v", err)
	}
	var call canon.FunctionCall
	var output canon.FunctionOutput
	for _, item := range parsed.Input {
		switch it := item.(type) {
		case canon.FunctionCall:
			call = it
		case canon.FunctionOutput:
			output = it
		}
	}
	if call.CallID == "" || call.CallID != canon.CallID(wireID) {
		t.Fatalf("next-turn call id = %q, want wire id %q", call.CallID, wireID)
	}
	if output.CallID == "" || output.CallID != canon.CallID(wireID) {
		t.Fatalf("next-turn tool_call_id = %q, want wire id %q", output.CallID, wireID)
	}

	up, err := r.buildUpstream(testTarget("https://example.com/v1/"), "sk-test", parsed)
	if err != nil {
		t.Fatalf("buildUpstream: %v", err)
	}
	if !strings.Contains(string(up.Body), `"id":`+strconv.Quote(wireID)) || !strings.Contains(string(up.Body), `"tool_call_id":`+strconv.Quote(wireID)) {
		t.Fatalf("upstream next-turn body lost the pairing id %q: %s", wireID, up.Body)
	}
}
