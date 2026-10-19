package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/provider"
	"github.com/deLiseLINO/prism/internal/providers/anthropic"
	"github.com/deLiseLINO/prism/internal/routing"
)

func TestMessagesBetweenToolsHTTP(t *testing.T) {
	for _, tc := range []struct {
		name, effort, format, history string
	}{
		{name: "without effort"},
		{name: "low effort", effort: "low"},
		{name: "xhigh effort", effort: "xhigh"},
		{name: "max effort", effort: "max"},
		{name: "effort", effort: "medium"},
		{name: "schema and tool replay", effort: "high", format: `,"format":{"type":"json_schema","schema":{"type":"object","properties":{"result":{"type":"string"}},"required":["result"],"additionalProperties":false}}`, history: `,{"role":"assistant","content":[{"type":"thinking","thinking":"Check the lookup result.","signature":"synthetic-signature"},{"type":"tool_use","id":"call_lookup","name":"lookup","input":{"query":"test"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_lookup","content":"lookup-result"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			captured := make(chan map[string]json.RawMessage, 1)
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				captured <- body
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_result\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"mock-model\",\"content\":[],\"usage\":{\"input_tokens\":4,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"DONE\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			}))
			defer up.Close()
			pool := account.New()
			pool.Register(account.Account{ID: "mock-account", Provider: "mock", State: account.Active, CredGen: 1, Version: 1})
			registry := provider.NewRegistry()
			if err := registry.Register("mock", anthropic.New(anthropic.Options{HTTP: up.Client()})); err != nil {
				t.Fatal(err)
			}
			planner := &fakePlanner{plans: map[canon.ModelID]routing.Plan{"mock-route": {Targets: []provider.Target{{Provider: "mock", Model: "mock-model", Wire: provider.WireMessages, BaseURL: up.URL, APIKeyRef: "mock-key"}}}}}
			cfg, err := config.Open(filepath.Join(t.TempDir(), "prism.json"))
			if err != nil {
				t.Fatal(err)
			}
			s := New(Options{Planner: planner, Registry: registry, Pool: pool, Config: cfg})
			outputConfig := ""
			if tc.effort != "" {
				outputConfig = `,"output_config":{"effort":"` + tc.effort + `"` + tc.format + `}`
			}
			raw := fmt.Sprintf(`{"model":"mock-route","max_tokens":512,"messages":[{"role":"user","content":"Use lookup to check the result."}%s],"tools":[{"name":"lookup","input_schema":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}],"tool_choice":{"type":"auto"},"thinking":{"type":"between_tools"}%s}`, tc.history, outputConfig)
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(raw))
			req.RemoteAddr = "127.0.0.1:1234"
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"text":"DONE"`) {
				t.Fatalf("HTTP %d: %s", rec.Code, rec.Body)
			}
			var body map[string]json.RawMessage
			select {
			case body = <-captured:
			case <-time.After(time.Second):
				t.Fatal("request did not reach the upstream")
			}
			if string(body["thinking"]) != `{"type":"between_tools"}` || string(body["max_tokens"]) != "512" {
				t.Fatalf("thinking=%s max_tokens=%s", body["thinking"], body["max_tokens"])
			}
			if tc.effort == "" {
				if _, ok := body["output_config"]; ok {
					t.Fatalf("unspecified output_config added: %s", body["output_config"])
				}
			} else {
				var output map[string]json.RawMessage
				if err := json.Unmarshal(body["output_config"], &output); err != nil {
					t.Fatal(err)
				}
				if string(output["effort"]) != fmt.Sprintf("%q", tc.effort) {
					t.Fatalf("effort=%s", output["effort"])
				}
				if tc.format == "" {
					if _, ok := output["format"]; ok {
						t.Fatalf("empty output format added: %s", output["format"])
					}
				} else if !strings.Contains(string(output["format"]), `"required":["result"]`) {
					t.Fatalf("schema lost: %s", output["format"])
				}
			}
			if !strings.Contains(string(body["tools"]), `"name":"lookup"`) {
				t.Fatalf("tools lost: %s", body["tools"])
			}
			if tc.history != "" {
				for _, want := range []string{`"type":"thinking"`, `"signature":"synthetic-signature"`, `"tool_use_id":"call_lookup"`, "lookup-result"} {
					if !strings.Contains(string(body["messages"]), want) {
						t.Fatalf("replay missing %s: %s", want, body["messages"])
					}
				}
			}
		})
	}
}
