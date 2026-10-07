package customchat_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatUsageRejectsInvalidSnapshotsAtHTTPBoundary(t *testing.T) {
	for _, usage := range []string{
		`{"prompt_tokens":7,"completion_tokens":2,"total_tokens":3}`,
		`{"prompt_tokens":9223372036854775807,"completion_tokens":1}`,
		`{"prompt_tokens":null,"completion_tokens":1}`,
		`{"prompt_tokens":"bad","completion_tokens":1}`,
	} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/%s", streaming, usage), func(t *testing.T) {
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if streaming {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"must not publish\"},\"finish_reason\":\"stop\"}],\"usage\":%s}\n\ndata: [DONE]\n\n", usage)
					} else {
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprintf(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"must not publish"},"finish_reason":"stop"}],"usage":%s}`, usage)
					}
				}))
				defer up.Close()
				proxy := protocolProxy(t, up)
				resp, err := proxy.Client().Post(proxy.URL+"/v1/responses", "application/json", strings.NewReader(fmt.Sprintf(`{"model":"edge/model","input":"hi","stream":%t}`, streaming)))
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				raw, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatal(err)
				}
				body := string(raw)
				if strings.Contains(body, `"status":"completed"`) || strings.Contains(body, "must not publish") || !strings.Contains(body, "failed") || !streaming && resp.StatusCode < 400 {
					t.Fatalf("invalid usage published http=%d body=%s", resp.StatusCode, body)
				}
			})
		}
	}
}
