package customchat_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPToolSubsetAndSamplingReachUpstream(t *testing.T) {
	captured := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		captured <- raw
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	protocolPost(t, protocolProxy(t, upstream), "/v1/chat/completions", `{"model":"edge/model","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"read"}},{"type":"function","function":{"name":"write"}}],"tool_choice":{"type":"allowed_tools","allowed_tools":{"mode":"required","tools":[{"type":"function","function":{"name":"read"}}]}},"reasoning_effort":"none","verbosity":"high","temperature":0.25,"top_p":0.75,"presence_penalty":0.5,"frequency_penalty":0.2,"parallel_tool_calls":false,"stop":["","END"]}`)
	var body struct {
		Tools       []struct{ Function struct{ Name string } }
		Choice      string `json:"tool_choice"`
		Effort      string `json:"reasoning_effort"`
		Verbosity   string
		Temperature float64
		TopP        float64 `json:"top_p"`
		Presence    float64 `json:"presence_penalty"`
		Frequency   float64 `json:"frequency_penalty"`
		Parallel    *bool   `json:"parallel_tool_calls"`
		Stop        []string
	}
	if err := json.Unmarshal(<-captured, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Tools) != 1 || body.Tools[0].Function.Name != "read" || body.Choice != "required" || body.Effort != "none" || body.Verbosity != "high" || body.Temperature != 0.25 || body.TopP != 0.75 || body.Presence != 0.5 || body.Frequency != 0.2 || body.Parallel == nil || *body.Parallel || len(body.Stop) != 1 || body.Stop[0] != "END" {
		t.Fatalf("controls=%+v", body)
	}
}
