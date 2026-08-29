package chat_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"prism/internal/canon"
	"prism/internal/conformance"
	"prism/internal/conformance/codec/chat"
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
		res := r.Run(context.Background(), c, conformance.Options{})
		if res.Passed {
			continue
		}
		if res.Classification != conformance.ClassHarnessFailure {
			t.Fatalf("%s classification %s want harness_failure", c.ID, res.Classification)
		}
		upstream := "openai-chat"
		if len(c.Requirements.UpstreamProtocols) > 0 {
			upstream = c.Requirements.UpstreamProtocols[0]
		}
		if c.Fixture.Role == conformance.RoleAdapterVector && upstream == "openai-chat" {
			if res.SecondaryCode != "contract_integrity" {
				t.Fatalf("%s: live path without golden must be contract_integrity, got %s", c.ID, res.SecondaryCode)
			}
			if len(res.Diagnostics) == 0 || !strings.Contains(res.Diagnostics[0], "golden") {
				t.Fatalf("%s: diagnostic must name the missing golden: %v", c.ID, res.Diagnostics)
			}
			continue
		}
		if res.SecondaryCode != "execution_error" {
			t.Fatalf("%s classification %s/%s", c.ID, res.Classification, res.SecondaryCode)
		}
		if len(res.Diagnostics) == 0 {
			t.Fatalf("%s diagnostic must be non-empty: %v", c.ID, res.Diagnostics)
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
