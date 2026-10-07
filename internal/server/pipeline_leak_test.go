package server

import (
	"errors"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
	"github.com/deLiseLINO/prism/internal/routing"
)

// brokenWriter is a client connection that is already gone: every write fails.
type brokenWriter struct{ *httptest.ResponseRecorder }

func (brokenWriter) Write([]byte) (int, error)       { return 0, errors.New("broken pipe") }
func (brokenWriter) WriteString(string) (int, error) { return 0, errors.New("broken pipe") }

func TestStreamToGoneClientLeavesNoGoroutines(t *testing.T) {
	for _, path := range []string{"/v1/responses", "/v1/messages", "/v1/chat/completions"} {
		t.Run(path, func(t *testing.T) {
			h := newTestServer(t, nil, map[canon.ModelID]routing.Plan{"p1/m": singlePlan("p1")}, func(reg *provider.Registry) {
				_ = reg.Register("p1", &fakeRunner{})
			})
			body := `{"model":"p1/m","stream":true,"max_tokens":8,"input":"hi","messages":[{"role":"user","content":"hi"}]}`
			before := runtime.NumGoroutine()
			for i := 0; i < 20; i++ {
				req := httptest.NewRequest("POST", path, strings.NewReader(body))
				req.RemoteAddr = "127.0.0.1:1"
				h.ServeHTTP(brokenWriter{httptest.NewRecorder()}, req)
			}
			deadline := time.Now().Add(2 * time.Second)
			for runtime.NumGoroutine() > before+2 && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if got := runtime.NumGoroutine(); got > before+2 {
				t.Fatalf("goroutines grew from %d to %d after 20 requests to a gone client", before, got)
			}
		})
	}
}
