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
	want := `"tools":[{"functionDeclarations":[{"name":"get_weather","description":"Get weather","parameters":{"type":"object","properties":{}}}]}],` +
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

// seedPresence installs a discovery snapshot for family-aware envelope tests
// and restores the prior snapshot on cleanup, so envelope tests stay
// independent of each other and of models_test.go's FetchModels runs.
func seedPresence(t *testing.T, raw ...string) {
	t.Helper()
	previous := presenceSnapshot()
	recordDiscovery(raw)
	t.Cleanup(func() {
		if previous == nil {
			discoverySnapshot.Lock()
			discoverySnapshot.present = nil
			discoverySnapshot.Unlock()
			return
		}
		recordDiscovery(keysOf(previous))
	})
}

func keysOf(present map[string]bool) []string {
	ids := make([]string, 0, len(present))
	for id := range present {
		ids = append(ids, id)
	}
	return ids
}

func envelopeJSON(t *testing.T, req canon.Request) string {
	t.Helper()
	body, err := BuildEnvelope(req, "p", "r", "-1")
	if err != nil {
		t.Fatalf("BuildEnvelope: %v", err)
	}
	return string(body)
}

func TestEnvelopeGoogleLevelFamilyPerEffort(t *testing.T) {
	seedPresence(t, "gemini-3.7-flash-low", "gemini-3.7-flash-medium", "gemini-3.7-flash-high", "gemini-3.7-flash-tiered")
	cases := []struct {
		effort  canon.ReasoningEffort
		model   string
		think   string
	}{
		{canon.EffortMinimal, "gemini-3.7-flash-low", `{"includeThoughts":true,"thinkingLevel":"LOW"}`},
		{canon.EffortLow, "gemini-3.7-flash-low", `{"includeThoughts":true,"thinkingLevel":"LOW"}`},
		{canon.EffortMedium, "gemini-3.7-flash-medium", `{"includeThoughts":true,"thinkingLevel":"MEDIUM"}`},
		{canon.EffortHigh, "gemini-3.7-flash-high", `{"includeThoughts":true,"thinkingLevel":"HIGH"}`},
	}
	for _, tc := range cases {
		req := baseRequest()
		req.Instructions = nil
		req.MaxOutputTokens = 0
		req.Sampling = canon.Sampling{}
		req.Reasoning = canon.ReasoningConfig{Effort: tc.effort}
		req.Input = []canon.Item{textMessage(canon.RoleUser, "Hi")}
		got := envelopeJSON(t, req)
		if want := `"model":"` + tc.model + `"`; !strings.Contains(got, want) {
			t.Fatalf("effort %d: missing %s in %s", tc.effort, want, got)
		}
		if !strings.Contains(got, `"thinkingConfig":`+tc.think) {
			t.Fatalf("effort %d: missing thinkingConfig %s in %s", tc.effort, tc.think, got)
		}
	}
}

func TestEnvelopeBudgetFamilyPerEffort(t *testing.T) {
	seedPresence(t, "gemini-3.5-flash-extra-low", "gemini-3.5-flash-low", "gemini-3-flash-agent")
	cases := []struct {
		effort canon.ReasoningEffort
		model  string
		think  string
	}{
		{canon.EffortMinimal, "gemini-3.5-flash-extra-low", `"generationConfig":{"maxOutputTokens":64000,"thinkingConfig":{"includeThoughts":true,"thinkingBudget":1000}}`},
		{canon.EffortLow, "gemini-3.5-flash-extra-low", `"generationConfig":{"maxOutputTokens":64000,"thinkingConfig":{"includeThoughts":true,"thinkingBudget":1000}}`},
		{canon.EffortMedium, "gemini-3.5-flash-low", `"generationConfig":{"maxOutputTokens":64000,"thinkingConfig":{"includeThoughts":true,"thinkingBudget":4000}}`},
		{canon.EffortHigh, "gemini-3-flash-agent", `"generationConfig":{"maxOutputTokens":64000,"thinkingConfig":{"includeThoughts":true,"thinkingBudget":10000}}`},
	}
	for _, tc := range cases {
		req := baseRequest()
		req.Model = "gemini-3.5-flash"
		req.Instructions = nil
		req.MaxOutputTokens = 0
		req.Sampling = canon.Sampling{}
		req.Reasoning = canon.ReasoningConfig{Effort: tc.effort}
		req.Input = []canon.Item{textMessage(canon.RoleUser, "Hi")}
		got := envelopeJSON(t, req)
		if want := `"model":"` + tc.model + `"`; !strings.Contains(got, want) {
			t.Fatalf("effort %d: missing %s in %s", tc.effort, want, got)
		}
		if !strings.Contains(got, tc.think) {
			t.Fatalf("effort %d: missing %s in %s", tc.effort, tc.think, got)
		}
	}
}

func TestEnvelopeBudgetAccommodatesCallerCap(t *testing.T) {
	seedPresence(t, "claude-sonnet-4-6")
	cases := []struct {
		name   string
		effort canon.ReasoningEffort
		want   string
	}{
		{"low cap 64 raises to cap plus budget", canon.EffortLow,
			`"generationConfig":{"maxOutputTokens":4160,"temperature":0,"thinkingConfig":{"includeThoughts":true,"thinkingBudget":4096}}`},
		{"minimal cap 64 raises to cap plus budget", canon.EffortMinimal,
			`"generationConfig":{"maxOutputTokens":1088,"temperature":0,"thinkingConfig":{"includeThoughts":true,"thinkingBudget":1024}}`},
		{"high cap 64 raises cap plus budget", canon.EffortHigh,
			`"generationConfig":{"maxOutputTokens":16448,"temperature":0,"thinkingConfig":{"includeThoughts":true,"thinkingBudget":16384}}`},
	}
	for _, tc := range cases {
		req := baseRequest()
		req.Model = "claude-sonnet-4-6"
		req.Instructions = nil
		req.MaxOutputTokens = 64
		temp := 0.0
		req.Sampling = canon.Sampling{Temperature: &temp}
		req.Reasoning = canon.ReasoningConfig{Effort: tc.effort}
		req.Input = []canon.Item{textMessage(canon.RoleUser, "Reply with exactly: budget-ok")}
		got := envelopeJSON(t, req)
		if want := `"model":"claude-sonnet-4-6"`; !strings.Contains(got, want) {
			t.Fatalf("%s: missing %s in %s", tc.name, want, got)
		}
		if !strings.Contains(got, tc.want) {
			t.Fatalf("%s: missing %s in %s", tc.name, tc.want, got)
		}
	}
}

func TestEnvelopeBudgetUnprofiledWireUsesUnknownCeiling(t *testing.T) {
	seedPresence(t, "gpt-oss-120b-medium")
	req := baseRequest()
	req.Model = "gpt-oss-120b"
	req.Instructions = nil
	req.MaxOutputTokens = 512
	req.Sampling = canon.Sampling{}
	req.Reasoning = canon.ReasoningConfig{Effort: canon.EffortHigh}
	req.Input = []canon.Item{textMessage(canon.RoleUser, "Hi")}
	got := envelopeJSON(t, req)
	if !strings.Contains(got, `"model":"gpt-oss-120b-medium"`) {
		t.Fatalf("unprofiled family must route to its live member: %s", got)
	}
	if !strings.Contains(got, `"generationConfig":{"maxOutputTokens":16896,"thinkingConfig":{"includeThoughts":true,"thinkingBudget":16384}}`) {
		t.Fatalf("unprofiled wire must keep caller cap plus budget under the unknown ceiling: %s", got)
	}
}

func TestAccommodateBudgetClamp(t *testing.T) {
	cases := []struct {
		name       string
		callerCap  int
		budget     int
		ceiling    int
		wantMax    int
		wantBudget int
	}{
		{"cap plus budget fits", 64, 4096, 64000, 4160, 4096},
		{"ceiling clamps the sum", 63999, 4096, 64000, 64000, 4096},
		{"no caller cap uses the unknown ceiling", 0, 1000, 64000, 64000, 1000},
		{"roomless ceiling shrinks the budget", 1200, 32768, 1225, 1225, 201},
		{"floorless ceiling zeroes the budget", 0, 32768, 400, 400, 0},
	}
	for _, tc := range cases {
		gotMax, gotBudget := accommodateBudget(tc.callerCap, tc.budget, tc.ceiling)
		if gotMax != tc.wantMax || gotBudget != tc.wantBudget {
			t.Fatalf("%s: accommodateBudget(%d, %d, %d) = (%d, %d), want (%d, %d)",
				tc.name, tc.callerCap, tc.budget, tc.ceiling, gotMax, gotBudget, tc.wantMax, tc.wantBudget)
		}
	}
}

func TestEnvelopeOffOnSuppressWhenOffFamily(t *testing.T) {
	seedPresence(t, "gemini-3.5-flash-extra-low", "gemini-3.5-flash-low", "gemini-3-flash-agent")
	req := baseRequest()
	req.Model = "gemini-3.5-flash"
	req.Instructions = nil
	req.MaxOutputTokens = 0
	req.Sampling = canon.Sampling{}
	req.Reasoning = canon.ReasoningConfig{Effort: canon.EffortOff}
	req.Input = []canon.Item{textMessage(canon.RoleUser, "Hi")}
	got := envelopeJSON(t, req)
	if !strings.Contains(got, `"model":"gemini-3.5-flash-extra-low"`) {
		t.Fatalf("off must route to the off wire id: %s", got)
	}
	if !strings.Contains(got, `"thinkingConfig":{"includeThoughts":false,"thinkingBudget":0}`) {
		t.Fatalf("off on a suppressWhenOff budget family must emit explicit suppression: %s", got)
	}
}

func TestEnvelopeOffOnSuppressWhenOffGoogleLevelFamily(t *testing.T) {
	seedPresence(t, "gemini-3-pro-low", "gemini-3-pro-high")
	req := baseRequest()
	req.Model = "gemini-3-pro"
	req.Instructions = nil
	req.MaxOutputTokens = 0
	req.Sampling = canon.Sampling{}
	req.Reasoning = canon.ReasoningConfig{Effort: canon.EffortOff}
	req.Input = []canon.Item{textMessage(canon.RoleUser, "Hi")}
	got := envelopeJSON(t, req)
	if !strings.Contains(got, `"model":"gemini-3-pro-low"`) {
		t.Fatalf("off must route to the off wire id: %s", got)
	}
	if !strings.Contains(got, `"thinkingConfig":{"includeThoughts":false,"thinkingLevel":"MINIMAL"}`) {
		t.Fatalf("off on a suppressWhenOff google-level family must emit MINIMAL suppression: %s", got)
	}
}

func TestEnvelopeUnsetEffortClampsOnRequiresEffortFamily(t *testing.T) {
	seedPresence(t, "gemini-3.7-flash-low", "gemini-3.7-flash-medium", "gemini-3.7-flash-high", "gemini-3.7-flash-tiered")
	req := baseRequest()
	req.Instructions = nil
	req.MaxOutputTokens = 0
	req.Sampling = canon.Sampling{}
	req.Input = []canon.Item{textMessage(canon.RoleUser, "Hi")}
	got := envelopeJSON(t, req)
	if !strings.Contains(got, `"model":"gemini-3.7-flash-low"`) {
		t.Fatalf("unset effort on a requiresEffort family must clamp to the lowest effort's wire id: %s", got)
	}
	if !strings.Contains(got, `"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"LOW"}`) {
		t.Fatalf("clamped minimal effort on gemini-{rev}-flash must emit LOW (minimal routes to the -low wire id): %s", got)
	}
}

func TestEnvelopeUnsetEffortOnNonRequiresEffortFamilyOmitsThinking(t *testing.T) {
	seedPresence(t, "claude-sonnet-4-6")
	req := baseRequest()
	req.Model = "claude-sonnet-4-6"
	req.Instructions = nil
	req.MaxOutputTokens = 0
	req.Sampling = canon.Sampling{}
	req.Input = []canon.Item{textMessage(canon.RoleUser, "Hi")}
	got := envelopeJSON(t, req)
	if !strings.Contains(got, `"model":"claude-sonnet-4-6"`) {
		t.Fatalf("family model with unset effort must keep the logical wire id: %s", got)
	}
	if strings.Contains(got, "thinkingConfig") {
		t.Fatalf("unset effort on a non-requiresEffort family must not invent a thinking budget: %s", got)
	}
}

func TestEnvelopeUnsetEffortOnNonFamilyModelUnchanged(t *testing.T) {
	seedPresence(t, "gemini-3.7-flash-low", "gemini-3.7-flash-medium", "gemini-3.7-flash-high", "gemini-3.7-flash-tiered")
	req := baseRequest()
	req.Model = "claude-opus-4"
	req.Instructions = nil
	req.MaxOutputTokens = 0
	req.Sampling = canon.Sampling{}
	req.Input = []canon.Item{textMessage(canon.RoleUser, "Hi")}
	got := envelopeJSON(t, req)
	if !strings.Contains(got, `"model":"claude-opus-4"`) {
		t.Fatalf("non-family model must pass through unchanged: %s", got)
	}
	if strings.Contains(got, "thinkingConfig") {
		t.Fatalf("non-family model with unset effort must not emit thinkingConfig: %s", got)
	}
	if strings.Contains(got, "generationConfig") {
		t.Fatalf("no sampling and no thinking means no generationConfig: %s", got)
	}
}
