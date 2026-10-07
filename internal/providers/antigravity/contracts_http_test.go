package antigravity

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
)

func TestNativeUnsupportedToolConstraintsRejectBeforeHTTP(t *testing.T) {
	for _, scenario := range []string{"parallel", "unknown named", "alternate forced"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; io.WriteString(w, textStream()) }))
			defer up.Close()
			runner, _ := NewRunner(stubCreds{pair: CredentialPair{AccessToken: "fixture"}}, up.Client(), up.URL)
			req := testRequest()
			req.Request.Tools = []canon.Tool{canon.FunctionTool{Name: "read"}}
			switch scenario {
			case "parallel":
				off := false
				req.Request.Sampling.ParallelToolCalls = &off
			case "unknown named":
				req.Request.ToolChoice = canon.ToolNamed{Name: "missing"}
			case "alternate forced":
				req.Request.Model = "claude-edge"
				req.Request.ToolChoice = canon.ToolRequired{}
			}
			err := runner.Run(t.Context(), req, &recordingSink{})
			var re provider.RunError
			if !errors.As(err, &re) || re.Class != provider.ClassInvalidRequest || calls != 0 {
				t.Fatalf("error=%v calls=%d", err, calls)
			}
		})
	}
}
