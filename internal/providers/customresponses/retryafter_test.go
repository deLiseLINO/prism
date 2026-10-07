package customresponses

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/provider"
)

func TestRateLimitCarriesRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Retry-After", "11")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"slow down"}}`))
	}))
	defer srv.Close()
	r := New(staticKey, Options{})
	err := r.Run(context.Background(), provider.RunRequest{Request: testRequest(false), Target: testTarget(srv.URL)}, &collector{})
	var runErr provider.RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("want RunError, got %v", err)
	}
	if runErr.RetryAfter != 11*time.Second {
		t.Fatalf("RetryAfter = %v, want 11s", runErr.RetryAfter)
	}
}
