package responses_test

import (
	"context"
	"path/filepath"
	"testing"

	"prism/internal/conformance"
	"prism/internal/conformance/codec/chat"
	"prism/internal/conformance/codec/responses"
)

func anthropicSuiteFixturePath() string {
	return filepath.Join("..", "..", "..", "..", "cmd", "prism-wirecheck", "fixtures", "protocol-v1-cases.json")
}

func TestLiveAnthropicCoreSuite(t *testing.T) {
	a, err := conformance.Load(anthropicSuiteFixturePath())
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
	ran := 0
	for _, c := range a.Cases {
		if c.Suite != "anthropic-core" {
			continue
		}
		ran++
		res := runner.Run(context.Background(), c, opts)
		if !res.Passed {
			t.Fatalf("%s failed: %+v diag=%v", c.ID, res, res.Diagnostics)
		}
		if res.Classification != conformance.ClassInconclusive || res.SecondaryCode != "unclassified" {
			t.Fatalf("%s classification %s/%s", c.ID, res.Classification, res.SecondaryCode)
		}
		for _, r := range res.AssertionResults {
			if !r.Passed {
				t.Fatalf("%s assertion %s failed: %s", c.ID, r.ID, r.Reason)
			}
		}
	}
	if ran != 4 {
		t.Fatalf("anthropic-core cases %d want 4", ran)
	}
}
