package codex

import (
	"encoding/json"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
)

// The ChatGPT backend takes a replayed reasoning item as summary plus its
// encrypted payload; the plain-text channel is output of open-weight models and
// is refused on input. The ingress copies the summary into Content when the
// client sent no content, so the builder must not forward Content.
func TestReplayedReasoningCarriesNoPlainTextChannel(t *testing.T) {
	req := canon.Request{
		Model: "gpt-5.2-codex",
		Input: []canon.Item{canon.ReasoningItem{
			ID:      "rs_1",
			Summary: []canon.TextContent{{Text: "thought"}},
			Content: "thought",
			State:   canon.OpaqueRef{Store: canon.StoreWire, Key: "blob"},
		}},
	}
	result, err := BuildRequestBody(req)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(result.Body, &got); err != nil || len(got.Input) != 1 {
		t.Fatalf("body %s err %v", result.Body, err)
	}
	if _, has := got.Input[0]["content"]; has {
		t.Fatalf("reasoning replay must not carry content: %s", result.Body)
	}
}
