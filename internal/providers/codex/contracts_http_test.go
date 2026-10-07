package codex

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
)

func TestNativeInvalidUsageNeverPublishesSnapshot(t *testing.T) {
	for _, usage := range []string{
		`{"input_tokens":-1}`, `{"input_tokens":1.5}`, `{"input_tokens":null}`, `{"input_tokens":"2"}`,
		`{"input_tokens":9223372036854775808}`, `{"input_tokens":9223372036854775807,"output_tokens":1}`,
		`{"input_tokens_details":{"cached_tokens":-1}}`, `{"output_tokens_details":{"reasoning_tokens":null}}`,
		`{"input_tokens":2,"output_tokens":1,"total_tokens":4}`, `{"input_tokens":2,"input_tokens_details":{"cached_tokens":3}}`, `{"output_tokens":1,"output_tokens_details":{"reasoning_tokens":2}}`,
	} {
		t.Run(usage, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, joinFrames(`{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","id":"m","role":"assistant","content":[{"type":"output_text","text":"must not publish"}]}],"usage":`+usage+`}}`))
			}))
			defer up.Close()
			runner := &Runner{Creds: fixedCreds{cred: Credential{AccessToken: "fixture"}}, Client: up.Client()}
			sink := &captureSink{}
			err := runner.Run(t.Context(), provider.RunRequest{Target: provider.Target{BaseURL: up.URL}}, sink)
			var re provider.RunError
			if !errors.As(err, &re) || re.Class != provider.ClassTransport || len(sink.events) != 1 {
				t.Fatalf("error=%v events=%v", err, sink.events)
			}
			if failure, ok := sink.events[0].(canon.TurnFailed); !ok || failure.Usage != (canon.Usage{}) {
				t.Fatalf("invalid usage published: %v", sink.events)
			}
		})
	}
}
