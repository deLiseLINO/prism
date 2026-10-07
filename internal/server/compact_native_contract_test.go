package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/requestlog"
)

func TestNativeCompactionPreservesOpaqueOutputAndAccountsUsage(t *testing.T) {
	const output = `{"type":"compaction","id":"synthetic-compact","encrypted_content":"opaque-fixture"}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses/compact" {
			t.Errorf("path=%s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"completed","output":[%s],"usage":{"input_tokens":7,"output_tokens":2,"total_tokens":9}}`, output)
	}))
	defer upstream.Close()
	s, journal, store := contractServer(t, upstream.URL)
	response := postJSON(t, s.Handler(), "/v1/responses/compact", `{"model":"native-route","input":"hello"}`)
	var result struct {
		Output []json.RawMessage `json:"output"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || len(result.Output) != 1 || string(result.Output[0]) != output {
		t.Fatalf("response=%d %s", response.Code, response.Body)
	}
	entries := journal.Snapshot()
	if len(entries) != 1 || len(entries[0].Attempts) != 1 || entries[0].RequestID == "" || entries[0].Terminal.Usage.TotalTokens != 9 {
		t.Fatalf("journal=%+v", entries)
	}
	stored, err := store.Overview(context.Background(), time.Time{})
	if err != nil || stored.Completed != 1 || stored.TotalTokens != 9 {
		t.Fatalf("usage=%+v err=%v", stored, err)
	}
}

func TestNativeCompactThrottlePreservesStatusAndRetryAfter(t *testing.T) {
	for _, delay := range []string{"8", "7.5"} {
		t.Run(delay, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", delay)
				w.WriteHeader(http.StatusTooManyRequests)
				fmt.Fprint(w, `{"error":{"code":"rate_limit_exceeded","message":"synthetic throttle"}}`)
			}))
			defer upstream.Close()
			s, journal, _ := contractServer(t, upstream.URL)
			request := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(`{"model":"native-route","input":"compact"}`))
			request.RemoteAddr = "127.0.0.1:1234"
			response := &firstStatusWriter{ResponseRecorder: httptest.NewRecorder()}
			s.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusTooManyRequests || response.first != http.StatusTooManyRequests || response.Header().Get("Retry-After") != "8" || !strings.Contains(response.Body.String(), "synthetic throttle") {
				t.Fatalf("first=%d final=%d retry=%q body=%s", response.first, response.Code, response.Header().Get("Retry-After"), response.Body)
			}
			entries := journal.Snapshot()
			if len(entries) != 1 || entries[0].Status != requestlog.StatusFailed || len(entries[0].Attempts) != 1 {
				t.Fatalf("journal=%+v", entries)
			}
		})
	}
}