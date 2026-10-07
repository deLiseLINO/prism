package customresponses

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
)

func TestOutOfOrderOutputIndicesRetainAssignedOrder(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, frame := range []string{
			`{"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"second","role":"assistant","content":[]}}`,
			`{"type":"response.output_text.delta","output_index":1,"item_id":"second","delta":"second"}`,
			`{"type":"response.output_item.done","output_index":1,"item":{"type":"message","id":"second","role":"assistant","content":[{"type":"output_text","text":"second"}]}}`,
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"first","role":"assistant","content":[]}}`,
			`{"type":"response.output_text.delta","output_index":0,"item_id":"first","delta":"first"}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"first","role":"assistant","content":[{"type":"output_text","text":"first"}]}}`,
			`{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","id":"first","role":"assistant","content":[{"type":"output_text","text":"first"}]},{"type":"message","id":"second","role":"assistant","content":[{"type":"output_text","text":"second"}]}]}}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", frame)
		}
	}))
	defer upstream.Close()
	sink := &collector{}
	if err := New(nil, Options{}).Run(t.Context(), provider.RunRequest{Target: provider.Target{Provider: "edge", Wire: provider.WireResponses, BaseURL: upstream.URL}, Request: canon.Request{Model: "m", Stream: true}}, sink); err != nil {
		t.Fatal(err)
	}
	var started []canon.ItemID
	for _, event := range sink.All() {
		if event, ok := event.(canon.ItemStarted); ok {
			started = append(started, event.Item.(canon.Message).ID)
		}
	}
	if len(started) != 2 || started[0] != "first" || started[1] != "second" {
		t.Fatalf("introductions=%v", started)
	}
}
