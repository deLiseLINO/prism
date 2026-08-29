package responses_test

import (
	"context"
	"encoding/json"
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

func responsesCase(t *testing.T, id string) conformance.Case {
	t.Helper()
	a, err := conformance.Load(fixturePath())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range a.Cases {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("case %s not found", id)
	return conformance.Case{}
}

func TestLiveResponsesCoreSuite(t *testing.T) {
	a, err := conformance.Load(fixturePath())
	if err != nil {
		t.Fatal(err)
	}
	goldens, err := conformance.LoadGoldens(goldenDir())
	if err != nil {
		t.Fatal(err)
	}
	runner := conformance.NewRoutedRunner(chat.Builder{}, responses.Builder{})
	opts := conformance.Options{
		BaseURL: "https://api.openai.com/v1",
		APIKey:  "fixture-key",
		Goldens: goldens,
	}
	for _, c := range a.Cases {
		if c.Suite != "responses-core" {
			continue
		}
		res := runner.Run(context.Background(), c, opts)
		if !res.Passed {
			t.Fatalf("%s failed: %+v diag=%v", c.ID, res, res.Diagnostics)
		}
		if res.Classification != conformance.ClassInconclusive || res.SecondaryCode != "unclassified" {
			t.Fatalf("%s classification %s/%s", c.ID, res.Classification, res.SecondaryCode)
		}
		if len(res.AssertionResults) != 3 {
			t.Fatalf("%s assertion results %d want 3", c.ID, len(res.AssertionResults))
		}
		for _, r := range res.AssertionResults {
			if !r.Passed {
				t.Fatalf("%s assertion %s failed: %s", c.ID, r.ID, r.Reason)
			}
		}
	}
}

func TestRequestShapeStillGreen(t *testing.T) {
	c := responsesCase(t, "responses-core.protocol.request-shape")
	goldens, err := conformance.LoadGoldens(goldenDir())
	if err != nil {
		t.Fatal(err)
	}
	res := conformance.NewRoutedRunner(chat.Builder{}, responses.Builder{}).Run(context.Background(), c, conformance.Options{
		BaseURL: "https://api.openai.com/v1",
		APIKey:  "fixture-key",
		Goldens: goldens,
	})
	if !res.Passed {
		t.Fatalf("request-shape regression: %+v diag=%v", res, res.Diagnostics)
	}
	if res.Codec == nil || !res.Codec.GoldenMatched || !res.Codec.HeaderCaseMatched {
		t.Fatalf("codec check %+v", res.Codec)
	}
}

func TestResponsesBuildWire(t *testing.T) {
	temperature := 0.0
	got, err := (responses.Builder{}).Build(context.Background(), canon.Request{
		Model:  "fixture-model",
		Stream: true,
		Input: []canon.Item{
			canon.Message{Role: canon.RoleUser, Content: []canon.Content{canon.TextContent{Text: "PING"}}},
		},
		Sampling: canon.Sampling{Temperature: &temperature},
	}, conformance.BuildOptions{
		BaseURL: "https://api.openai.com/v1",
		APIKey:  "fixture-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != "POST" {
		t.Fatalf("method %s", got.Method)
	}
	if got.URL != "https://api.openai.com/v1/responses" {
		t.Fatalf("url %s", got.URL)
	}
	if len(got.Headers) != 2 || got.Headers[0].Name != "Content-Type" || got.Headers[1].Name != "Authorization" {
		t.Fatalf("headers %v", got.Headers)
	}
	want := `{"model":"fixture-model","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"PING"}]}],"stream":true,"temperature":0}`
	if string(got.Body) != want {
		t.Fatalf("body %s", got.Body)
	}
}

func TestResponsesBuildInitiatingRequest(t *testing.T) {
	c := responsesCase(t, "responses-core.protocol.sse-framing")
	if c.InitiatingRequest == nil {
		t.Fatal("sse-framing must carry an initiating request")
	}
	var vector map[string]any
	if err := json.Unmarshal([]byte(c.InitiatingRequest.Bytes), &vector); err != nil {
		t.Fatal(err)
	}
	built, err := (responses.Builder{}).Build(context.Background(), conformance.VectorToRequest(vector), conformance.BuildOptions{
		BaseURL: "https://api.openai.com/v1",
		APIKey:  "fixture-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(built.Body), "fixture-model") {
		t.Fatalf("initiating request body must carry the model: %s", built.Body)
	}
	if !strings.Contains(string(built.Body), `"stream":true`) {
		t.Fatalf("initiating request body must carry stream: %s", built.Body)
	}
}

func TestBuildRejectsUnsupportedItem(t *testing.T) {
	got, err := (responses.Builder{}).Build(context.Background(), canon.Request{
		Input: []canon.Item{canon.CompactionMarker{}},
	}, conformance.BuildOptions{})
	if err == nil {
		t.Fatal("unsupported item must return an error")
	}
	if got != nil {
		t.Fatalf("unsupported item returned request: %+v", got)
	}
}

func TestBuildRejectsUnsupportedTool(t *testing.T) {
	got, err := (responses.Builder{}).Build(context.Background(), canon.Request{
		Tools: []canon.Tool{canon.CustomToolDef{}},
	}, conformance.BuildOptions{})
	if err == nil {
		t.Fatal("unsupported tool must return an error")
	}
	if got != nil {
		t.Fatalf("unsupported tool returned request: %+v", got)
	}
}
