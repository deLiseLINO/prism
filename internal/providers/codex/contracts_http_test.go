package codex

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
)

func TestNativeTerminalClosesBeforeHTTPBodyEOF(t *testing.T) {
	for _, terminal := range []string{
		`{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","id":"m","role":"assistant","content":[{"type":"output_text","text":"answer"}]}],"usage":{"input_tokens":9007199254740993,"output_tokens":1}}}`,
		`{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"failed turn"},"usage":{"input_tokens":3,"output_tokens":1}}}`,
	} {
		t.Run(terminal, func(t *testing.T) {
			release := make(chan struct{})
			closed := make(chan struct{})
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, joinFrames(terminal))
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
					close(closed)
				case <-release:
				}
			}))
			defer up.Close()
			defer close(release)
			runner := &Runner{Creds: fixedCreds{cred: Credential{AccessToken: "fixture"}}, Client: up.Client()}
			sink := &captureSink{}
			done := make(chan error, 1)
			go func() {
				done <- runner.Run(t.Context(), provider.RunRequest{Target: provider.Target{BaseURL: up.URL}}, sink)
			}()
			select {
			case err := <-done:
				if strings.Contains(terminal, "response.failed") {
					var re provider.RunError
					if !errors.As(err, &re) || re.Kind != provider.TerminalEmitted {
						t.Fatalf("failure = %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("terminal still waits for HTTP EOF")
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("body not closed")
			}
			last := sink.events[len(sink.events)-1]
			if finished, ok := last.(canon.TurnFinished); ok && finished.Usage.InputTokens != 9007199254740993 {
				t.Fatalf("usage = %v", finished.Usage)
			}
		})
	}
}

func TestNativeInvalidUsageNeverPublishesSnapshot(t *testing.T) {
	for _, usage := range []string{
		`{"input_tokens":-1}`, `{"input_tokens":1.5}`, `{"input_tokens":null}`, `{"input_tokens":"2"}`,
		`{"input_tokens":9223372036854775808}`, `{"input_tokens":9223372036854775807,"output_tokens":1}`,
		`{"input_tokens_details":{"cached_tokens":-1}}`, `{"output_tokens_details":{"reasoning_tokens":null}}`,
		`{"input_tokens":2,"output_tokens":1,"total_tokens":4}`, `{"input_tokens":2,"input_tokens_details":{"cached_tokens":3}}`, `{"output_tokens":1,"output_tokens_details":{"reasoning_tokens":2}}`,
	} {
		t.Run(usage, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, joinFrames(`{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","id":"m","role":"assistant","content":[{"type":"output_text","text":"must not publish"}]}],"usage":`+usage+`}}`))
			}))
			defer up.Close()
			runner := &Runner{Creds: fixedCreds{cred: Credential{AccessToken: "fixture"}}, Client: up.Client()}
			sink := &captureSink{}
			err := runner.Run(t.Context(), provider.RunRequest{Target: provider.Target{BaseURL: up.URL}}, sink)
			var re provider.RunError
			if !errors.As(err, &re) || re.Class != provider.ClassTransport || len(sink.events) != 1 {
				t.Fatalf("error=%v events=%v", err, sink.events)
			}
			if failure, ok := sink.events[0].(canon.TurnFailed); !ok || failure.Usage != (canon.Usage{}) {
				t.Fatalf("invalid usage published: %v", sink.events)
			}
		})
	}
}

func TestNativeCompactionOpaqueReplayAndStatus(t *testing.T) {
	for _, kind := range []string{"compaction", "compaction_summary", "context_compaction"} {
		output := `{"type":"` + kind + `","id":"cp","encrypted_content":"opaque/原文==","extra":{"n":9007199254740993}}`
		for _, status := range []string{"completed", "incomplete", "failed", "in_progress", ""} {
			t.Run(kind+"/"+status, func(t *testing.T) {
				var bodies []string
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					raw, _ := io.ReadAll(r.Body)
					bodies = append(bodies, string(raw))
					io.WriteString(w, `{"status":"`+status+`","output":[`+output+`],"usage":{"input_tokens":3,"output_tokens":1}}`)
				}))
				defer up.Close()
				runner := &Runner{Creds: fixedCreds{cred: Credential{AccessToken: "fixture"}}, Client: up.Client()}
				result, err := runner.Compact(t.Context(), provider.CompactRequest{Target: provider.Target{BaseURL: up.URL}, Input: []canon.Item{canon.CompactionMarker{ID: "old", Type: kind, State: canon.OpaqueRef{Store: canon.StoreWire, Key: "prior opaque"}}}})
				if status != "completed" {
					if err == nil {
						t.Fatal("incomplete compaction accepted")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if len(result.Output) != 1 || string(result.Output[0]) != output {
					t.Fatalf("opaque output = %s", result.Output)
				}
				if len(bodies) != 1 || !strings.Contains(bodies[0], `"type":"`+kind+`"`) || !strings.Contains(bodies[0], `"encrypted_content":"prior opaque"`) || !strings.Contains(bodies[0], `"id":"old"`) {
					t.Fatalf("replay = %v", bodies)
				}
			})
		}
	}
}

func TestNativeOAuthAndReasoningRepairShareRefreshBudget(t *testing.T) {
	var auths []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		auths = append(auths, r.Header.Get("Authorization"))
		if len(auths) == 1 || !strings.Contains(string(body), `"type":"reasoning"`) {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(400)
		io.WriteString(w, `{"error":{"code":"invalid_encrypted_content","message":"cannot verify reasoning"}}`)
	}))
	defer up.Close()
	creds := &rotatingCreds{current: "old", next: "new"}
	runner := &Runner{Creds: creds, Client: up.Client()}
	var attempts []provider.NetworkAttempt
	var generations []account.CredentialGeneration
	err := runner.Run(t.Context(), provider.RunRequest{Target: provider.Target{BaseURL: up.URL}, Request: canon.Request{Input: []canon.Item{canon.ReasoningItem{State: canon.OpaqueRef{Store: canon.StoreWire, Key: "opaque"}}}}, AttemptObserver: func(a provider.NetworkAttempt) { attempts = append(attempts, a) }, CredentialObserver: func(g account.CredentialGeneration) { generations = append(generations, g) }}, &captureSink{})
	var re provider.RunError
	if !errors.As(err, &re) || re.Class != provider.ClassUnauthorized {
		t.Fatalf("error = %v", err)
	}
	if !reflect.DeepEqual(auths, []string{"Bearer old", "Bearer new", "Bearer new"}) || len(creds.rejected) != 1 || len(attempts) != 3 || len(generations) != 3 {
		t.Fatalf("auth=%v refresh=%v attempts=%v generations=%v", auths, creds.rejected, attempts, generations)
	}
}
