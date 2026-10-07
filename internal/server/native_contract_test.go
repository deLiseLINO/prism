package server

import (
	"context"
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
	"github.com/deLiseLINO/prism/internal/providers/codex"
	"github.com/deLiseLINO/prism/internal/requestlog"
	"github.com/deLiseLINO/prism/internal/routing"
	"github.com/deLiseLINO/prism/internal/usage"
)

type contractCredentials struct{}

func (contractCredentials) Credential(context.Context, account.Lease) (codex.Credential, error) {
	return codex.Credential{AccessToken: "synthetic-key", Generation: 1}, nil
}

func contractServer(t *testing.T, upstream string) (*Server, *requestlog.Journal, usage.Store) {
	t.Helper()
	pool := account.New()
	pool.Register(account.Account{ID: "synthetic-account", Provider: "native", State: account.Active, CredGen: 1, Version: 1})
	registry := provider.NewRegistry()
	if err := registry.Register("native", &codex.Runner{Creds: contractCredentials{}}); err != nil {
		t.Fatal(err)
	}
	journal := requestlog.New(8, time.Now)
	store, err := usage.Open(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	plan := &fakePlanner{plans: map[canon.ModelID]routing.Plan{"native-route": {Targets: []provider.Target{{Provider: "native", Model: "model", Wire: provider.WireCodex, BaseURL: upstream}}}}}
	cfg, err := config.Open(filepath.Join(t.TempDir(), "prism.json"))
	if err != nil {
		t.Fatal(err)
	}
	return New(Options{Planner: plan, Registry: registry, Pool: pool, Config: cfg, Usage: store, RequestLog: journal}), journal, store
}

func TestBufferedNativeErrorStatusPrecedesBodyAcrossIngress(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"rate_limit_exceeded\",\"message\":\"synthetic refusal\"},\"usage\":{\"input_tokens\":7,\"output_tokens\":2,\"total_tokens\":9}}}\n\n")
	}))
	defer upstream.Close()
	for _, path := range []string{"/v1/responses", "/v1/chat/completions", "/v1/messages"} {
		t.Run(path, func(t *testing.T) {
			s, journal, store := contractServer(t, upstream.URL)
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"native-route","input":"hello","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`))
			request.RemoteAddr = "127.0.0.1:1234"
			w := &firstStatusWriter{ResponseRecorder: httptest.NewRecorder()}
			s.Handler().ServeHTTP(w, request)
			if w.Code != 429 || w.first != 429 || !strings.Contains(w.Body.String(), "synthetic refusal") {
				t.Fatalf("first=%d final=%d body=%s", w.first, w.Code, w.Body)
			}
			entries := journal.Snapshot()
			if len(entries) != 1 || entries[0].Terminal.Usage.TotalTokens != 9 || entries[0].Status != requestlog.StatusFailed {
				t.Fatalf("journal=%+v", entries)
			}
			stored, err := store.Overview(context.Background(), time.Time{})
			if err != nil || stored.Failed != 1 || stored.TotalTokens != 9 {
				t.Fatalf("usage=%+v err=%v", stored, err)
			}
		})
	}
}

type firstStatusWriter struct {
	*httptest.ResponseRecorder
	first int
}

func (w *firstStatusWriter) Write(b []byte) (int, error) {
	if w.first == 0 {
		w.first = w.Code
	}
	return w.ResponseRecorder.Write(b)
}

func (w *firstStatusWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }

func TestNativeCompletionPayloadContracts(t *testing.T) {
	for _, tc := range []struct {
		name, prefix, output, want string
		failed                     bool
	}{
		{"refusal", "", `{"type":"message","id":"m","role":"assistant","content":[{"type":"refusal","refusal":"cannot help"}]}`, "cannot help", false},
		{"empty custom input", "", `{"type":"custom_tool_call","id":"c","call_id":"call","name":"write","input":""}`, "", false},
		{"missing custom input", "", `{"type":"custom_tool_call","id":"c","call_id":"call","name":"write"}`, "", true},
		{"null custom input", "", `{"type":"custom_tool_call","id":"c","call_id":"call","name":"write","input":null}`, "", true},
		{"conflicting text", "visible", `{"type":"message","id":"m","role":"assistant","content":[{"type":"output_text","text":"different"}]}`, "visible", true},
	} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, streaming), func(t *testing.T) {
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					if tc.prefix != "" {
						fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"m\",\"role\":\"assistant\",\"content\":[]}}\n\n")
						fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"m\",\"delta\":%q}\n\n", tc.prefix)
					}
					fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[%s]}}\n\n", tc.output)
				}))
				defer up.Close()
				s, _, _ := contractServer(t, up.URL)
				rec := postJSON(t, s.Handler(), "/v1/responses", fmt.Sprintf(`{"model":"native-route","input":"hello","stream":%t}`, streaming))
				var response struct {
					Status string
					Output []json.RawMessage
				}
				if streaming {
					terminals := 0
					for _, line := range strings.Split(rec.Body.String(), "\n") {
						if !strings.HasPrefix(line, "data: {") {
							continue
						}
						var event struct {
							Type     string
							Response json.RawMessage
						}
						if err := json.Unmarshal([]byte(line[6:]), &event); err != nil {
							t.Fatal(err)
						}
						if event.Type == "response.completed" || event.Type == "response.failed" {
							terminals++
							if err := json.Unmarshal(event.Response, &response); err != nil {
								t.Fatal(err)
							}
						}
					}
					if terminals != 1 {
						t.Fatalf("terminal count=%d body=%s", terminals, rec.Body)
					}
				} else if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				wantStatus := "completed"
				if tc.failed {
					wantStatus = "failed"
				}
				if response.Status != wantStatus || !streaming && tc.failed && rec.Code < 400 {
					t.Fatalf("http=%d response=%s", rec.Code, rec.Body)
				}
				if tc.want != "" && !strings.Contains(rec.Body.String(), tc.want) {
					t.Fatalf("payload lost %q: %s", tc.want, rec.Body)
				}
				if tc.name == "empty custom input" {
					var item map[string]json.RawMessage
					if len(response.Output) != 1 || json.Unmarshal(response.Output[0], &item) != nil || string(item["input"]) != `""` {
						t.Fatalf("empty input lost: %s", rec.Body)
					}
				}
			})
		}
	}
}

func TestNativeEmptyCompletionOverHTTP(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	defer upstream.Close()
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", streaming), func(t *testing.T) {
			s, _, _ := contractServer(t, upstream.URL)
			proxy := httptest.NewServer(s.Handler())
			defer proxy.Close()
			response, err := proxy.Client().Post(proxy.URL+"/v1/responses", "application/json", strings.NewReader(fmt.Sprintf(`{"model":"native-route","input":"hello","stream":%t}`, streaming)))
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || response.StatusCode != http.StatusOK {
				t.Fatalf("status=%d body=%s err=%v", response.StatusCode, body, err)
			}
			var completed struct {
				Status string
				Output []json.RawMessage
			}
			if streaming {
				terminals := 0
				for _, line := range strings.Split(string(body), "\n") {
					if !strings.HasPrefix(line, "data: ") {
						continue
					}
					if strings.TrimSpace(line[6:]) == "[DONE]" {
						continue
					}
					var event struct {
						Type string
						Response json.RawMessage
					}
					if err := json.Unmarshal([]byte(line[6:]), &event); err != nil {
						t.Fatal(err)
					}
					if event.Type == "response.failed" || event.Type == "response.incomplete" || event.Type == "response.completed" {
						terminals++
						if event.Type != "response.completed" {
							t.Fatalf("unexpected terminal: %s", line)
						}
						if err := json.Unmarshal(event.Response, &completed); err != nil {
							t.Fatal(err)
						}
					}
				}
				if terminals != 1 {
					t.Fatalf("terminal count=%d body=%s", terminals, body)
				}
			} else if err := json.Unmarshal(body, &completed); err != nil {
				t.Fatal(err)
			}
			if completed.Status != "completed" || completed.Output == nil || len(completed.Output) != 0 {
				t.Fatalf("empty completion lost: %s", body)
			}
		})
	}
}
