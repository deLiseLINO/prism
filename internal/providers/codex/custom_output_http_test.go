package codex

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
)

func TestNativeCustomOutputContentArrayHTTP(t *testing.T) {
	for _, content := range [][]canon.Content{{}, {canon.TextContent{Text: "single result"}}, {canon.TextContent{Text: "result"}, canon.ImageContent{MIMEType: "image/png", Data: []byte{1, 2}}}} {
		captured := make(chan []byte, 1)
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			captured <- raw
			io.WriteString(w, joinFrames(`{"type":"response.completed","response":{}}`))
		}))
		runner := &Runner{Creds: fixedCreds{cred: Credential{AccessToken: "fixture"}}, Client: up.Client()}
		err := runner.Run(t.Context(), provider.RunRequest{Target: provider.Target{BaseURL: up.URL}, Request: canon.Request{Input: []canon.Item{canon.CustomToolOutput{CallID: "call", Content: content}}}}, &captureSink{})
		up.Close()
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Input []struct {
				Output []json.RawMessage `json:"output"`
			} `json:"input"`
		}
		if err := json.Unmarshal(<-captured, &body); err != nil {
			t.Fatal(err)
		}
		if body.Input[0].Output == nil || len(body.Input[0].Output) != len(content) {
			t.Fatalf("output=%v", body.Input[0].Output)
		}
	}
}
