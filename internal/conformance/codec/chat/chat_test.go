package chat_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"prism/internal/canon"
	"prism/internal/conformance"
	"prism/internal/conformance/codec/chat"
	"prism/internal/conformance/codec/responses"
)

func fixturePath() string {
	return filepath.Join("..", "..", "..", "..", "cmd", "prism-wirecheck", "fixtures", "protocol-v1-cases.json")
}

func goldenDir() string {
	return filepath.Join("..", "..", "..", "..", "cmd", "prism-wirecheck", "golden")
}

func requestShapeCase(t *testing.T) conformance.Case {
	t.Helper()
	a, err := conformance.Load(fixturePath())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range a.Cases {
		if c.ID == "responses-core.protocol.request-shape" {
			return c
		}
	}
	t.Fatal("request-shape case not found")
	return conformance.Case{}
}

func TestLiveRequestShape(t *testing.T) {
	c := requestShapeCase(t)
	goldens, err := conformance.LoadGoldens(goldenDir())
	if err != nil {
		t.Fatal(err)
	}
	want, ok := goldens[c.ID]
	if !ok {
		t.Fatal("golden missing for request-shape")
	}
	if want.Body != `{"model":"fixture-model","messages":[{"role":"user","content":"PING"}],"stream":false,"temperature":0}` {
		t.Fatalf("golden body does not pin the reference bytes: %s", want.Body)
	}
	if len(want.Body) != 102 {
		t.Fatalf("golden body %d bytes want 102", len(want.Body))
	}
	res := conformance.NewRunner(chat.Builder{}).Run(context.Background(), c, conformance.Options{
		BaseURL: "https://api.openai.com/v1",
		APIKey:  "fixture-key",
		Goldens: goldens,
	})
	if !res.Passed {
		t.Fatalf("case failed: %+v diag=%v", res, res.Diagnostics)
	}
	if res.Classification != conformance.ClassInconclusive || res.SecondaryCode != "unclassified" {
		t.Fatalf("classification %s/%s", res.Classification, res.SecondaryCode)
	}
	if res.Codec == nil || !res.Codec.GoldenMatched || !res.Codec.HeaderCaseMatched {
		t.Fatalf("codec check %+v", res.Codec)
	}
	if res.Codec.BodyBytes != 102 {
		t.Fatalf("body %d bytes want 102", res.Codec.BodyBytes)
	}
	if res.Codec.URL != "https://api.openai.com/v1/chat/completions" {
		t.Fatalf("url %s", res.Codec.URL)
	}
	for _, r := range res.AssertionResults {
		if !r.Passed {
			t.Fatalf("assertion %s failed: %s", r.ID, r.Reason)
		}
	}
	if len(res.AssertionResults) != 3 {
		t.Fatalf("assertion results %d want 3", len(res.AssertionResults))
	}
}

func TestRunnerNonRunnableCasesFailLoudly(t *testing.T) {
	a, err := conformance.Load(fixturePath())
	if err != nil {
		t.Fatal(err)
	}
	r := conformance.NewRunner(chat.Builder{})
	for _, c := range a.Cases {
		if c.Suite == "anthropic-core" {
			continue
		}
		res := r.Run(context.Background(), c, conformance.Options{})
		if res.Passed {
			continue
		}
		if res.Classification == conformance.ClassProtocolFailure {
			if res.SecondaryCode != "deterministic_assertion" {
				t.Fatalf("%s protocol failure code %s", c.ID, res.SecondaryCode)
			}
			continue
		}
		upstream := "openai-chat"
		if len(c.Requirements.UpstreamProtocols) > 0 {
			upstream = c.Requirements.UpstreamProtocols[0]
		}
		if (c.Fixture.Role == conformance.RoleAdapterVector || c.Fixture.Role == conformance.RoleClientRequest) && upstream == "openai-chat" && res.SecondaryCode == "contract_integrity" {
			found := false
			for _, d := range res.Diagnostics {
				if strings.Contains(d, "golden") {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s: diagnostic must name the missing golden: %v", c.ID, res.Diagnostics)
			}
			continue
		}
		if res.Classification != conformance.ClassHarnessFailure {
			t.Fatalf("%s classification %s want harness_failure", c.ID, res.Classification)
		}
		if res.SecondaryCode != "execution_error" {
			t.Fatalf("%s classification %s/%s", c.ID, res.Classification, res.SecondaryCode)
		}
		if len(res.Diagnostics) == 0 {
			t.Fatalf("%s diagnostic must be non-empty: %v", c.ID, res.Diagnostics)
		}
	}
}

func TestLiveChatCoreSuite(t *testing.T) {
	a, err := conformance.Load(fixturePath())
	if err != nil {
		t.Fatal(err)
	}
	r := conformance.NewRoutedRunner(chat.Builder{}, responses.Builder{})
	ids := []string{
		"chat-core.protocol.request-mapping",
		"chat-core.protocol.nonstream-envelope",
		"chat-core.protocol.stream-assembly",
		"chat-core.protocol.stream-terminal",
	}
	for _, id := range ids {
		found := false
		for _, c := range a.Cases {
			if c.ID != id {
				continue
			}
			found = true
			res := r.Run(context.Background(), c, conformance.Options{})
			if !res.Passed {
				t.Fatalf("%s failed: %+v diag=%v", id, res, res.Diagnostics)
			}
			if len(res.AssertionResults) == 0 {
				t.Fatalf("%s: no assertions evaluated", id)
			}
		}
		if !found {
			t.Fatalf("%s not in fixture authority", id)
		}
	}
}

func TestRunnerDigestIntegrity(t *testing.T) {
	c := requestShapeCase(t)
	c.Fixture.Digest = "tampered"
	res := conformance.NewRunner(chat.Builder{}).Run(context.Background(), c, conformance.Options{})
	if res.Passed || res.Classification != conformance.ClassHarnessFailure || res.SecondaryCode != "contract_integrity" {
		t.Fatalf("tampered digest: %+v", res)
	}
}

func TestBuildRejectsUnsupportedItem(t *testing.T) {
	got, err := (chat.Builder{}).Build(context.Background(), canon.Request{
		Input: []canon.Item{canon.ReasoningItem{}},
	}, conformance.BuildOptions{})
	if err == nil {
		t.Fatal("unsupported item must return an error")
	}
	if got != nil {
		t.Fatalf("unsupported item returned request: %+v", got)
	}
}

func TestBuildRejectsUnsupportedTool(t *testing.T) {
	got, err := (chat.Builder{}).Build(context.Background(), canon.Request{
		Tools: []canon.Tool{canon.CustomToolDef{}},
	}, conformance.BuildOptions{})
	if err == nil {
		t.Fatal("unsupported tool must return an error")
	}
	if got != nil {
		t.Fatalf("unsupported tool returned request: %+v", got)
	}
}

func TestBuildChatCoreMapping(t *testing.T) {
	got, err := (chat.Builder{}).Build(context.Background(), canon.Request{
		Model:        "fixture-model",
		Instructions: []canon.Content{canon.TextContent{Text: "SYS"}},
		Input: []canon.Item{
			canon.Message{Role: canon.RoleDeveloper, Content: []canon.Content{canon.TextContent{Text: "DEV"}}},
			canon.Message{Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "PING"}}},
		},
		Text: canon.TextOutput{Format: &canon.TextFormat{Type: "json_object"}},
	}, conformance.BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"model":"fixture-model","messages":[{"role":"system","content":"SYS"},{"role":"developer","content":"DEV"},{"role":"user","content":"PING"}],"stream":false,"response_format":{"type":"json_object"}}`
	if string(got.Body) != want {
		t.Fatalf("body\n got %s\nwant %s", got.Body, want)
	}
}

func TestBuildStreamOptions(t *testing.T) {
	got, err := (chat.Builder{}).Build(context.Background(), canon.Request{
		Model:  "fixture-model",
		Stream: true,
		Input:  []canon.Item{canon.Message{Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "PING"}}}},
	}, conformance.BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"model":"fixture-model","messages":[{"role":"user","content":"PING"}],"stream":true,"stream_options":{"include_usage":true}}`
	if string(got.Body) != want {
		t.Fatalf("body\n got %s\nwant %s", got.Body, want)
	}
}

func TestBuildJoinsMultiPartTextContent(t *testing.T) {
	got, err := (chat.Builder{}).Build(context.Background(), canon.Request{
		Model: "fixture-model",
		Input: []canon.Item{canon.Message{
			Role:    canon.RoleUser,
			Content: []canon.Content{canon.TextContent{Text: "AB"}, canon.TextContent{Text: "CD"}},
		}},
	}, conformance.BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"model":"fixture-model","messages":[{"role":"user","content":"ABCD"}],"stream":false}`
	if string(got.Body) != want {
		t.Fatalf("body\n got %s\nwant %s", got.Body, want)
	}
}
