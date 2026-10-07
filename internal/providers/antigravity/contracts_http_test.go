package antigravity

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/provider"
)

func TestNativeTerminalAndFrameBoundariesHTTP(t *testing.T) {
	for _, payload := range []string{
		"data: {\"response\":\ndata: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"answer\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":2,\"candidatesTokenCount\":1}}}\n\n",
		"data: {\"error\":{\"message\":\"failed turn\"}}\n\n",
	} {
		t.Run(payload, func(t *testing.T) {
			release, closed := make(chan struct{}), make(chan struct{})
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, payload)
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
					close(closed)
				case <-release:
				}
			}))
			defer up.Close()
			defer close(release)
			runner, err := NewRunner(stubCreds{pair: CredentialPair{AccessToken: "fixture"}}, up.Client(), up.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			sink := &recordingSink{}
			done := make(chan error, 1)
			go func() { done <- runner.Run(t.Context(), testRequest(), sink) }()
			select {
			case err := <-done:
				if strings.Contains(payload, `"error"`) {
					var re provider.RunError
					if !errors.As(err, &re) || re.Kind != provider.TerminalEmitted {
						t.Fatalf("error=%v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("terminal waits for EOF")
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("body not closed")
			}
			if countTerminals(sink.events) != 1 {
				t.Fatalf("events=%v", sink.events)
			}
		})
	}
}

func TestNativeInvalidUsageKeepsPriorSnapshotHTTP(t *testing.T) {
	for _, invalid := range []string{`-1`, `1.5`, `"2"`, `null`, `9223372036854775808`} {
		t.Run(invalid, func(t *testing.T) {
			payload := "data: {\"response\":{\"usageMetadata\":{\"promptTokenCount\":2,\"candidatesTokenCount\":3,\"cachedContentTokenCount\":1}}}\n\n" + "data: {\"response\":{\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":" + invalid + "}}}\n\n"
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) }))
			defer up.Close()
			runner, _ := NewRunner(stubCreds{pair: CredentialPair{AccessToken: "fixture"}}, up.Client(), up.URL, nil)
			sink := &recordingSink{}
			err := runner.Run(t.Context(), testRequest(), sink)
			var re provider.RunError
			failed, ok := lastTerminal(sink.events).(canon.TurnFailed)
			if !errors.As(err, &re) || re.Class != provider.ClassTransport || !ok || failed.Usage != (canon.Usage{InputTokens: 2, OutputTokens: 3, CachedInputTokens: 1, TotalTokens: 5}) {
				t.Fatalf("error=%v events=%v", err, sink.events)
			}
		})
	}
}

func TestNativeExactSchemaAndCatalogIsolationHTTP(t *testing.T) {
	const schema = `{"type":"object","properties":{"count":{"type":"integer","minimum":9007199254740993,"enum":[9007199254740993,1e20]}},"additionalProperties":false}`
	captured := make(chan []byte, 4)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		captured <- raw
		io.WriteString(w, textStream())
	}))
	defer up.Close()
	for _, model := range []canon.ModelID{"gemini-3.7-flash", "claude-edge"} {
		t.Run(string(model), func(t *testing.T) {
			p := config.Provider{ModelMode: config.ModelModeLogical, ModelCatalogs: []config.ModelCatalog{{BaseURL: EndpointKey(up.URL), Account: "other-account", Project: "p", RawModels: []string{"gemini-3.7-flash-high"}}, {BaseURL: EndpointKey(up.URL), Account: "acc-1", Project: "p", RawModels: []string{"gemini-3.7-flash-low"}}}}
			runner, _ := NewRunner(stubCreds{pair: CredentialPair{AccessToken: "fixture", ProjectID: "p"}}, up.Client(), up.URL, func() config.Provider { return p })
			req := testRequest()
			req.Request.Model = model
			req.Request.Reasoning.Effort = canon.EffortHigh
			req.Request.Tools = []canon.Tool{canon.FunctionTool{Name: "read", Parameters: []byte(schema)}}
			err := runner.Run(t.Context(), req, &recordingSink{})
			if model == "claude-edge" {
				var re provider.RunError
				if !errors.As(err, &re) || re.Class != provider.ClassInvalidRequest || len(captured) != 0 {
					t.Fatalf("lossy schema dispatched: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			raw := <-captured
			var body struct {
				Model   string `json:"model"`
				Request struct {
					Tools []struct {
						Declarations []struct {
							Schema json.RawMessage `json:"parametersJsonSchema"`
						} `json:"functionDeclarations"`
					} `json:"tools"`
				} `json:"request"`
			}
			if json.Unmarshal(raw, &body) != nil || body.Model != "gemini-3.7-flash-low" || string(body.Request.Tools[0].Declarations[0].Schema) != schema {
				t.Fatalf("request=%s", raw)
			}
		})
	}
}

func TestNativeUnsupportedToolConstraintsRejectBeforeHTTP(t *testing.T) {
	for _, scenario := range []string{"parallel", "unknown named", "alternate forced"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; io.WriteString(w, textStream()) }))
			defer up.Close()
			runner, _ := NewRunner(stubCreds{pair: CredentialPair{AccessToken: "fixture"}}, up.Client(), up.URL, nil)
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
