package anthropic

import (
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
)

func TestMaxTokensDefaultsToModelOutputLimit(t *testing.T) {
	cases := []struct {
		name   string
		client int
		limit  int
		want   int
	}{
		{"absent client value takes the model limit", 0, 128000, 128000},
		{"absent client value on an unknown model takes the fallback", 0, 0, unknownModelMaxTokens},
		{"client value is kept", 4096, 128000, 4096},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := baseRequest()
			req.MaxOutputTokens = tc.client
			wr, err := New(Options{}).buildWireRequest(req, tc.limit, true)
			if err != nil {
				t.Fatal(err)
			}
			if wr.MaxTokens != tc.want {
				t.Fatalf("max_tokens = %d, want %d", wr.MaxTokens, tc.want)
			}
		})
	}
}

func TestThinkingKeepsClientMaxTokens(t *testing.T) {
	cases := []struct {
		name   string
		client int
		limit  int
		want   int
	}{
		{"client above the limit is kept", 64000, 32000, 64000},
		{"client below the thinking floor is raised", 4096, 64000, 16384 + thinkingHeadroom},
		{"client above the floor is kept", 30000, 64000, 30000},
		{"raise stops at the model limit", 4096, 20000, 20000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := baseRequest()
			req.MaxOutputTokens = tc.client
			req.Reasoning = canon.ReasoningConfig{Effort: canon.EffortHigh}
			req.Sampling.Temperature, req.Sampling.TopP = nil, nil
			wr, err := New(Options{}).buildWireRequest(req, tc.limit, true)
			if err != nil {
				t.Fatal(err)
			}
			if wr.MaxTokens != tc.want {
				t.Fatalf("max_tokens = %d, want %d", wr.MaxTokens, tc.want)
			}
		})
	}
}

func TestThinkingBudgetStaysBelowMaxTokens(t *testing.T) {
	cases := []struct {
		name       string
		effort     canon.ReasoningEffort
		limit      int
		wantMax    int
		wantBudget int
	}{
		{"limit above budget plus headroom keeps the budget", canon.EffortHigh, 64000, 64000, 16384},
		{"limit below budget plus headroom shrinks the budget", canon.EffortHigh, 16000, 16000, 16000 - thinkingHeadroom},
		{"limit equal to the budget shrinks the budget", canon.EffortXHigh, 24576, 24576, 24576 - thinkingHeadroom},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := baseRequest()
			req.MaxOutputTokens = 0
			req.Reasoning = canon.ReasoningConfig{Effort: tc.effort}
			req.Sampling.Temperature, req.Sampling.TopP = nil, nil
			wr, err := New(Options{}).buildWireRequest(req, tc.limit, true)
			if err != nil {
				t.Fatal(err)
			}
			if wr.MaxTokens != tc.wantMax || wr.Thinking.BudgetTokens != tc.wantBudget {
				t.Fatalf("max_tokens/budget = %d/%d, want %d/%d", wr.MaxTokens, wr.Thinking.BudgetTokens, tc.wantMax, tc.wantBudget)
			}
		})
	}
}

func TestThinkingRejectedWhenLimitLeavesNoBudget(t *testing.T) {
	req := baseRequest()
	req.Reasoning = canon.ReasoningConfig{Effort: canon.EffortHigh}
	req.Sampling.Temperature, req.Sampling.TopP = nil, nil
	if _, err := New(Options{}).buildWireRequest(req, 8192, true); err == nil {
		t.Fatal("accepted thinking with no room under max_tokens")
	}
}
