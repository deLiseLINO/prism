package customchat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
)

// A 200 whose body is {"error": "..."} (string form) is a failed turn, not a
// malformed response.
func TestAggregateStringErrorFailsTurn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"error":"quota exceeded"}`)
	}))
	defer srv.Close()
	c := &collector{}
	err := New(staticKey, Options{}).Run(context.Background(), provider.RunRequest{Request: testRequest(false), Target: testTarget(srv.URL)}, c)
	if err == nil {
		t.Fatal("want error")
	}
	var failed *canon.TurnFailed
	for _, ev := range c.events {
		if f, ok := ev.(canon.TurnFailed); ok {
			failed = &f
		}
	}
	if failed == nil || failed.Failure.Message != "quota exceeded" {
		t.Fatalf("events=%v err=%v", c.events, err)
	}
}
