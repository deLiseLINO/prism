package anthropic

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"prism/internal/canon"
	"prism/internal/provider"
)

func f64(v float64) *float64 { return &v }

func baseRequest() canon.Request {
	return canon.Request{
		Model:           "claude-prism-anthropic--claude-sonnet-4-5",
		Stream:          true,
		MaxOutputTokens: 512,
		Instructions:    []canon.Content{canon.TextContent{Text: "be brief"}},
		Input: []canon.Item{
			canon.Message{ID: "m1", Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "hi"}}},
		},
		Sampling: canon.Sampling{
			Temperature: f64(0.5),
			TopP:        f64(0.9),
			Stop:        []string{"END"},
		},
	}
}

func decodeBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return payload
}

func TestRequestBuild(t *testing.T) {
	runner := New(Options{})
	out, err := runner.buildRequest(provider.RunRequest{
		Request: baseRequest(),
		Target:  provider.Target{APIKeyRef: "sk-test", Model: "claude-prism-anthropic--claude-sonnet-4-5"},
	})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if out.url != "https://api.anthropic.com/v1/messages" {
		t.Fatalf("url = %q", out.url)
	}
	wantOrder := []string{"Content-Type", "Accept", "x-api-key", "anthropic-version"}
	if len(out.headers) != len(wantOrder) {
		t.Fatalf("headers = %v", out.headers)
	}
	for i, name := range wantOrder {
		if out.headers[i].name != name {
			t.Fatalf("header[%d] = %s, want %s", i, out.headers[i].name, name)
		}
	}
	if v, ok := out.headers.Get("x-api-key"); !ok || v != "sk-test" {
		t.Fatalf("x-api-key = %q %t", v, ok)
	}
	if v, ok := out.headers.Get("anthropic-version"); !ok || v != DefaultAPIVersion {
		t.Fatalf("anthropic-version = %q %t", v, ok)
	}
	body := decodeBody(t, out.body)
	if body["model"] != "claude-prism-anthropic--claude-sonnet-4-5" {
		t.Fatalf("model = %v", body["model"])
	}
	if body["max_tokens"] != float64(512) {
		t.Fatalf("max_tokens = %v", body["max_tokens"])
	}
	if body["stream"] != true {
		t.Fatalf("stream = %v", body["stream"])
	}
	system, ok := body["system"].([]any)
	if !ok || len(system) != 1 {
		t.Fatalf("system = %v", body["system"])
	}
	first, _ := system[0].(map[string]any)
	if first["type"] != "text" || first["text"] != "be brief" {
		t.Fatalf("system[0] = %v", first)
	}
	messages, _ := body["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("messages = %v", messages)
	}
	msg, _ := messages[0].(map[string]any)
	if msg["role"] != "user" {
		t.Fatalf("role = %v", msg["role"])
	}
	if _, ok := body["temperature"]; !ok {
		t.Fatalf("temperature missing")
	}
	if _, ok := body["top_p"]; !ok {
		t.Fatalf("top_p missing")
	}
	stops, _ := body["stop_sequences"].([]any)
	if len(stops) != 1 || stops[0] != "END" {
		t.Fatalf("stop_sequences = %v", stops)
	}
	if _, ok := body["cache_control"]; ok {
		t.Fatalf("cache_control synthesized")
	}
	if _, ok := body["thinking"]; ok {
		t.Fatalf("thinking emitted without effort")
	}
}

func TestImageDataBase64Encoded(t *testing.T) {
	runner := New(Options{})
	request := baseRequest()
	request.Input = []canon.Item{
		canon.Message{ID: "m1", Role: canon.RoleUser, Content: []canon.Content{
			canon.ImageContent{MIMEType: "image/png", Data: []byte{0x89, 0x50, 0x4e, 0x47}},
		}},
	}
	out, err := runner.buildRequest(provider.RunRequest{
		Request: request,
		Target:  provider.Target{APIKeyRef: "sk-test", Model: "claude-prism-anthropic--claude-sonnet-4-5"},
	})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	body := decodeBody(t, out.body)
	messages, _ := body["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("messages = %v", body["messages"])
	}
	msg, _ := messages[0].(map[string]any)
	content, _ := msg["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content = %v", msg["content"])
	}
	block, _ := content[0].(map[string]any)
	source, _ := block["source"].(map[string]any)
	if source["data"] != "iVBORw==" {
		t.Fatalf("image data = %v, want base64", source["data"])
	}
}

func TestEmptyTextBlocksSkipped(t *testing.T) {
	runner := New(Options{})
	request := baseRequest()
	request.Input = []canon.Item{
		canon.Message{ID: "m1", Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: ""}}},
		canon.Message{ID: "m2", Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "hi"}}},
	}
	out, err := runner.buildRequest(provider.RunRequest{
		Request: request,
		Target:  provider.Target{APIKeyRef: "sk-test", Model: "claude-prism-anthropic--claude-sonnet-4-5"},
	})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	body := decodeBody(t, out.body)
	messages, _ := body["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("messages = %v, want only the non-empty one", body["messages"])
	}
}

func TestToolResultKeepsImageContent(t *testing.T) {
	runner := New(Options{})
	request := baseRequest()
	request.Input = []canon.Item{
		canon.Message{ID: "m1", Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "shot?"}}},
		canon.FunctionCall{ID: "fc1", CallID: "toolu_1", Name: "camera", Arguments: []byte(`{}`)},
		canon.FunctionOutput{ID: "fo1", CallID: "toolu_1", Output: []canon.Content{
			canon.ImageContent{MIMEType: "image/png", Data: []byte{0x89, 0x50}},
		}},
	}
	out, err := runner.buildRequest(provider.RunRequest{
		Request: request,
		Target:  provider.Target{APIKeyRef: "sk-test", Model: "claude-prism-anthropic--claude-sonnet-4-5"},
	})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	body := decodeBody(t, out.body)
	messages, _ := body["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("messages = %v", body["messages"])
	}
	msg, _ := messages[2].(map[string]any)
	content, _ := msg["content"].([]any)
	result, _ := content[0].(map[string]any)
	inner, _ := result["content"].([]any)
	block, _ := inner[0].(map[string]any)
	if block["type"] != "image" {
		t.Fatalf("tool result content = %v, want image block", inner)
	}
	source, _ := block["source"].(map[string]any)
	if source["data"] != "iVA=" {
		t.Fatalf("tool image data = %v, want base64", source["data"])
	}
}

func TestEmptyToolResultFailsLoud(t *testing.T) {
	runner := New(Options{})
	request := baseRequest()
	request.Input = []canon.Item{
		canon.Message{ID: "m1", Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "hi"}}},
		canon.FunctionCall{ID: "fc1", CallID: "toolu_1", Name: "noop", Arguments: []byte(`{}`)},
		canon.FunctionOutput{ID: "fo1", CallID: "toolu_1", Output: []canon.Content{canon.TextContent{Text: ""}}},
	}
	_, err := runner.buildRequest(provider.RunRequest{
		Request: request,
		Target:  provider.Target{APIKeyRef: "sk-test", Model: "claude-prism-anthropic--claude-sonnet-4-5"},
	})
	if err == nil {
		t.Fatal("buildRequest with empty tool result: want error, got nil")
	}
}

func TestRequestBuildTooling(t *testing.T) {
	request := baseRequest()
	request.Input = []canon.Item{
		canon.Message{ID: "m1", Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "weather?"}}},
		canon.FunctionCall{ID: "fc1", CallID: "toolu_1", Name: "get_weather", Arguments: []byte(`{"city":"Paris"}`)},
		canon.FunctionOutput{ID: "fo1", CallID: "toolu_1", Output: []canon.Content{canon.TextContent{Text: "sunny"}}},
	}
	request.Tools = []canon.Tool{
		canon.FunctionTool{Name: "get_weather", Description: "weather lookup", Parameters: []byte(`{"type":"object","properties":{"city":{"type":"string"}}}`)},
	}
	request.ToolChoice = canon.ToolRequired{}
	runner := New(Options{})
	out, err := runner.buildRequest(provider.RunRequest{Request: request, Target: provider.Target{APIKeyRef: "k"}})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	body := decodeBody(t, out.body)
	messages, _ := body["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("messages = %v", messages)
	}
	call, _ := messages[1].(map[string]any)
	if call["role"] != "assistant" {
		t.Fatalf("call role = %v", call["role"])
	}
	blocks, _ := call["content"].([]any)
	use, _ := blocks[0].(map[string]any)
	if use["type"] != "tool_use" || use["id"] != "toolu_1" || use["name"] != "get_weather" {
		t.Fatalf("tool_use block = %v", use)
	}
	input, _ := use["input"].(map[string]any)
	if input["city"] != "Paris" {
		t.Fatalf("tool input = %v", input)
	}
	result, _ := messages[2].(map[string]any)
	resultBlocks, _ := result["content"].([]any)
	toolResult, _ := resultBlocks[0].(map[string]any)
	if toolResult["type"] != "tool_result" || toolResult["tool_use_id"] != "toolu_1" {
		t.Fatalf("tool_result block = %v", toolResult)
	}
	tools, _ := body["tools"].([]any)
	tool, _ := tools[0].(map[string]any)
	if tool["name"] != "get_weather" {
		t.Fatalf("tool = %v", tool)
	}
	schema, _ := tool["input_schema"].(map[string]any)
	if schema["type"] != "object" {
		t.Fatalf("input_schema = %v", schema)
	}
	choice, _ := body["tool_choice"].(map[string]any)
	if choice["type"] != "any" {
		t.Fatalf("tool_choice = %v", choice)
	}
}

func TestToolChoiceMapping(t *testing.T) {
	cases := []struct {
		choice canon.ToolChoice
		want   string
	}{
		{canon.ToolAuto{}, "auto"},
		{canon.ToolNone{}, "none"},
		{canon.ToolRequired{}, "any"},
		{canon.ToolNamed{Name: "get_weather"}, "tool"},
		{canon.ToolAllowed{Mode: canon.AllowedRequired}, "any"},
		{canon.ToolAllowed{Mode: canon.AllowedAuto}, "auto"},
	}
	runner := New(Options{})
	for _, tc := range cases {
		request := baseRequest()
		request.ToolChoice = tc.choice
		out, err := runner.buildRequest(provider.RunRequest{Request: request, Target: provider.Target{APIKeyRef: "k"}})
		if err != nil {
			t.Fatalf("buildRequest %T: %v", tc.choice, err)
		}
		body := decodeBody(t, out.body)
		choice, _ := body["tool_choice"].(map[string]any)
		if choice["type"] != tc.want {
			t.Fatalf("%T tool_choice = %v, want %s", tc.choice, choice, tc.want)
		}
	}
}

func TestBudgetTablePerModel(t *testing.T) {
	cases := []struct {
		model  string
		effort canon.ReasoningEffort
		budget int
	}{
		{"claude-sonnet-4-5", canon.EffortMinimal, 1024},
		{"claude-sonnet-4-5", canon.EffortLow, 4096},
		{"claude-sonnet-4-5", canon.EffortMedium, 8192},
		{"claude-sonnet-4-5", canon.EffortHigh, 16384},
		{"claude-sonnet-4-5", canon.EffortXHigh, 24576},
		{"claude-opus-4-1", canon.EffortHigh, 16384},
		{"claude-haiku-4-5", canon.EffortLow, 4096},
		{"claude-fable-2", canon.EffortMedium, 8192},
		{"unknown-model", canon.EffortHigh, 16384},
	}
	for _, tc := range cases {
		budget, ok := budgetFor(canon.ModelID(tc.model), tc.effort)
		if !ok || budget != tc.budget {
			t.Fatalf("budgetFor(%q, %d) = %d %t, want %d", tc.model, tc.effort, budget, ok, tc.budget)
		}
	}
}

func TestThinkingRequestShape(t *testing.T) {
	request := baseRequest()
	request.Reasoning.Effort = canon.EffortHigh
	runner := New(Options{})
	out, err := runner.buildRequest(provider.RunRequest{Request: request, Target: provider.Target{APIKeyRef: "k"}})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	body := decodeBody(t, out.body)
	thinking, ok := body["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking = %v", body["thinking"])
	}
	if thinking["type"] != "enabled" || thinking["budget_tokens"] != float64(16384) {
		t.Fatalf("thinking = %v", thinking)
	}
	if body["max_tokens"] != float64(24576) {
		t.Fatalf("max_tokens = %v, want budget+headroom", body["max_tokens"])
	}
	if _, ok := body["temperature"]; ok {
		t.Fatalf("temperature kept with thinking enabled")
	}
	if _, ok := body["top_p"]; ok {
		t.Fatalf("top_p kept with thinking enabled")
	}
}

func TestThinkingBudgetCappedAtCeiling(t *testing.T) {
	request := baseRequest()
	request.Reasoning.Effort = canon.EffortXHigh
	runner := New(Options{})
	out, err := runner.buildRequest(provider.RunRequest{Request: request, Target: provider.Target{APIKeyRef: "k"}})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	body := decodeBody(t, out.body)
	if body["max_tokens"] != float64(maxTokensCeiling) {
		t.Fatalf("max_tokens = %v, want ceiling %d", body["max_tokens"], maxTokensCeiling)
	}
}

func TestMessagesURLNormalization(t *testing.T) {
	cases := map[string]string{
		"https://api.anthropic.com":             "https://api.anthropic.com/v1/messages",
		"https://api.anthropic.com/":            "https://api.anthropic.com/v1/messages",
		"https://api.anthropic.com/v1":          "https://api.anthropic.com/v1/messages",
		"https://api.anthropic.com/v1/":         "https://api.anthropic.com/v1/messages",
		"https://api.anthropic.com/v1/messages": "https://api.anthropic.com/v1/messages",
	}
	for base, want := range cases {
		if got := messagesURL(base); got != want {
			t.Fatalf("messagesURL(%q) = %q, want %q", base, got, want)
		}
	}
}

func TestBetaHeadersApplied(t *testing.T) {
	var got []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Values("Anthropic-Beta")
		w.Write([]byte("event: message_start\ndata: {}\n\nevent: message_stop\ndata: {}\n\n"))
	}))
	defer server.Close()
	runner := New(Options{BaseURL: server.URL, Beta: []string{"beta-one", "beta-two"}})
	request := baseRequest()
	err := runner.Run(t.Context(), provider.RunRequest{
		Request: request,
		Target:  provider.Target{BaseURL: server.URL, APIKeyRef: "k"},
	}, &collectingSink{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 2 || got[0] != "beta-one" || got[1] != "beta-two" {
		t.Fatalf("anthropic-beta = %v", got)
	}
}

func TestCountTokens(t *testing.T) {
	var gotPath, gotMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"input_tokens":42}`))
	}))
	defer server.Close()
	runner := New(Options{})
	count, err := runner.CountTokens(t.Context(), provider.CountTokensRequest{
		Target: provider.Target{BaseURL: server.URL, APIKeyRef: "k", Model: "claude-sonnet-4-5"},
		Input:  []canon.Item{canon.Message{ID: "m1", Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("CountTokens: %v", err)
	}
	if count.InputTokens != 42 {
		t.Fatalf("input tokens = %d", count.InputTokens)
	}
	if gotPath != "/v1/messages/count_tokens" || gotMethod != http.MethodPost {
		t.Fatalf("count_tokens request = %s %s", gotMethod, gotPath)
	}
}
