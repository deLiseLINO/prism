package customchat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deLiseLINO/prism/internal/provider"
)

// Some gateways send a whole tool-call argument string in one chunk.
func TestStreamAcceptsMultiMegabyteChunk(t *testing.T) {
	big := strings.Repeat("x", 3<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\""+big+"\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	c := &collector{}
	if err := New(staticKey, Options{}).Run(context.Background(), provider.RunRequest{Request: testRequest(true), Target: testTarget(srv.URL)}, c); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if c.TerminalCount() != 1 {
		t.Fatalf("terminals = %d", c.TerminalCount())
	}
}
