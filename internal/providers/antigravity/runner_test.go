package antigravity

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"prism/internal/account"
	"prism/internal/canon"
	"prism/internal/provider"
)

type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	slept []time.Duration
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Sleep(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.slept = append(c.slept, d)
	c.now = c.now.Add(d)
}

func (c *fakeClock) sleeps() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.slept...)
}

type stubCreds struct {
	pair CredentialPair
	err  error
}

func (s stubCreds) Credential(ctx context.Context, lease account.Lease) (CredentialPair, error) {
	if s.err != nil {
		return CredentialPair{}, s.err
	}
	return s.pair, nil
}

type recordingSink struct {
	events []canon.Event
}

func (s *recordingSink) Emit(e canon.Event) error {
	s.events = append(s.events, e)
	return nil
}

func testRequest() provider.RunRequest {
	return provider.RunRequest{
		Request: canon.Request{
			Model: "gemini-3.7-flash",
			Input: []canon.Item{textMessage(canon.RoleUser, "Hi")},
		},
		Target: provider.Target{Provider: "google-antigravity", Wire: provider.WireAntigravity},
		Lease:  account.Lease{Provider: "google-antigravity", Account: "acc-1", CredGen: 3},
	}
}

func fixedSSE(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}
}

func textStream() string {
	return "data: {\"response\":{\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":1}}}\n\n"
}

func TestRunnerSendsFingerprintHeaders(t *testing.T) {
	var gotUA, gotAuth, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.RequestURI()
		fixedSSE(textStream())(w, r)
	}))
	defer server.Close()
	runner, err := NewRunner(stubCreds{pair: CredentialPair{AccessToken: "tok-1", ProjectID: "proj-1"}}, server.Client(), server.URL)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	var sink recordingSink
	if err := runner.Run(context.Background(), testRequest(), &sink); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gotUA != "antigravity/ide/2.5.5 (os_type=windows; arch=amd64; aidev_client; auth_method=oauth)" {
		t.Fatalf("user agent = %q", gotUA)
	}
	if gotAuth != "Bearer tok-1" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if gotPath != "/v1internal:streamGenerateContent?alt=sse" {
		t.Fatalf("request URI = %q", gotPath)
	}
}

func TestRunnerEnvelopeCarriesProjectAndSession(t *testing.T) {
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = readAllBody(r)
		fixedSSE(textStream())(w, r)
	}))
	defer server.Close()
	runner, _ := NewRunner(stubCreds{pair: CredentialPair{AccessToken: "t", ProjectID: "proj-9"}}, server.Client(), server.URL)
	req := testRequest()
	req.Facts.Thread = "thread-42"
	var sink recordingSink
	if err := runner.Run(context.Background(), req, &sink); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := string(body)
	if !strings.Contains(got, `"project":"proj-9"`) {
		t.Fatalf("project missing: %s", got)
	}
	wantSession := SessionID("thread-42", "Hi")
	if !strings.Contains(got, `"sessionId":"`+wantSession+`"`) {
		t.Fatalf("session id mismatch: %s (want %s)", got, wantSession)
	}
	if !strings.Contains(got, `"requestId":"agent-`) {
		t.Fatalf("request id missing: %s", got)
	}
	if !strings.Contains(got, `"userAgent":"antigravity"`) {
		t.Fatalf("envelope userAgent must be the protocol constant: %s", got)
	}
}

func TestRunnerRetryThenSuccess(t *testing.T) {
	clock := newFakeClock()
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fixedSSE(textStream())(w, r)
	}))
	defer server.Close()
	runner, _ := NewRunner(stubCreds{pair: CredentialPair{AccessToken: "t"}}, server.Client(), server.URL)
	runner.SetSleep(clock.Sleep)
	runner.SetRand(func() float64 { return 0.5 })
	var sink recordingSink
	if err := runner.Run(context.Background(), testRequest(), &sink); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	if len(clock.sleeps()) != 2 {
		t.Fatalf("sleeps = %v", clock.sleeps())
	}
}

func TestRunner429BodyPeek(t *testing.T) {
	clock := newFakeClock()
	t.Run("quota exhausted does not retry", func(t *testing.T) {
		attempts := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempts++
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"Quota exceeded for the day"}}`))
		}))
		defer server.Close()
		runner, _ := NewRunner(stubCreds{pair: CredentialPair{AccessToken: "t"}}, server.Client(), server.URL)
		runner.SetSleep(clock.Sleep)
		var sink recordingSink
		err := runner.Run(context.Background(), testRequest(), &sink)
		var runErr provider.RunError
		if !errors.As(err, &runErr) {
			t.Fatalf("expected RunError, got %v", err)
		}
		if runErr.Class != provider.ClassQuotaExhausted {
			t.Fatalf("class = %v", runErr.Class)
		}
		if attempts != 1 {
			t.Fatalf("attempts = %d, want 1 (no retry on hard quota)", attempts)
		}
	})
	t.Run("transient rate limit retries", func(t *testing.T) {
		attempts := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempts++
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"Too many requests"}}`))
		}))
		defer server.Close()
		runner, _ := NewRunner(stubCreds{pair: CredentialPair{AccessToken: "t"}}, server.Client(), server.URL)
		runner.SetSleep(clock.Sleep)
		runner.SetRand(func() float64 { return 0 })
		var sink recordingSink
		err := runner.Run(context.Background(), testRequest(), &sink)
		var runErr provider.RunError
		if !errors.As(err, &runErr) {
			t.Fatalf("expected RunError, got %v", err)
		}
		if runErr.Class != provider.ClassRateLimited || runErr.Kind != provider.Retryable {
			t.Fatalf("class/kind = %v/%v", runErr.Class, runErr.Kind)
		}
		if runErr.RetryAfter != time.Second {
			t.Fatalf("retry after = %v", runErr.RetryAfter)
		}
		if attempts != 3 {
			t.Fatalf("attempts = %d, want 3", attempts)
		}
	})
}

func TestRunnerRepairAndReplay(t *testing.T) {
	clock := newFakeClock()
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodies = append(bodies, string(readAllBodyMust(r)))
		if len(bodies) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Invalid JSON payload: function_declarations[0].parameters is not valid"}}`))
			return
		}
		fixedSSE(textStream())(w, r)
	}))
	defer server.Close()
	runner, _ := NewRunner(stubCreds{pair: CredentialPair{AccessToken: "t"}}, server.Client(), server.URL)
	runner.SetSleep(clock.Sleep)
	req := testRequest()
	req.Request.Tools = []canon.Tool{canon.FunctionTool{
		Name:       "get_weather",
		Parameters: []byte(`{"type":"object","properties":{"broken":{"type":"not-a-type"}}}`),
	}}
	var sink recordingSink
	if err := runner.Run(context.Background(), req, &sink); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("bodies = %d, want 2 (original + repaired replay)", len(bodies))
	}
	if !strings.Contains(bodies[0], `"broken"`) {
		t.Fatalf("original body missing the property name: %s", bodies[0])
	}
	if strings.Contains(bodies[0], `not-a-type`) {
		t.Fatalf("invalid type must not reach the wire: %s", bodies[0])
	}
	if !strings.Contains(bodies[1], `{"type":"object","properties":{}}`) {
		t.Fatalf("repaired body must carry the empty object schema: %s", bodies[1])
	}
}

func TestRunnerRunErrorClassification(t *testing.T) {
	cases := []struct {
		status int
		class  provider.ErrorClass
		kind   provider.RunErrorKind
	}{
		{http.StatusUnauthorized, provider.ClassUnauthorized, provider.TerminalOmitted},
		{http.StatusForbidden, provider.ClassUnauthorized, provider.TerminalOmitted},
		{http.StatusNotFound, provider.ClassNotFound, provider.TerminalOmitted},
		{http.StatusInternalServerError, provider.ClassServer, provider.Retryable},
		{http.StatusBadGateway, provider.ClassServer, provider.Retryable},
		{http.StatusBadRequest, provider.ClassInvalidRequest, provider.TerminalOmitted},
	}
	for _, tc := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
		}))
		runner, _ := NewRunner(stubCreds{pair: CredentialPair{AccessToken: "t"}}, server.Client(), server.URL)
		runner.SetSleep(func(time.Duration) {})
		runner.SetRand(func() float64 { return 0 })
		var sink recordingSink
		err := runner.Run(context.Background(), testRequest(), &sink)
		server.Close()
		var runErr provider.RunError
		if !errors.As(err, &runErr) {
			t.Fatalf("status %d: expected RunError, got %v", tc.status, err)
		}
		if runErr.Class != tc.class || runErr.Kind != tc.kind {
			t.Fatalf("status %d: class/kind = %v/%v, want %v/%v", tc.status, runErr.Class, runErr.Kind, tc.class, tc.kind)
		}
	}
}

func TestRunnerCredentialFailureClassification(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		class provider.ErrorClass
		kind  provider.RunErrorKind
	}{
		{"rejected grant", account.ErrNeedsReauth, provider.ClassUnauthorized, provider.TerminalOmitted},
		{"transient refresh", account.ErrRefreshTransient, provider.ClassTransport, provider.Retryable},
		{"unknown", errors.New("missing generation"), provider.ClassTransport, provider.Retryable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner, err := NewRunner(stubCreds{err: tc.err}, http.DefaultClient, DefaultBaseURL)
			if err != nil {
				t.Fatalf("NewRunner: %v", err)
			}
			var sink recordingSink
			runErr := runner.Run(context.Background(), testRequest(), &sink)
			var re provider.RunError
			if !errors.As(runErr, &re) {
				t.Fatalf("expected RunError, got %v", runErr)
			}
			if re.Class != tc.class || re.Kind != tc.kind {
				t.Fatalf("class/kind = %v/%v, want %v/%v", re.Class, re.Kind, tc.class, tc.kind)
			}
			if len(sink.events) != 0 {
				t.Fatalf("no events must be emitted before the wire: %v", sink.events)
			}
		})
	}
}

func TestRunnerRetryAfterHonoredCapped(t *testing.T) {
	clock := newFakeClock()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "10")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	runner, _ := NewRunner(stubCreds{pair: CredentialPair{AccessToken: "t"}}, server.Client(), server.URL)
	runner.SetSleep(clock.Sleep)
	runner.SetRand(func() float64 { return 0 })
	var sink recordingSink
	_ = runner.Run(context.Background(), testRequest(), &sink)
	sleeps := clock.sleeps()
	if len(sleeps) != 2 {
		t.Fatalf("sleeps = %v", sleeps)
	}
	for _, d := range sleeps {
		if d != BackoffMax {
			t.Fatalf("sleep = %v, want capped %v", d, BackoffMax)
		}
	}
}

func TestRunnerRegister(t *testing.T) {
	runner, err := NewRunner(stubCreds{pair: CredentialPair{AccessToken: "t"}}, http.DefaultClient, DefaultBaseURL)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	reg := provider.NewRegistry()
	if err := runner.Register(reg, "google-antigravity"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, ok := reg.Lookup("google-antigravity"); !ok {
		t.Fatal("runner not registered")
	}
	if err := runner.Register(reg, "google-antigravity"); !errors.Is(err, provider.ErrDuplicateProvider) {
		t.Fatalf("duplicate register = %v", err)
	}
}

func TestNewRunnerGuards(t *testing.T) {
	if _, err := NewRunner(nil, http.DefaultClient, ""); err == nil {
		t.Fatal("nil credential source must be rejected")
	}
	if _, err := NewRunner(stubCreds{}, nil, ""); err == nil {
		t.Fatal("nil http client must be rejected")
	}
	if _, err := NewRunner(stubCreds{}, http.DefaultClient, ""); err != nil {
		t.Fatalf("default base URL path failed: %v", err)
	}
}

func TestErrorEnvelopeMessage(t *testing.T) {
	got := errorEnvelopeMessage(`{"error":{"message":"boom","status":"INVALID_ARGUMENT"}}`)
	if got != "INVALID_ARGUMENT: boom" {
		t.Fatalf("message = %q", got)
	}
	if got := errorEnvelopeMessage("plain text"); got != "plain text" {
		t.Fatalf("message = %q", got)
	}
}

func readAllBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, fmt.Errorf("nil body")
	}
	buf := make([]byte, 0, 1024)
	tmp := make([]byte, 512)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	return buf, nil
}

func readAllBodyMust(r *http.Request) []byte {
	data, err := readAllBody(r)
	if err != nil {
		panic(err)
	}
	return data
}
