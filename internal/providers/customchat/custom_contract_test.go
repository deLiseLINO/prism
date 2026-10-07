package customchat

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
)

func TestMalformedCustomArgumentsNeverBecomeToolReceipt(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, args := range []string{`{}`, `"raw"`, `{"input":null}`, `{"input":1}`, `{"input":"partial`, `{"input":"ok","extra":true}`} {
			t.Run(fmt.Sprintf("stream=%t/%s", streaming, args), func(t *testing.T) {
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					quoted := fmt.Sprintf("%q", args)
					if streaming {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c\",\"function\":{\"name\":\"patch\",\"arguments\":%s}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", quoted)
					} else {
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprintf(w, "{\"choices\":[{\"message\":{\"tool_calls\":[{\"id\":\"c\",\"function\":{\"name\":\"patch\",\"arguments\":%s}}]},\"finish_reason\":\"tool_calls\"}]}", quoted)
					}
				}))
				defer upstream.Close()
				sink := &collector{}
				err := New(nil, Options{}).Run(t.Context(), provider.RunRequest{Target: provider.Target{Provider: "edge", Wire: provider.WireChat, BaseURL: upstream.URL}, Request: canon.Request{Model: "m", Stream: streaming, Tools: []canon.Tool{canon.CustomToolDef{Name: "patch"}}}}, sink)
				if err == nil {
					t.Fatal("accepted malformed custom arguments")
				}
				for _, event := range sink.All() {
					switch event.(type) {
					case canon.CustomToolInputDelta, canon.ToolArgumentsDelta, canon.ItemFinished, canon.TurnFinished:
						t.Fatalf("malformed arguments published %T", event)
					}
				}
			})
		}
	}
}
