package antigravity

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
)

func TestNativeRetryHintRespectsDeadline(t *testing.T) {
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "42")
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"message":"too many requests"}}`)
	}))
	defer up.Close()
	runner, _ := NewRunner(stubCreds{pair: CredentialPair{AccessToken: "fixture"}}, up.Client(), up.URL, nil)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	var observed []provider.NetworkAttempt
	req := testRequest()
	req.AttemptObserver = func(a provider.NetworkAttempt) { observed = append(observed, a) }
	err := runner.Run(ctx, req, &recordingSink{})
	var re provider.RunError
	if !errors.As(err, &re) || re.RetryAfter != 42*time.Second || re.Class != provider.ClassRateLimited || calls != 1 || len(observed) != 1 || observed[0].Err == nil {
		t.Fatalf("error=%v calls=%d observed=%v", err, calls, observed)
	}
}

func TestNativeStructuredOutputAndInstructionHistoryHTTP(t *testing.T) {
	const schema = `{"type":"object","properties":{"n":{"minimum":9007199254740993,"multipleOf":1e-12}},"required":["n"]}`
	captured := make(chan []byte, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		captured <- raw
		io.WriteString(w, textStream())
	}))
	defer up.Close()
	runner, _ := NewRunner(stubCreds{pair: CredentialPair{AccessToken: "fixture"}}, up.Client(), up.URL, nil)
	req := testRequest()
	req.Request.Text.Format = &canon.TextFormat{Type: "json_schema", Schema: []byte(schema)}
	req.Request.Input = []canon.Item{textMessage(canon.RoleSystem, "RULE"), textMessage(canon.RoleUser, "QUESTION"), textMessage(canon.RoleDeveloper, "DEVELOPER"), textMessage(canon.RoleAssistant, "ANSWER")}
	if err := runner.Run(t.Context(), req, &recordingSink{}); err != nil {
		t.Fatal(err)
	}
	raw := <-captured
	var body struct {
		Request struct {
			Generation struct {
				Schema json.RawMessage `json:"responseJsonSchema"`
				MIME   string          `json:"responseMimeType"`
			} `json:"generationConfig"`
			System struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"systemInstruction"`
			Contents []struct {
				Role string `json:"role"`
			} `json:"contents"`
		} `json:"request"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if string(body.Request.Generation.Schema) != schema || body.Request.Generation.MIME != "application/json" || len(body.Request.Contents) != 2 || body.Request.System.Parts[0].Text != "RULE\n\nDEVELOPER" {
		t.Fatalf("request=%s", raw)
	}
}

func TestNativeMalformedCompletionFailsAfterVisibleOutput(t *testing.T) {
	payload := "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"visible\"}]}}]}}\n\n" + "data: malformed\n\n" + textStream()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) }))
	defer up.Close()
	runner, _ := NewRunner(stubCreds{pair: CredentialPair{AccessToken: "fixture"}}, up.Client(), up.URL, nil)
	sink := &recordingSink{}
	err := runner.Run(t.Context(), testRequest(), sink)
	var re provider.RunError
	if !errors.As(err, &re) || re.Kind != provider.TerminalEmitted || !re.Accepted {
		t.Fatalf("error=%v", err)
	}
	visible := false
	for _, ev := range sink.events {
		if delta, ok := ev.(canon.TextDelta); ok && strings.Contains(delta.Text, "visible") {
			visible = true
		}
	}
	if !visible || countTerminals(sink.events) != 1 {
		t.Fatalf("events=%v", sink.events)
	}
	if _, ok := lastTerminal(sink.events).(canon.TurnFailed); !ok {
		t.Fatalf("terminal=%v", lastTerminal(sink.events))
	}
}
