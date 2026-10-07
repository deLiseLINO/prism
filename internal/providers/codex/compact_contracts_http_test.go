package codex

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
)

func TestNativeCompactionOpaqueReplayAndStatus(t *testing.T) {
	for _, kind := range []string{"compaction", "compaction_summary", "context_compaction"} {
		output := `{"type":"` + kind + `","id":"cp","encrypted_content":"opaque/原文==","extra":{"n":9007199254740993}}`
		for _, status := range []string{"completed", "incomplete", "failed", "in_progress", ""} {
			t.Run(kind+"/"+status, func(t *testing.T) {
				var bodies []string
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					raw, _ := io.ReadAll(r.Body)
					bodies = append(bodies, string(raw))
					io.WriteString(w, `{"status":"`+status+`","output":[`+output+`],"usage":{"input_tokens":3,"output_tokens":1}}`)
				}))
				defer up.Close()
				runner := &Runner{Creds: fixedCreds{cred: Credential{AccessToken: "fixture"}}, Client: up.Client()}
				result, err := runner.Compact(t.Context(), provider.CompactRequest{Target: provider.Target{BaseURL: up.URL}, Input: []canon.Item{canon.CompactionMarker{ID: "old", Type: kind, State: canon.OpaqueRef{Store: canon.StoreWire, Key: "prior opaque"}}}})
				if status != "completed" {
					if err == nil {
						t.Fatal("incomplete compaction accepted")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if len(result.Output) != 1 || string(result.Output[0]) != output {
					t.Fatalf("opaque output = %s", result.Output)
				}
				if len(bodies) != 1 || !strings.Contains(bodies[0], `"type":"`+kind+`"`) || !strings.Contains(bodies[0], `"encrypted_content":"prior opaque"`) || !strings.Contains(bodies[0], `"id":"old"`) {
					t.Fatalf("replay = %v", bodies)
				}
			})
		}
	}
}