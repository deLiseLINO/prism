package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/provider"
	"github.com/deLiseLINO/prism/internal/providers/customchat"
	"github.com/deLiseLINO/prism/internal/providers/customresponses"
	"github.com/deLiseLINO/prism/internal/routing"
)

func TestProviderAuthRefusalSurvivesBlockedRetry(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid credential","type":"authentication_error","details":{"request":"mock"}}}`))
	}))
	defer upstream.Close()
	runner := customchat.New(func(context.Context, provider.Target, account.Lease) (string, error) { return "mock", nil }, customchat.Options{Client: upstream.Client()})
	plan := routing.Plan{Targets: []provider.Target{{Provider: "p1", Model: "mock", Wire: provider.WireChat, BaseURL: upstream.URL}}}
	h := newTestServer(t, nil, map[canon.ModelID]routing.Plan{"edge/mock": plan}, func(reg *provider.Registry) {
		if err := reg.Register("p1", runner); err != nil {
			t.Fatal(err)
		}
	})
	for i := range 2 {
		rec := postJSON(t, h, "/v1/chat/completions", `{"model":"edge/mock","messages":[{"role":"user","content":"hi"}]}`)
		var body struct {
			Error struct {
				Message string            `json:"message"`
				Details map[string]string `json:"details"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusUnauthorized || body.Error.Message != "Invalid credential" || body.Error.Details["request"] != "mock" {
			t.Fatalf("request %d: HTTP %d %s", i+1, rec.Code, rec.Body.String())
		}
	}
	if calls != 1 {
		t.Fatalf("blocked retry made %d upstream calls, want 1", calls)
	}
	pool := h.pool.(interface {
		PublishCredential(account.AccountID, account.CredentialGeneration, bool) error
	})
	if err := pool.PublishCredential("a1", 2, true); err != nil {
		t.Fatal(err)
	}
	rec := postJSON(t, h, "/v1/chat/completions", `{"model":"edge/mock","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusUnauthorized || calls != 2 {
		t.Fatalf("replacement did not reach upstream: HTTP %d, calls %d", rec.Code, calls)
	}
}

func TestProviderFailureSurvivesUnavailableFallback(t *testing.T) {
	for _, status := range []int{402, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			runner := &fakeRunner{scripts: []fakeScript{{err: provider.RunError{Kind: provider.Retryable, Class: provider.ClassServer, ReplaySafe: true, RetryAfter: 3 * time.Second, Cause: errors.New("upstream refused"), Reported: &canon.ProviderError{Status: status, Error: []byte(`{"message":"upstream refused","details":{"request":"mock"}}`)}}}}}
			plan := routing.Plan{Targets: []provider.Target{{Provider: "p1", Model: "m1"}, {Provider: "p2", Model: "m1"}}}
			h := newTestServer(t, nil, map[canon.ModelID]routing.Plan{"edge/mock": plan}, func(reg *provider.Registry) {
				if err := reg.Register("p1", runner); err != nil {
					t.Fatal(err)
				}
				if err := reg.Register("p2", &fakeRunner{}); err != nil {
					t.Fatal(err)
				}
			})
			if err := h.pool.DeleteAccount(context.Background(), "a2"); err != nil {
				t.Fatal(err)
			}
			rec := postJSON(t, h, "/v1/chat/completions", `{"model":"edge/mock","messages":[{"role":"user","content":"hi"}]}`)
			if rec.Code != status || !strings.Contains(rec.Body.String(), `"request":"mock"`) || rec.Header().Get("Retry-After") != "3" {
				t.Fatalf("lost upstream failure: HTTP %d %s headers=%v", rec.Code, rec.Body.String(), rec.Header())
			}
		})
	}
}

func TestAccountUnavailableIsNotQuotaFailure(t *testing.T) {
	for _, state := range []string{"missing", "paused", "unselected"} {
		t.Run(state, func(t *testing.T) {
			runner := &fakeRunner{}
			h := newTestServer(t, nil, map[canon.ModelID]routing.Plan{"edge/mock": singlePlan("p1")}, func(reg *provider.Registry) {
				if err := reg.Register("p1", runner); err != nil {
					t.Fatal(err)
				}
			})
			switch state {
			case "missing":
				if err := h.pool.DeleteAccount(context.Background(), "a1"); err != nil {
					t.Fatal(err)
				}
			case "paused":
				if err := h.pool.Pause(context.Background(), "a1", 1); err != nil {
					t.Fatal(err)
				}
			case "unselected":
				h.pool.(interface{ Register(account.Account) }).Register(activeAccount("other", "p1"))
			}
			rec := postJSON(t, h, "/v1/chat/completions", `{"model":"edge/mock","messages":[{"role":"user","content":"hi"}]}`)
			if rec.Code != 503 || strings.Contains(rec.Body.String(), "quota") || !strings.Contains(rec.Body.String(), `"code":"account_unavailable"`) || runner.callsMade() != 0 {
				t.Fatalf("wrong admission failure: HTTP %d %s calls=%d", rec.Code, rec.Body.String(), runner.callsMade())
			}
		})
	}
}

func TestProviderHTTPFailureKeepsStatusAndDetails(t *testing.T) {
	for _, wire := range []struct{ name, path, request string }{
		{"chat", "/v1/chat/completions", `{"model":"edge/mock","messages":[{"role":"user","content":"hi"}]}`},
		{"responses", "/v1/responses", `{"model":"edge/mock","input":"hi"}`},
		{"messages", "/v1/messages", `{"model":"edge/mock","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`},
	} {
		for _, failure := range []struct {
			name, body, want string
			status           int
		}{
			{"json", `{"error":{"message":"busy","details":{"request":"mock"}}}`, `"request":"mock"`, 529},
			{"text", "gateway unavailable", "gateway unavailable", 503},
			{"details", `{"response":{"status_details":{"error":{"message":"busy","details":{"request":"mock"}}}}}`, `"request":"mock"`, 402},
			{"large integer", `{"error":{"message":"busy","request":9007199254740993}}`, `9007199254740993`, 418},
			{"array", `["denied",{"request":7}]`, `"request":7`, 418},
			{"null", `null`, `null`, 418},
		} {
			t.Run(wire.name+"/"+failure.name, func(t *testing.T) {
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(failure.status)
					_, _ = w.Write([]byte(failure.body))
				}))
				defer upstream.Close()
				runner := customchat.New(func(context.Context, provider.Target, account.Lease) (string, error) { return "mock", nil }, customchat.Options{Client: upstream.Client()})
				plan := routing.Plan{Targets: []provider.Target{{Provider: "p1", Model: "mock", Wire: provider.WireChat, BaseURL: upstream.URL}}}
				h := newTestServer(t, nil, map[canon.ModelID]routing.Plan{"edge/mock": plan}, func(reg *provider.Registry) {
					if err := reg.Register("p1", runner); err != nil {
						t.Fatal(err)
					}
				})
				rec := postJSON(t, h, wire.path, wire.request)
				if rec.Code != failure.status || !strings.Contains(rec.Body.String(), failure.want) {
					t.Fatalf("lost upstream response: HTTP %d %s", rec.Code, rec.Body.String())
				}
			})
		}
	}
}

func TestLimitFailureOnlyOverridesUnavailableOrAuthFallback(t *testing.T) {
	for _, next := range []struct {
		status int
		class  provider.ErrorClass
		want   int
	}{{401, provider.ClassUnauthorized, 429}, {401, provider.ClassTransport, 429}, {503, provider.ClassServer, 503}, {400, provider.ClassInvalidRequest, 400}} {
		t.Run(http.StatusText(next.status), func(t *testing.T) {
			primary := &fakeRunner{scripts: []fakeScript{{err: provider.RunError{Kind: provider.Retryable, Class: provider.ClassRateLimited, ReplaySafe: true, Reported: &canon.ProviderError{Status: 429, Error: []byte(`{"message":"primary limit"}`)}}}}}
			backup := &fakeRunner{scripts: []fakeScript{{err: provider.RunError{Kind: provider.Retryable, Class: next.class, ReplaySafe: true, Reported: &canon.ProviderError{Status: next.status, Error: []byte(`{"message":"backup refusal"}`)}}}}}
			plan := routing.Plan{Targets: []provider.Target{{Provider: "p1", Model: "m1"}, {Provider: "p2", Model: "m1"}}}
			h := newTestServer(t, nil, map[canon.ModelID]routing.Plan{"edge/mock": plan}, func(reg *provider.Registry) {
				if err := reg.Register("p1", primary); err != nil {
					t.Fatal(err)
				}
				if err := reg.Register("p2", backup); err != nil {
					t.Fatal(err)
				}
			})
			rec := postJSON(t, h, "/v1/chat/completions", `{"model":"edge/mock","messages":[{"role":"user","content":"hi"}]}`)
			if rec.Code != next.want {
				t.Fatalf("wrong fallback precedence: HTTP %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestChatProviderFailureHasReadableMessage(t *testing.T) {
	for _, tc := range []struct {
		name, raw, message string
		statusDetails      bool
	}{
		{"overload", `{"code":"server_overloaded","request":9007199254740993,"param":null}`, "Server is overloaded. Please try again later.", false},
		{"unknown code", `{"code":"edge_unavailable","type":"server_error"}`, "Provider error: edge_unavailable", false},
		{"empty message", `{"code":"server_overloaded","message":""}`, "Server is overloaded. Please try again later.", false},
		{"blank message", `{"code":"server_overloaded","message":" \t\n"}`, "Server is overloaded. Please try again later.", false},
		{"null message", `{"code":"server_overloaded","message":null}`, "Server is overloaded. Please try again later.", false},
		{"provider message", `{"code":"server_overloaded","message":"  Provider says wait.  ","request":9007199254740993}`, "  Provider says wait.  ", false},
		{"case-distinct extra field", `{"code":"server_overloaded","message":"  Provider says wait.  ","MESSAGE":""}`, "  Provider says wait.  ", false},
		{"status details", `{"code":"server_overloaded","details":{"request":"mock"}}`, "Server is overloaded. Please try again later.", true},
	} {
		for _, stream := range []bool{true, false} {
			t.Run(tc.name+"/stream="+strconv.FormatBool(stream), func(t *testing.T) {
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					response := `{"status":"failed","error":` + tc.raw + `}`
					if tc.statusDetails {
						response = `{"status":"failed","status_details":{"error":` + tc.raw + `}}`
					}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = w.Write([]byte("event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":" + response + "}\n\n"))
					} else {
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(response))
					}
				}))
				defer upstream.Close()
				runner := customresponses.New(func(context.Context, provider.Target, account.Lease) (string, error) { return "mock", nil }, customresponses.Options{})
				plan := routing.Plan{Targets: []provider.Target{{Provider: "p1", Model: "mock", Wire: provider.WireResponses, BaseURL: upstream.URL}}}
				h := newTestServer(t, nil, map[canon.ModelID]routing.Plan{"edge/mock": plan}, func(reg *provider.Registry) {
					if err := reg.Register("p1", runner); err != nil {
						t.Fatal(err)
					}
				})
				rec := postJSON(t, h, "/v1/chat/completions", `{"model":"edge/mock","messages":[{"role":"user","content":"hi"}],"stream":`+strconv.FormatBool(stream)+`}`)
				body := rec.Body.String()
				wantStatus := http.StatusBadGateway
				if stream {
					wantStatus = http.StatusOK
					parts := strings.Split(strings.TrimSpace(body), "\n\n")
					body = strings.TrimPrefix(parts[len(parts)-1], "data: ")
				}
				if rec.Code != wantStatus {
					t.Fatalf("HTTP %d, want %d: %s", rec.Code, wantStatus, body)
				}
				var envelope struct {
					Error map[string]json.RawMessage `json:"error"`
				}
				if err := json.Unmarshal([]byte(body), &envelope); err != nil {
					t.Fatal(err)
				}
				var message string
				if err := json.Unmarshal(envelope.Error["message"], &message); err != nil || message != tc.message {
					t.Fatalf("message = %q, want %q: %s", message, tc.message, body)
				}
				var original map[string]json.RawMessage
				if err := json.Unmarshal([]byte(tc.raw), &original); err != nil {
					t.Fatal(err)
				}
				for key, value := range original {
					if key != "message" && string(envelope.Error[key]) != string(value) {
						t.Fatalf("provider field %s changed: %s", key, body)
					}
				}
			})
		}
	}
}
