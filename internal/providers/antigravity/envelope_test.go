package antigravity

import (
	"strings"
	"testing"

	"prism/internal/canon"
)

func baseRequest() canon.Request {
	temp := 0.7
	return canon.Request{
		Model:           "gemini-3.7-flash",
		Instructions:    []canon.Content{canon.TextContent{Text: "You are helpful."}},
		MaxOutputTokens: 512,
		Sampling:        canon.Sampling{Temperature: &temp},
	}
}

func textMessage(role canon.Role, text string) canon.Message {
	return canon.Message{ID: "m1", Role: role, Content: []canon.Content{canon.TextContent{Text: text}}}
}

func TestEnvelopeKeyOrderAndValues(t *testing.T) {
	req := baseRequest()
	req.Input = []canon.Item{textMessage(canon.RoleUser, "Hi")}
	body, err := BuildEnvelope(req, "proj-1", "agent-abc", "-123")
	if err != nil {
		t.Fatalf("BuildEnvelope: %v", err)
	}
	got := string(body)
	const want = `{"model":"gemini-3.7-flash","userAgent":"antigravity","requestType":"agent","project":"proj-1","requestId":"agent-abc",` +
		`"request":{"contents":[{"role":"user","parts":[{"text":"Hi"}]}],` +
		`"systemInstruction":{"parts":[{"text":"You are helpful."}]},"generationConfig":{"maxOutputTokens":512,"temperature":0.7},"sessionId":"-123"}}`
	if got != want {
		t.Fatalf("envelope mismatch:\n got %s\nwant %s", got, want)
	}
}

func TestEnvelopeFieldOrdering(t *testing.T) {
	req := baseRequest()
	req.Input = []canon.Item{textMessage(canon.RoleUser, "Hi")}
	body, err := BuildEnvelope(req, "p", "r", "-1")
	if err != nil {
		t.Fatalf("BuildEnvelope: %v", err)
	}
	got := string(body)
	keys := []string{`"model":`, `"userAgent":`, `"requestType":`, `"project":`, `"requestId":`, `"request":`}
	pos := 0
	for _, key := range keys {
		idx := strings.Index(got[pos:], key)
		if idx < 0 {
			t.Fatalf("key %s out of order in %s", key, got)
		}
		pos += idx + len(key)
	}
	bodyKeys := []string{`"contents":`, `"systemInstruction":`, `"generationConfig":`, `"sessionId":`}
	pos = strings.Index(got, `"request":`) + len(`"request":`)
	for _, key := range bodyKeys {
		idx := strings.Index(got[pos:], key)
		if idx < 0 {
			t.Fatalf("request key %s out of order in %s", key, got)
		}
		pos += idx + len(key)
	}
}

func TestEnvelopeContentsMapping(t *testing.T) {
	req := baseRequest()
	req.Instructions = nil
	req.MaxOutputTokens = 0
	req.Sampling = canon.Sampling{}
	imageData := []byte{0x89, 0x50, 0x4e, 0x47}
	req.Input = []canon.Item{
		textMessage(canon.RoleUser, "look"),
		canon.Message{
			ID:   "m2",
			Role: canon.RoleUser,
			Content: []canon.Content{
				canon.ImageContent{MIMEType: "image/png", Data: imageData},
			},
		},
		canon.ReasoningItem{ID: "r1", Content: "hmm", Signature: "AbCdEf123456+/7890_-sig"},
		canon.FunctionCall{
			ID: "fc1", CallID: "call_1", Name: "get_weather",
			Arguments: []byte(`{"city":"Paris"}`),
			State:     canon.OpaqueRef{Store: "antigravity", Key: "XyZ987654321+/abc=-sig"},
		},
		canon.FunctionOutput{
			ID: "fo1", CallID: "call_1",
			Output: []canon.Content{canon.TextContent{Text: "sunny"}},
		},
		textMessage(canon.RoleAssistant, "done"),
	}
	body, err := BuildEnvelope(req, "p", "r", "-1")
	if err != nil {
		t.Fatalf("BuildEnvelope: %v", err)
	}
	got := string(body)
	expectations := []string{
		`{"role":"user","parts":[{"text":"look"}]}`,
		`{"inline_data":{"mime_type":"image/png","data":"iVBORw=="}}`,
		`{"text":"hmm","thought":true,"thoughtSignature":"AbCdEf123456+/7890_-sig"}`,
		`{"functionCall":{"name":"get_weather","args":{"city":"Paris"},"id":"call_1"},"thoughtSignature":"XyZ987654321+/abc=-sig"}`,
		`{"functionResponse":{"name":"get_weather","response":{"result":"sunny"},"id":"call_1"}}`,
		`{"role":"model","parts":[{"text":"done"}]}`,
	}
	for _, want := range expectations {
		if !strings.Contains(got, want) {
			t.Fatalf("contents mapping missing %s in %s", want, got)
		}
	}
	if strings.Contains(got, "systemInstruction") {
		t.Fatalf("empty instructions must not emit systemInstruction: %s", got)
	}
}

func TestEnvelopeEmptyUserTurnPlaceholder(t *testing.T) {
	req := baseRequest()
	req.Instructions = nil
	req.MaxOutputTokens = 0
	req.Sampling = canon.Sampling{}
	req.Input = []canon.Item{
		canon.Message{ID: "m1", Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: ""}}},
	}
	body, err := BuildEnvelope(req, "p", "r", "-1")
	if err != nil {
		t.Fatalf("BuildEnvelope: %v", err)
	}
	if !strings.Contains(string(body), `{"text":"(empty)"}`) {
		t.Fatalf("missing empty placeholder: %s", body)
	}
}

func TestEnvelopeToolsAndToolChoice(t *testing.T) {
	req := baseRequest()
	req.MaxOutputTokens = 0
	req.Sampling = canon.Sampling{}
	req.Tools = []canon.Tool{canon.FunctionTool{
		Name: "get_weather", Description: "Get weather",
		Parameters: []byte(`{"type":"object"}`),
	}}
	req.ToolChoice = canon.ToolNamed{Name: "get_weather"}
	req.Input = []canon.Item{textMessage(canon.RoleUser, "Hi")}
	body, err := BuildEnvelope(req, "p", "r", "-1")
	if err != nil {
		t.Fatalf("BuildEnvelope: %v", err)
	}
	want := `"tools":[{"functionDeclarations":[{"name":"get_weather","description":"Get weather","parameters":{"type":"object"}}]}],` +
		`"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["get_weather"]}}`
	if !strings.Contains(string(body), want) {
		t.Fatalf("tools mapping mismatch: %s", body)
	}

	req.ToolChoice = canon.ToolNone{}
	body, _ = BuildEnvelope(req, "p", "r", "-1")
	if !strings.Contains(string(body), `"mode":"NONE"`) {
		t.Fatalf("tool none mapping mismatch: %s", body)
	}
	req.ToolChoice = canon.ToolRequired{}
	body, _ = BuildEnvelope(req, "p", "r", "-1")
	if !strings.Contains(string(body), `"toolConfig":{"functionCallingConfig":{"mode":"ANY"}}`) {
		t.Fatalf("tool required mapping mismatch: %s", body)
	}
}

func TestEnvelopeClaudeQuirks(t *testing.T) {
	req := baseRequest()
	req.Model = "claude-sonnet-4-5"
	req.Instructions = nil
	req.MaxOutputTokens = 0
	req.Sampling = canon.Sampling{}
	req.Tools = []canon.Tool{canon.FunctionTool{Name: "get_weather"}}
	req.ToolChoice = canon.ToolAuto{}
	req.Input = []canon.Item{
		canon.FunctionCall{
			ID: "fc1", CallID: "call_1", Name: "get_weather",
			Arguments: []byte(`{}`),
			State:     canon.OpaqueRef{Store: "antigravity", Key: "fc_foreign_id_signature"},
		},
		canon.FunctionOutput{ID: "fo1", CallID: "call_1", Output: []canon.Content{canon.TextContent{Text: "ok"}}},
		textMessage(canon.RoleAssistant, "prefill"),
	}
	body, err := BuildEnvelope(req, "p", "r", "-1")
	if err != nil {
		t.Fatalf("BuildEnvelope: %v", err)
	}
	got := string(body)
	if !strings.Contains(got, `"mode":"VALIDATED"`) {
		t.Fatalf("claude must force VALIDATED function calling: %s", got)
	}
	if strings.Contains(got, "fc_foreign_id_signature") {
		t.Fatalf("foreign signature must be stripped from claude envelopes: %s", got)
	}
	if !strings.Contains(got, `{"role":"user","parts":[{"text":"(continue)"}]}`) {
		t.Fatalf("model-tail claude history must get (continue) nudge: %s", got)
	}
}

func TestEnvelopeClaudeThinkingWithoutSignatureDropped(t *testing.T) {
	req := baseRequest()
	req.Model = "claude-sonnet-4-5"
	req.Instructions = nil
	req.MaxOutputTokens = 0
	req.Sampling = canon.Sampling{}
	req.Input = []canon.Item{
		canon.ReasoningItem{ID: "r1", Content: "secret thoughts"},
		textMessage(canon.RoleUser, "Hi"),
	}
	body, err := BuildEnvelope(req, "p", "r", "-1")
	if err != nil {
		t.Fatalf("BuildEnvelope: %v", err)
	}
	if strings.Contains(string(body), "secret thoughts") {
		t.Fatalf("unsigned thinking part must be dropped for claude: %s", body)
	}
}

func TestEnvelopeGeminiThinkingWithoutSignatureDropped(t *testing.T) {
	req := baseRequest()
	req.Instructions = nil
	req.MaxOutputTokens = 0
	req.Sampling = canon.Sampling{}
	req.Input = []canon.Item{
		canon.ReasoningItem{ID: "r1", Content: "hmm"},
		textMessage(canon.RoleUser, "Hi"),
	}
	body, err := BuildEnvelope(req, "p", "r", "-1")
	if err != nil {
		t.Fatalf("BuildEnvelope: %v", err)
	}
	if strings.Contains(string(body), `"thought":true`) {
		t.Fatalf("unsigned thinking part must be dropped in the no-cache path: %s", body)
	}
}
