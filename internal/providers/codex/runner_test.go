package codex

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"prism/internal/account"
	"prism/internal/canon"
	"prism/internal/provider"
	"prism/internal/quota"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newRunnerWithTransport(t *testing.T, fn roundTripFunc) *Runner {
	t.Helper()
	creds := fixedCreds{cred: Credential{AccessToken: "tok-1", ChatGPTAccountID: "acct-1"}}
	return &Runner{
		Creds:  creds,
		Client: &http.Client{Transport: fn},
		Now:    func() time.Time { return time.Unix(0, 0).UTC() },
	}
}

func newResp(status int, headers http.Header, body string) *http.Response {
	h := http.Header{}
	for k, v := range headers {
		for _, vv := range v {
			h.Add(k, vv)
		}
	}
	return &http.Response{
		StatusCode: status,
		Header:     h,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

type captureSink struct {
	events []canon.Event
}

func (c *captureSink) Emit(ev canon.Event) error { c.events = append(c.events, ev); return nil }

func runOnce(t *testing.T, rt roundTripFunc) (provider.RunError, *captureSink) {
	t.Helper()
	runner := newRunnerWithTransport(t, rt)
	sink := &captureSink{}
	err := runner.Run(context.Background(), provider.RunRequest{}, sink)
	var re provider.RunError
	if !errors.As(err, &re) {
		t.Fatalf("expected RunError, got %T %v", err, err)
	}
	return re, sink
}

func TestRunErrorClassifyStatus(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   provider.ErrorClass
	}{
		{"401", http.StatusUnauthorized, provider.ClassUnauthorized},
		{"403", http.StatusForbidden, provider.ClassUnauthorized},
		{"404", http.StatusNotFound, provider.ClassNotFound},
		{"408", http.StatusRequestTimeout, provider.ClassTimeout},
		{"429", http.StatusTooManyRequests, provider.ClassRateLimited},
		{"402", http.StatusPaymentRequired, provider.ClassQuotaExhausted},
		{"500", http.StatusInternalServerError, provider.ClassServer},
		{"502", http.StatusBadGateway, provider.ClassServer},
		{"503", http.StatusServiceUnavailable, provider.ClassServer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := func(*http.Request) (*http.Response, error) {
				return newResp(tc.status, nil, ""), nil
			}
			re, _ := runOnce(t, rt)
			if re.Class != tc.want {
				t.Fatalf("class = %d want %d", re.Class, tc.want)
			}
		})
	}
}

func TestRunErrorInvalidRequestIsTerminalOmitted(t *testing.T) {
	rt := func(*http.Request) (*http.Response, error) {
		return newResp(http.StatusBadRequest, nil, ""), nil
	}
	re, _ := runOnce(t, rt)
	if re.Class != provider.ClassInvalidRequest {
		t.Fatalf("class = %d", re.Class)
	}
	if re.Kind != provider.TerminalOmitted {
		t.Fatalf("kind = %d want TerminalOmitted", re.Kind)
	}
}

func TestRunErrorCredentialFetchFails(t *testing.T) {
	runner := &Runner{
		Creds: errCreds{},
		Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("transport must not be invoked")
			return nil, nil
		})},
		Now: func() time.Time { return time.Unix(0, 0).UTC() },
	}
	sink := &captureSink{}
	err := runner.Run(context.Background(), provider.RunRequest{}, sink)
	var re provider.RunError
	if !errors.As(err, &re) {
		t.Fatalf("expected RunError, got %T %v", err, err)
	}
	if re.Class != provider.ClassTransport {
		t.Fatalf("class = %d", re.Class)
	}
}

type errCreds struct{}

func (errCreds) Credential(ctx context.Context, lease account.Lease) (Credential, error) {
	return Credential{}, errors.New("nope")
}

func TestClassForStatus(t *testing.T) {
	cases := map[int]provider.ErrorClass{
		http.StatusUnauthorized:        provider.ClassUnauthorized,
		http.StatusForbidden:           provider.ClassUnauthorized,
		http.StatusTooManyRequests:     provider.ClassRateLimited,
		http.StatusPaymentRequired:     provider.ClassQuotaExhausted,
		http.StatusNotFound:            provider.ClassNotFound,
		http.StatusRequestTimeout:      provider.ClassTimeout,
		http.StatusInternalServerError: provider.ClassServer,
		http.StatusBadGateway:          provider.ClassServer,
		http.StatusServiceUnavailable:  provider.ClassServer,
		http.StatusBadRequest:          provider.ClassInvalidRequest,
	}
	for status, want := range cases {
		if got := classForStatus(status); got != want {
			t.Fatalf("status %d: got %d want %d", status, got, want)
		}
	}
}

func TestStreamEndsWithoutTerminalReportsFailure(t *testing.T) {
	rt := func(*http.Request) (*http.Response, error) {
		body := "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"m\",\"delta\":\"x\"}\n\n"
		return newResp(http.StatusOK, nil, body), nil
	}
	runner := newRunnerWithTransport(t, rt)
	sink := &captureSink{}
	err := runner.Run(context.Background(), provider.RunRequest{}, sink)
	var re provider.RunError
	if !errors.As(err, &re) {
		t.Fatalf("expected RunError, got %T %v", err, err)
	}
	if re.Kind != provider.TerminalEmitted {
		t.Fatalf("kind = %d want TerminalEmitted", re.Kind)
	}
	if len(sink.events) == 0 {
		t.Fatal("expected at least one event")
	}
	last := sink.events[len(sink.events)-1]
	if _, ok := last.(canon.TurnFailed); !ok {
		t.Fatalf("last event = %T want TurnFailed", last)
	}
}

func TestRunQuotaHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("content-type", "text/event-stream")
	h.Set("x-codex-primary-used-percent", "50")
	h.Set("x-codex-primary-reset-at", "1800000000")
	rt := func(*http.Request) (*http.Response, error) {
		return newResp(http.StatusOK, h, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r-1\"}}\n\n"), nil
	}
	runner := newRunnerWithTransport(t, rt)
	runner.QuotaSink = func(_ account.AccountID, snap quota.Snapshot, _ []string) {
		if snap.Used != 5000 {
			t.Fatalf("quota used = %d want 5000", snap.Used)
		}
	}
	sink := &captureSink{}
	if err := runner.Run(context.Background(), provider.RunRequest{}, sink); err != nil {
		t.Fatalf("Run: %v", err)
	}
}
