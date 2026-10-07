package customchat_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/config"
	"github.com/deLiseLINO/prism/internal/provider"
	"github.com/deLiseLINO/prism/internal/providers/customchat"
	"github.com/deLiseLINO/prism/internal/routing"
	"github.com/deLiseLINO/prism/internal/server"
)

type protocolPlanner struct{ target provider.Target }

func (p protocolPlanner) Plan(model canon.ModelID) (routing.Plan, bool) {
	return routing.Plan{Targets: []provider.Target{p.target}}, model == "edge/model"
}

func protocolProxy(t *testing.T, upstream *httptest.Server) *httptest.Server {
	t.Helper()
	pool := account.New()
	pool.Register(account.Account{ID: "fixture", Provider: "edge", State: account.Active})
	registry := provider.NewRegistry()
	if err := registry.Register("edge", customchat.New(nil, customchat.Options{Client: upstream.Client()})); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Open(filepath.Join(t.TempDir(), "prism.json"))
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(server.New(server.Options{Pool: pool, Registry: registry, Config: cfg, Planner: protocolPlanner{provider.Target{Provider: "edge", Wire: provider.WireChat, BaseURL: upstream.URL}}}).Handler())
	t.Cleanup(proxy.Close)
	return proxy
}

func protocolPost(t *testing.T, proxy *httptest.Server, path, body string) []byte {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, proxy.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := proxy.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("status=%d error=%v body=%s", resp.StatusCode, err, raw)
	}
	return raw
}

func TestSchemaLexemesAtEveryHTTPIngress(t *testing.T) {
	const schema = `{"type":"object","properties":{"id":{"type":"integer","const":9007199254740993},"fraction":{"type":"number","const":1.234567890123456789},"power":{"type":"number","const":1e300}}}`
	for _, tc := range []struct{ path, request string }{
		{"/v1/chat/completions", `{"model":"edge/model","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":` + schema + `}}],"response_format":{"type":"json_schema","json_schema":{"name":"out","schema":` + schema + `}}}`},
		{"/v1/responses", `{"model":"edge/model","input":"hi","tools":[{"type":"function","name":"lookup","parameters":` + schema + `}],"text":{"format":{"type":"json_schema","name":"out","schema":` + schema + `}}}`},
		{"/v1/messages", `{"model":"edge/model","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"lookup","input_schema":` + schema + `}],"output_config":{"format":{"type":"json_schema","schema":` + schema + `}}}`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			captured := make(chan []byte, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				captured <- raw
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
			}))
			defer upstream.Close()
			protocolPost(t, protocolProxy(t, upstream), tc.path, tc.request)
			var wire struct {
				Tools []struct {
					Function struct{ Parameters json.RawMessage }
				}
				Format struct {
					Schema struct {
						Raw json.RawMessage `json:"schema"`
					} `json:"json_schema"`
				} `json:"response_format"`
			}
			if err := json.Unmarshal(<-captured, &wire); err != nil {
				t.Fatal(err)
			}
			if len(wire.Tools) != 1 {
				t.Fatalf("tools=%v", wire.Tools)
			}
			for _, raw := range []json.RawMessage{wire.Tools[0].Function.Parameters, wire.Format.Schema.Raw} {
				for _, literal := range []string{"9007199254740993", "1.234567890123456789", "1e300"} {
					if !bytes.Contains(raw, []byte(literal)) {
						t.Fatalf("numeric lexeme %s lost: %s", literal, raw)
					}
				}
			}
		})
	}
}

func TestFreeformHTTPOutputAndReplay(t *testing.T) {
	const input = "*** patch\n+日本 \\\u0061 \"quoted\"\r\n"
	wrapper, _ := json.Marshal(map[string]string{"input": input})
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", streaming), func(t *testing.T) {
			captures := make(chan []byte, 2)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				captures <- raw
				var body struct {
					Stream bool
					Tools  []struct {
						Function struct{ Parameters json.RawMessage }
					}
				}
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Error(err)
					return
				}
				if len(body.Tools) != 1 || !bytes.Contains(body.Tools[0].Function.Parameters, []byte(`"required":["input"]`)) || !bytes.Contains(body.Tools[0].Function.Parameters, []byte(`"input":{"type":"string"}`)) {
					w.WriteHeader(422)
					io.WriteString(w, `{"error":{"message":"missing input schema"}}`)
					return
				}
				call := map[string]any{"index": 0, "id": "call_patch", "type": "function", "function": map[string]any{"name": "patch", "arguments": string(wrapper)}}
				if body.Stream {
					w.Header().Set("Content-Type", "text/event-stream")
					frame, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{call}}, "finish_reason": "tool_calls"}}})
					fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", frame)
				} else {
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "tool_calls": []any{call}}, "finish_reason": "tool_calls"}}})
				}
			}))
			defer upstream.Close()
			proxy := protocolProxy(t, upstream)
			tools := `,"tools":[{"type":"custom","name":"patch","format":{"type":"text"}}]`
			raw := protocolPost(t, proxy, "/v1/responses", fmt.Sprintf(`{"model":"edge/model","input":"edit","stream":%t%s}`, streaming, tools))
			<-captures
			var response struct{ Output []json.RawMessage }
			if streaming {
				for _, line := range strings.Split(string(raw), "\n") {
					if !strings.HasPrefix(line, "data: {") {
						continue
					}
					var frame struct {
						Type     string
						Response json.RawMessage
					}
					json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame)
					if frame.Type == "response.completed" {
						json.Unmarshal(frame.Response, &response)
					}
				}
			} else if err := json.Unmarshal(raw, &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Output) != 1 {
				t.Fatalf("output=%s", raw)
			}
			var call struct {
				Type, Input string
				CallID      string `json:"call_id"`
			}
			if err := json.Unmarshal(response.Output[0], &call); err != nil || call.Type != "custom_tool_call" || call.Input != input || call.CallID != "call_patch" {
				t.Fatalf("call=%+v error=%v body=%s", call, err, raw)
			}
			replay := `{"model":"edge/model","input":[` + string(response.Output[0]) + `,{"type":"custom_tool_call_output","call_id":"call_patch","output":"applied"}]` + tools + `}`
			protocolPost(t, proxy, "/v1/responses", replay)
			var history struct {
				Messages []struct {
					Role, Content string
					Calls         []struct{ Function struct{ Arguments string } } `json:"tool_calls"`
				}
			}
			if err := json.Unmarshal(<-captures, &history); err != nil {
				t.Fatal(err)
			}
			if len(history.Messages) != 2 || len(history.Messages[0].Calls) != 1 || history.Messages[0].Calls[0].Function.Arguments != string(wrapper) || history.Messages[1].Role != "tool" || history.Messages[1].Content != "applied" {
				t.Fatalf("history=%+v", history)
			}
		})
	}
}
