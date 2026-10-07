package customchat_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPReplayPreservesNumericArgumentLexemes(t *testing.T) {
	const arguments = `{"id":9007199254740993,"nested":{"id":9223372036854775808123},"count":3.0,"ratio":1.234567890123456789,"power":1e300}`
	quoted, _ := json.Marshal(arguments)
	for _, tc := range []struct{ path, request string }{
		{"/v1/chat/completions", `{"model":"edge/model","messages":[{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"lookup","arguments":` + string(quoted) + `}}]},{"role":"tool","tool_call_id":"c","content":"ok"}]}`},
		{"/v1/responses", `{"model":"edge/model","input":[{"type":"function_call","call_id":"c","name":"lookup","arguments":` + string(quoted) + `},{"type":"function_call_output","call_id":"c","output":"ok"}],"tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"count":{"type":"integer"}}}}]}`},
		{"/v1/messages", `{"model":"edge/model","max_tokens":64,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"c","name":"lookup","input":` + arguments + `}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c","content":"ok"}]}]}`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			captured := make(chan string, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Messages []struct {
						Calls []struct{ Function struct{ Arguments string } } `json:"tool_calls"`
					}
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if len(body.Messages) > 0 && len(body.Messages[0].Calls) > 0 {
					captured <- body.Messages[0].Calls[0].Function.Arguments
				} else {
					captured <- ""
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
			}))
			defer upstream.Close()
			protocolPost(t, protocolProxy(t, upstream), tc.path, tc.request)
			if got := <-captured; got != arguments {
				t.Fatalf("arguments=%s want exact %s", got, arguments)
			}
		})
	}
}
