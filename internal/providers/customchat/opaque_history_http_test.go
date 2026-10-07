package customchat_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOpaqueCompactionRefusesBeforeChatDispatch(t *testing.T) {
	for _, kind := range []string{"compaction", "compaction_summary", "context_compaction"} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", kind, streaming), func(t *testing.T) {
				var calls atomic.Int64
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}]}`)
				}))
				defer upstream.Close()
				proxy := protocolProxy(t, upstream)
				response, err := proxy.Client().Post(proxy.URL+"/v1/responses", "application/json", strings.NewReader(fmt.Sprintf(`{"model":"edge/model","input":[{"type":"message","role":"user","content":"keep"},{"type":%q,"id":"cmp","encrypted_content":"opaque-state"}],"stream":%t}`, kind, streaming)))
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				raw, err := io.ReadAll(response.Body)
				if err != nil {
					t.Fatal(err)
				}
				if calls.Load() != 0 || !strings.Contains(string(raw), "opaque compaction cannot be represented") || strings.Contains(string(raw), `"status":"completed"`) || !streaming && response.StatusCode != http.StatusBadRequest {
					t.Fatalf("calls=%d http=%d body=%s", calls.Load(), response.StatusCode, raw)
				}
				if !streaming {
					var result struct {
						Status string `json:"status"`
					}
					if json.Unmarshal(raw, &result) != nil || result.Status != "failed" {
						t.Fatalf("buffered response=%s", raw)
					}
				} else if !strings.Contains(string(raw), "event: response.failed") {
					t.Fatalf("stream response=%s", raw)
				}
			})
		}
	}
}
