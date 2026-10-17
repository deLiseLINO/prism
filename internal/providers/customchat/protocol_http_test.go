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

func TestOutputLimitAtEveryHTTPEgress(t *testing.T) {
	const text = "partial answer"
	const reasoning = "partial reasoning"
	const usage = `{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Stream bool }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if request.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, frame := range []string{
				`{"choices":[{"index":0,"delta":{"role":"assistant","content":"partial answer","reasoning_content":"partial reasoning"}}]}`,
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_lookup","type":"function","function":{"name":"lookup","arguments":"{\"city\":"}}]}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":` + usage + `}`,
			} {
				fmt.Fprintf(w, "data: %s\n\n", frame)
			}
			io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"partial answer","reasoning_content":"partial reasoning","tool_calls":[{"id":"call_lookup","type":"function","function":{"name":"lookup","arguments":"{\"city\":"}}]},"finish_reason":"length"}],"usage":`+usage+`}`)
	}))
	defer upstream.Close()
	proxy := protocolProxy(t, upstream)

	type wireUsage struct {
		Input  int `json:"input_tokens"`
		Output int `json:"output_tokens"`
		Total  int `json:"total_tokens"`
		Prompt int `json:"prompt_tokens"`
		Tokens int `json:"completion_tokens"`
	}
	type contentBlock struct {
		Type     string
		Text     string
		Thinking string
		Content  []struct{ Type, Text string }
	}
	decode := func(t *testing.T, raw []byte, value any) {
		t.Helper()
		if err := json.Unmarshal(raw, value); err != nil {
			t.Fatalf("invalid JSON: %v body=%s", err, raw)
		}
	}
	for _, tc := range []struct{ path, request string }{
		{"/v1/chat/completions", `{"model":"edge/model","messages":[{"role":"user","content":"hi"}],"stream":%t,"stream_options":{"include_usage":true},"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`},
		{"/v1/responses", `{"model":"edge/model","input":"hi","stream":%t,"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`},
		{"/v1/messages", `{"model":"edge/model","max_tokens":2,"messages":[{"role":"user","content":"hi"}],"stream":%t,"tools":[{"name":"lookup","input_schema":{"type":"object"}}]}`},
	} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.path, streaming), func(t *testing.T) {
				raw := protocolPost(t, proxy, tc.path, fmt.Sprintf(tc.request, streaming))
				frames := []json.RawMessage{raw}
				if streaming {
					frames = protocolDataFrames(t, raw)
				}
				var gotText, gotReasoning, terminal string
				var gotUsage wireUsage
				switch tc.path {
				case "/v1/chat/completions":
					for _, raw := range frames {
						var frame struct {
							Error   json.RawMessage
							Usage   *wireUsage
							Choices []struct {
								Finish string `json:"finish_reason"`
								Delta  struct {
									Content   string
									Reasoning string `json:"reasoning_content"`
								}
								Message struct {
									Content   string
									Reasoning string            `json:"reasoning_content"`
									Tools     []json.RawMessage `json:"tool_calls"`
								}
							}
						}
						decode(t, raw, &frame)
						if len(frame.Error) != 0 {
							t.Fatalf("chat failure: %s", raw)
						}
						if frame.Usage != nil {
							gotUsage = *frame.Usage
						}
						for _, choice := range frame.Choices {
							if choice.Finish != "" {
								if terminal != "" || choice.Finish != "length" {
									t.Fatalf("unexpected chat terminal: %s", raw)
								}
								terminal = choice.Finish
							}
							if len(choice.Message.Tools) != 0 {
								t.Fatalf("unfinished tools in chat snapshot: %s", raw)
							}
							gotText += choice.Delta.Content + choice.Message.Content
							gotReasoning += choice.Delta.Reasoning + choice.Message.Reasoning
						}
					}
					gotUsage.Input, gotUsage.Output = gotUsage.Prompt, gotUsage.Tokens
				case "/v1/responses":
					var snapshot struct {
						Status  string
						Error   json.RawMessage
						Details struct{ Reason string } `json:"incomplete_details"`
						Output  []contentBlock
						Usage   wireUsage
					}
					for _, raw := range frames {
						if !streaming {
							decode(t, raw, &snapshot)
							continue
						}
						var frame struct {
							Type     string
							Response json.RawMessage
							Item     contentBlock
						}
						decode(t, raw, &frame)
						switch frame.Type {
						case "error", "response.failed", "response.completed", "response.function_call_arguments.done", "response.custom_tool_call_input.done":
							t.Fatalf("unexpected Responses terminal: %s", raw)
						case "response.output_item.done":
							if frame.Item.Type == "function_call" || frame.Item.Type == "custom_tool_call" {
								t.Fatalf("unfinished tool published as done: %s", raw)
							}
						case "response.incomplete":
							if terminal != "" {
								t.Fatalf("duplicate Responses terminal: %s", raw)
							}
							terminal = frame.Type
							decode(t, frame.Response, &snapshot)
						}
					}
					if snapshot.Status != "incomplete" || snapshot.Details.Reason != "max_output_tokens" || len(snapshot.Error) != 0 {
						t.Fatalf("Responses snapshot=%+v body=%s", snapshot, raw)
					}
					terminal = snapshot.Status
					gotUsage = snapshot.Usage
					for _, item := range snapshot.Output {
						if item.Type != "message" && item.Type != "reasoning" {
							t.Fatalf("unfinished tool in Responses snapshot: %+v", item)
						}
						for _, part := range item.Content {
							if item.Type == "message" {
								gotText += part.Text
							} else {
								gotReasoning += part.Text
							}
						}
					}
				case "/v1/messages":
					open := make(map[int]bool)
					for _, raw := range frames {
						var frame struct {
							Type    string
							Index   int
							Error   json.RawMessage
							Content []contentBlock
							Block   contentBlock `json:"content_block"`
							Stop    string       `json:"stop_reason"`
							Usage   wireUsage
							Delta   struct {
								Type, Text, Thinking string
								Stop                 string `json:"stop_reason"`
							}
						}
						decode(t, raw, &frame)
						if frame.Type == "error" || len(frame.Error) != 0 {
							t.Fatalf("Messages failure: %s", raw)
						}
						if !streaming {
							terminal, gotUsage = frame.Stop, frame.Usage
							for _, block := range frame.Content {
								if block.Type != "text" && block.Type != "thinking" {
									t.Fatalf("unfinished tool in Messages snapshot: %+v", block)
								}
								gotText += block.Text
								gotReasoning += block.Thinking
							}
							continue
						}
						switch frame.Type {
						case "content_block_start":
							if open[frame.Index] {
								t.Fatalf("duplicate block start: %s", raw)
							}
							open[frame.Index] = true
							gotText += frame.Block.Text
							gotReasoning += frame.Block.Thinking
						case "content_block_delta":
							if !open[frame.Index] {
								t.Fatalf("delta outside open block: %s", raw)
							}
							gotText += frame.Delta.Text
							gotReasoning += frame.Delta.Thinking
						case "content_block_stop":
							if !open[frame.Index] {
								t.Fatalf("stop outside open block: %s", raw)
							}
							delete(open, frame.Index)
						case "message_delta":
							if len(open) != 0 || terminal != "" || frame.Delta.Stop != "max_tokens" {
								t.Fatalf("invalid Messages terminal: %s open=%v", raw, open)
							}
							terminal, gotUsage = frame.Delta.Stop, frame.Usage
						case "message_stop":
							if terminal != "max_tokens" || len(open) != 0 {
								t.Fatalf("message stopped before closed blocks and max_tokens: %s", raw)
							}
							terminal = "message_stop"
						}
					}
					if streaming && terminal != "message_stop" || !streaming && terminal != "max_tokens" {
						t.Fatalf("missing Messages terminal: %s", raw)
					}
					gotUsage.Total = gotUsage.Input + gotUsage.Output
				}
				if terminal == "" || gotText != text || gotReasoning != reasoning || gotUsage.Input != 7 || gotUsage.Output != 2 || gotUsage.Total != 9 {
					t.Fatalf("terminal=%q text=%q reasoning=%q usage=%+v body=%s", terminal, gotText, gotReasoning, gotUsage, raw)
				}
			})
		}
	}
}

func protocolDataFrames(t *testing.T, raw []byte) []json.RawMessage {
	t.Helper()
	var frames []json.RawMessage
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			continue
		}
		if !json.Valid([]byte(data)) {
			t.Fatalf("invalid SSE data: %s", line)
		}
		frames = append(frames, json.RawMessage(data))
	}
	if len(frames) == 0 {
		t.Fatalf("missing SSE data: %s", raw)
	}
	return frames
}

func TestMixedReasoningPrecedesTextAtHTTPEgress(t *testing.T) {
	for _, field := range []string{"reasoning_content", "reasoning", "reasoning_text"} {
		t.Run(field, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{%q:\"Considering the request\"}}]}\n\n", field)
				fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"D\",%q:\". Ready.\"}}]}\n\n", field)
				fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"one.\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer upstream.Close()
			raw := protocolPost(t, protocolProxy(t, upstream), "/v1/chat/completions", `{"model":"edge/model","messages":[{"role":"user","content":"Confirm completion."}],"stream":true}`)
			var text, reasoning strings.Builder
			textStarted := false
			for _, data := range protocolDataFrames(t, raw) {
				var frame struct {
					Choices []struct {
						Delta struct {
							Content   string `json:"content"`
							Reasoning string `json:"reasoning_content"`
						} `json:"delta"`
					} `json:"choices"`
				}
				if err := json.Unmarshal(data, &frame); err != nil {
					t.Fatal(err)
				}
				for _, choice := range frame.Choices {
					if choice.Delta.Reasoning != "" {
						if textStarted {
							t.Fatalf("reasoning resumed after answer text: %s", raw)
						}
						reasoning.WriteString(choice.Delta.Reasoning)
					}
					if choice.Delta.Content != "" {
						textStarted = true
						text.WriteString(choice.Delta.Content)
					}
				}
			}
			if text.String() != "Done." || reasoning.String() != "Considering the request. Ready." {
				t.Fatalf("text=%q reasoning=%q", text.String(), reasoning.String())
			}
		})
	}
}
