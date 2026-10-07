package anthropic

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
	"github.com/deLiseLINO/prism/internal/reasonenv"
)

func f64(v float64) *float64 { return &v }

func baseRequest() canon.Request {
	return canon.Request{
		Model:           "claude-anthropic--claude-sonnet-4-5",
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
		Target:  provider.Target{APIKeyRef: "sk-test", Model: "claude-anthropic--claude-sonnet-4-5"},
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
	if body["model"] != "claude-anthropic--claude-sonnet-4-5" {
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
		Target:  provider.Target{APIKeyRef: "sk-test", Model: "claude-anthropic--claude-sonnet-4-5"},
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
		Target:  provider.Target{APIKeyRef: "sk-test", Model: "claude-anthropic--claude-sonnet-4-5"},
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

func TestSystemAndDeveloperMessagesFoldIntoSystem(t *testing.T) {
	text := func(s string) []canon.Content { return []canon.Content{canon.TextContent{Text: s}} }
	user := canon.Message{ID: "u", Role: canon.RoleUser, Content: text("hi")}
	cases := []struct {
		name         string
		instructions []canon.Content
		input        []canon.Item
		wantSystem   []string
		wantMessages int
	}{
		{
			name:         "system then user",
			input:        []canon.Item{canon.Message{Role: canon.RoleSystem, Content: text("rules")}, user},
			wantSystem:   []string{"rules"},
			wantMessages: 1,
		},
		{
			name:         "developer then user",
			input:        []canon.Item{canon.Message{Role: canon.RoleDeveloper, Content: text("dev rules")}, user},
			wantSystem:   []string{"dev rules"},
			wantMessages: 1,
		},
		{
			name:         "instructions precede folded messages in order",
			instructions: text("first"),
			input: []canon.Item{
				canon.Message{Role: canon.RoleSystem, Content: text("second")},
				user,
				canon.Message{Role: canon.RoleDeveloper, Content: text("third")},
			},
			wantSystem:   []string{"first", "second", "third"},
			wantMessages: 1,
		},
		{
			name:         "empty system text skipped",
			input:        []canon.Item{canon.Message{Role: canon.RoleSystem, Content: text("")}, user},
			wantMessages: 1,
		},
		{
			name:       "only system message leaves no messages",
			input:      []canon.Item{canon.Message{Role: canon.RoleSystem, Content: text("rules")}},
			wantSystem: []string{"rules"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := baseRequest()
			request.Instructions = tc.instructions
			request.Input = tc.input
			out, err := New(Options{}).buildRequest(provider.RunRequest{
				Request: request,
				Target:  provider.Target{APIKeyRef: "sk-test", Model: "claude-anthropic--claude-sonnet-4-5"},
			})
			if err != nil {
				t.Fatalf("buildRequest: %v", err)
			}
			body := decodeBody(t, out.body)
			blocks, _ := body["system"].([]any)
			if len(blocks) != len(tc.wantSystem) {
				t.Fatalf("system = %v, want %v", body["system"], tc.wantSystem)
			}
			for i, want := range tc.wantSystem {
				block, _ := blocks[i].(map[string]any)
				if block["text"] != want {
					t.Fatalf("system[%d] = %v, want %q", i, block["text"], want)
				}
			}
			messages, _ := body["messages"].([]any)
			if len(messages) != tc.wantMessages {
				t.Fatalf("messages = %v, want %d", body["messages"], tc.wantMessages)
			}
			for _, m := range messages {
				if role := m.(map[string]any)["role"]; role != "user" {
					t.Fatalf("message role = %v, want user", role)
				}
			}
		})
	}
}

func TestSystemMessageWithImageFailsLoud(t *testing.T) {
	request := baseRequest()
	request.Input = []canon.Item{
		canon.Message{Role: canon.RoleSystem, Content: []canon.Content{canon.ImageContent{MIMEType: "image/png", Data: []byte{0x89}}}},
		canon.Message{ID: "u", Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "hi"}}},
	}
	_, err := New(Options{}).buildRequest(provider.RunRequest{
		Request: request,
		Target:  provider.Target{APIKeyRef: "sk-test", Model: "claude-anthropic--claude-sonnet-4-5"},
	})
	if err == nil {
		t.Fatal("buildRequest with image in system message: want error, got nil")
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
		Target:  provider.Target{APIKeyRef: "sk-test", Model: "claude-anthropic--claude-sonnet-4-5"},
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

func TestEmptyToolResultIsSentWithoutContent(t *testing.T) {
	runner := New(Options{})
	request := baseRequest()
	request.Input = []canon.Item{
		canon.Message{ID: "m1", Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "hi"}}},
		canon.FunctionCall{ID: "fc1", CallID: "toolu_1", Name: "noop", Arguments: []byte(`{}`)},
		canon.FunctionOutput{ID: "fo1", CallID: "toolu_1", Output: []canon.Content{canon.TextContent{Text: ""}}},
	}
	out, err := runner.buildRequest(provider.RunRequest{
		Request: request,
		Target:  provider.Target{APIKeyRef: "sk-test", Model: "claude-anthropic--claude-sonnet-4-5"},
	})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	messages, _ := decodeBody(t, out.body)["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("messages = %v", messages)
	}
	msg, _ := messages[2].(map[string]any)
	content, _ := msg["content"].([]any)
	result, _ := content[0].(map[string]any)
	if result["type"] != "tool_result" || result["tool_use_id"] != "toolu_1" {
		t.Fatalf("result = %v", result)
	}
	if _, has := result["content"]; has {
		t.Fatalf("empty result carries content: %v", result["content"])
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
		{"claude-sonnet-4-5", canon.EffortMax, 28672},
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

func TestEffortOffOmitsThinking(t *testing.T) {
	request := baseRequest()
	request.Reasoning.Effort = canon.EffortOff
	runner := New(Options{})
	out, err := runner.buildRequest(provider.RunRequest{Request: request, Target: provider.Target{APIKeyRef: "k"}})
	if err != nil {
		t.Fatalf("buildRequest(effort off): %v", err)
	}
	body := decodeBody(t, out.body)
	if _, ok := body["thinking"]; ok {
		t.Fatalf("thinking emitted for effort off: %v", body["thinking"])
	}
}

func TestThinkingRequestShape(t *testing.T) {
	request := baseRequest()
	request.Reasoning.Effort = canon.EffortHigh
	request.Sampling.Temperature, request.Sampling.TopP = nil, nil
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
	request.Sampling.Temperature, request.Sampling.TopP = nil, nil
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

const realSignature = "EqoBCkYIBhgCKkCq1v2JQ0m8n3Xr7dUeYt5bLwPzHc9sAoFgKiVjNxTRe4MhDl6uZaWyB0Sp1QfGtCk2OvEnI8rXbJmHdLs3UwYaTq7Ne5Pc9ZhKfVgR0oA=="

func TestEmptyThinkingIsReplayedWithThinkingField(t *testing.T) {
	request := baseRequest()
	request.Input = append(request.Input, canon.ReasoningItem{ID: "r1", Signature: realSignature})
	out, err := New(Options{}).buildRequest(provider.RunRequest{
		Request: request,
		Target:  provider.Target{APIKeyRef: "k", Model: "claude-anthropic--claude-sonnet-4-5"},
	})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	messages := decodeBody(t, out.body)["messages"].([]any)
	blocks := messages[1].(map[string]any)["content"].([]any)
	block := blocks[0].(map[string]any)
	if v, ok := block["thinking"]; !ok || v != "" {
		t.Fatalf("thinking block = %v, want thinking field present and empty", block)
	}
	if block["signature"] != realSignature {
		t.Fatalf("signature = %v", block["signature"])
	}
}

func TestReasoningReplayRules(t *testing.T) {
	cases := []struct {
		name  string
		item  canon.ReasoningItem
		state string
		want  string
	}{
		{"empty item", canon.ReasoningItem{ID: "r1"}, "", `[]`},
		{"empty signature with text", canon.ReasoningItem{ID: "r1", Content: "pondering"}, "", `[]`},
		{"text-only envelope", canon.ReasoningItem{ID: "r1", Content: "pondering", Signature: reasonenv.Encode("pondering")}, "", `[]`},
		{
			"redacted envelope skips empty entries",
			canon.ReasoningItem{ID: "r1", Signature: reasonenv.EncodeRedacted([]string{"", "opaque-blob"})},
			"",
			`[{"type":"redacted_thinking","data":"opaque-blob"}]`,
		},
		{"openai style id", canon.ReasoningItem{ID: "r1", Content: "pondering", Signature: "rs_0123456789abcdef0123"}, "", `[]`},
		{"short string", canon.ReasoningItem{ID: "r1", Content: "pondering", Signature: "sig-abc"}, "", `[]`},
		{"foreign charset", canon.ReasoningItem{ID: "r1", Content: "pondering", Signature: "not a signature at all!!"}, "", `[]`},
		{
			"realistic signature",
			canon.ReasoningItem{ID: "r1", Content: "pondering", Signature: realSignature},
			"",
			`[{"type":"thinking","thinking":"pondering","signature":"` + realSignature + `"}]`,
		},
		{
			"state store blob trusted",
			canon.ReasoningItem{ID: "r1", Content: "pondering", State: canon.OpaqueRef{Store: stateStoreName, Key: "r1"}},
			"sig-abc",
			`[{"type":"thinking","thinking":"pondering","signature":"sig-abc"}]`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := New(Options{})
			if tc.state != "" {
				runner.state.put("r1", []byte(tc.state))
			}
			messages, _, err := runner.messagesFromItems([]canon.Item{tc.item})
			if err != nil {
				t.Fatalf("messagesFromItems: %v", err)
			}
			var blocks []wireBlock
			for _, m := range messages {
				blocks = append(blocks, m.Content...)
			}
			if blocks == nil {
				blocks = []wireBlock{}
			}
			got, err := json.Marshal(blocks)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("blocks = %s, want %s", got, tc.want)
			}
		})
	}
}

func buildBody(t *testing.T, request canon.Request) map[string]any {
	t.Helper()
	out, err := New(Options{}).buildRequest(provider.RunRequest{Request: request, Target: provider.Target{APIKeyRef: "k"}})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	return decodeBody(t, out.body)
}

func TestCustomToolLowering(t *testing.T) {
	cases := []struct {
		name string
		tool canon.CustomToolDef
		want string
	}{
		{
			name: "apply_patch",
			tool: canon.CustomToolDef{Name: "apply_patch", Description: "edit files"},
			want: "{\"description\":\"edit files\",\"input_schema\":{\"properties\":{\"input\":{\"description\":\"Raw tool input. For apply_patch, begin exactly with `*** Begin Patch` (no trailing `***`), then use its standard patch envelope.\",\"type\":\"string\"}},\"required\":[\"input\"],\"type\":\"object\"},\"name\":\"apply_patch\"}",
		},
		{
			name: "generic",
			tool: canon.CustomToolDef{Name: "exec", Description: "run code"},
			want: `{"description":"run code","input_schema":{"properties":{"input":{"description":"Raw freeform input for this tool.","type":"string"}},"required":["input"],"type":"object"},"name":"exec"}`,
		},
		{
			name: "format and grammar ignored",
			tool: canon.CustomToolDef{
				Name:    "exec",
				Format:  canon.FormatText,
				Grammar: &canon.ToolGrammar{Syntax: "lark", Definition: "start: /.+/"},
			},
			want: `{"input_schema":{"properties":{"input":{"description":"Raw freeform input for this tool.","type":"string"}},"required":["input"],"type":"object"},"name":"exec"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := baseRequest()
			request.Tools = []canon.Tool{tc.tool}
			body := buildBody(t, request)
			tools, _ := body["tools"].([]any)
			if len(tools) != 1 {
				t.Fatalf("tools = %v", tools)
			}
			got, _ := json.Marshal(tools[0])
			if string(got) != tc.want {
				t.Fatalf("tool = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestCustomToolNamedChoice(t *testing.T) {
	request := baseRequest()
	request.Tools = []canon.Tool{canon.CustomToolDef{Name: "apply_patch"}}
	request.ToolChoice = canon.ToolNamed{Name: "apply_patch"}
	body := buildBody(t, request)
	got, _ := json.Marshal(body["tool_choice"])
	if string(got) != `{"name":"apply_patch","type":"tool"}` {
		t.Fatalf("tool_choice = %s", got)
	}
}

func TestCustomToolHistoryRoundTrip(t *testing.T) {
	cases := []struct {
		name  string
		items []canon.Item
		want  string
	}{
		{
			name: "quotes newlines and unicode",
			items: []canon.Item{
				canon.CustomToolCall{ID: "ct1", CallID: "call_1", Name: "apply_patch", Input: "*** Begin Patch\nsay \"héllo\" \\ 日本\n*** End Patch"},
				canon.CustomToolOutput{ID: "co1", CallID: "call_1", Output: "done \"ok\"\n"},
			},
			want: `[{"content":[{"text":"hi","type":"text"}],"role":"user"},` +
				`{"content":[{"id":"call_1","input":{"input":"*** Begin Patch\nsay \"héllo\" \\ 日本\n*** End Patch"},"name":"apply_patch","type":"tool_use"}],"role":"assistant"},` +
				`{"content":[{"content":[{"text":"done \"ok\"\n","type":"text"}],"tool_use_id":"call_1","type":"tool_result"}],"role":"user"}]`,
		},
		{
			name: "empty output has no content blocks",
			items: []canon.Item{
				canon.CustomToolCall{ID: "ct1", CallID: "call_2", Name: "exec", Input: ""},
				canon.CustomToolOutput{ID: "co1", CallID: "call_2", Output: ""},
			},
			want: `[{"content":[{"text":"hi","type":"text"}],"role":"user"},` +
				`{"content":[{"id":"call_2","input":{"input":""},"name":"exec","type":"tool_use"}],"role":"assistant"},` +
				`{"content":[{"tool_use_id":"call_2","type":"tool_result"}],"role":"user"}]`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := baseRequest()
			request.Input = append(request.Input, tc.items...)
			body := buildBody(t, request)
			got, _ := json.Marshal(body["messages"])
			if string(got) != tc.want {
				t.Fatalf("messages = %s\nwant      %s", got, tc.want)
			}
		})
	}
}

func TestCustomToolMissingCallIDFailsLoud(t *testing.T) {
	cases := []struct {
		name string
		item canon.Item
	}{
		{"call", canon.CustomToolCall{ID: "ct1", Name: "exec", Input: "x"}},
		{"output", canon.CustomToolOutput{ID: "co1", Output: "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := baseRequest()
			request.Input = append(request.Input, tc.item)
			_, err := New(Options{}).buildRequest(provider.RunRequest{Request: request, Target: provider.Target{APIKeyRef: "k"}})
			if err == nil {
				t.Fatal("buildRequest without call id: want error, got nil")
			}
		})
	}
}

func TestToolsFromNormalizesRootSchema(t *testing.T) {
	cases := []struct {
		name   string
		params string
		want   string
	}{
		{"empty bytes", ``, `{"properties":{},"type":"object"}`},
		{"empty object", `{}`, `{"properties":{},"type":"object"}`},
		{"null", `null`, `{"properties":{},"type":"object"}`},
		{"array", `[]`, `{"properties":{},"type":"object"}`},
		{"string", `"x"`, `{"properties":{},"type":"object"}`},
		{"untyped with properties", `{"properties":{"a":{"type":"string"}}}`, `{"properties":{"a":{"type":"string"}},"type":"object"}`},
		{"type array", `{"type":["object","null"],"properties":{"a":{"type":"string"}}}`, `{"properties":{"a":{"type":"string"}},"type":"object"}`},
		{"properties null", `{"type":"object","properties":null}`, `{"properties":{},"type":"object"}`},
		{"required not array", `{"type":"object","properties":{},"required":"a"}`, `{"properties":{},"type":"object"}`},
		{"required mixed entries", `{"type":"object","properties":{"a":{},"b":{}},"required":["a",1,null,"b"]}`, `{"properties":{"a":{},"b":{}},"required":["a","b"],"type":"object"}`},
		{"other root keys kept", `{"type":"object","properties":{},"additionalProperties":false}`, `{"additionalProperties":false,"properties":{},"type":"object"}`},
		{
			"root anyOf",
			`{"anyOf":[{"properties":{"a":{"type":"string"}},"required":["a"]},{"properties":{"b":{"type":"number"}}}]}`,
			`{"properties":{"a":{"type":"string"},"b":{"type":"number"}},"type":"object"}`,
		},
		{
			"root oneOf",
			`{"type":"object","properties":{"a":{"type":"integer"}},"oneOf":[{"properties":{"a":{"type":"string"},"c":{"type":"boolean"}}},{"properties":{"c":{"type":"null"}}}]}`,
			`{"properties":{"a":{"type":"integer"},"c":{"type":"boolean"}},"type":"object"}`,
		},
		{
			"root allOf with required",
			`{"required":["z"],"allOf":[{"properties":{"a":{}},"required":["a","z"]},{"properties":{"b":{}},"required":["b"]},"junk"]}`,
			`{"properties":{"a":{},"b":{}},"required":["z","a","b"],"type":"object"}`,
		},
		{
			"nested combinator untouched",
			`{"type":"object","properties":{"a":{"anyOf":[{"type":"string"},{"type":"null"}]}}}`,
			`{"properties":{"a":{"anyOf":[{"type":"string"},{"type":"null"}]}},"type":"object"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tools, _, err := toolsFrom([]canon.Tool{canon.FunctionTool{Name: "t", Parameters: []byte(tc.params)}})
			if err != nil {
				t.Fatalf("toolsFrom: %v", err)
			}
			if got := string(tools[0].InputSchema); got != tc.want {
				t.Fatalf("input_schema = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestToolsFromRejectsInvalidJSONParameters(t *testing.T) {
	_, _, err := toolsFrom([]canon.Tool{canon.FunctionTool{Name: "t", Parameters: []byte(`{"type":`)}})
	if err == nil || !strings.Contains(err.Error(), "malformed parameters") {
		t.Fatalf("err = %v, want malformed parameters", err)
	}
}

func TestToolsFromSkipsToolsTheUpstreamRejects(t *testing.T) {
	tools, skipped, err := toolsFrom([]canon.Tool{
		canon.FunctionTool{Name: "ok", Parameters: []byte(`{"type":"object","properties":{"a.b-c_1":{"type":"string"}}}`)},
		canon.FunctionTool{Name: "bracket_key", Parameters: []byte(`{"type":"object","properties":{"not[assignee_id]":{"type":"string"}}}`)},
		canon.FunctionTool{Name: "nested_bad_key", Parameters: []byte(`{"type":"object","properties":{"f":{"type":"object","properties":{"$ref":{"type":"string"}}}}}`)},
		canon.FunctionTool{Name: "bad.name"},
		canon.FunctionTool{Name: canon.ToolName(strings.Repeat("n", 65))},
		canon.CustomToolDef{Name: "custom tool"},
		canon.FunctionTool{Name: "property_named_properties", Parameters: []byte(`{"type":"object","properties":{"properties":{"type":"string"}}}`)},
	})
	if err != nil {
		t.Fatalf("toolsFrom: %v", err)
	}
	var kept []string
	for _, tool := range tools {
		kept = append(kept, tool.Name)
	}
	if got, want := strings.Join(kept, ","), "ok,property_named_properties"; got != want {
		t.Errorf("kept = %s, want %s", got, want)
	}
	if got, want := len(skipped), 5; got != want {
		t.Errorf("skipped = %v, want %d entries", skipped, want)
	}
}
