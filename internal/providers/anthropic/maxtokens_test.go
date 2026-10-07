package anthropic

import (
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
)

func TestThinkingKeepsClientMaxTokens(t *testing.T) {
	cases := []struct {
		name   string
		client int
		want   int
	}{
		{"client above ceiling is kept", 64000, 64000},
		{"client below the thinking floor is raised", 4096, 16384 + thinkingHeadroom},
		{"client above the floor is kept", 30000, 30000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := baseRequest()
			req.MaxOutputTokens = tc.client
			req.Reasoning = canon.ReasoningConfig{Effort: canon.EffortHigh}
			req.Sampling.Temperature, req.Sampling.TopP = nil, nil
			wr, err := New(Options{}).buildWireRequest(req, true)
			if err != nil {
				t.Fatal(err)
			}
			if wr.MaxTokens != tc.want {
				t.Fatalf("max_tokens = %d, want %d", wr.MaxTokens, tc.want)
			}
		})
	}
}
