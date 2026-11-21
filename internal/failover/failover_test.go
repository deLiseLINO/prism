package failover

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type upstream struct {
	*httptest.Server
	hits   atomic.Int32
	bodies chan string
}

func newUpstream(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *upstream {
	t.Helper()
	u := &upstream{bodies: make(chan string, 16)}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		u.bodies <- r.URL.Path + " " + string(b)
		handler(w, r)
	}))
	t.Cleanup(u.Close)
	return u
}

func status(code int) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
		_, _ = io.WriteString(w, http.StatusText(code))
	}
}

// deadURL is a base URL nothing listens on.
func deadURL(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return "http://" + addr + "/v1"
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type logs struct {
	mu    sync.Mutex
	lines []string
}

func (l *logs) Logf(format string, args ...any) {
	l.mu.Lock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
	l.mu.Unlock()
}

func (l *logs) contains(s string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if strings.Contains(line, s) {
			return true
		}
	}
	return false
}

func newTransport() (*Transport, *clock, *logs) {
	c := &clock{now: time.Unix(1_700_000_000, 0)}
	l := &logs{}
	return &Transport{Now: c.Now, Logf: l.Logf, Cooldown: 30 * time.Second}, c, l
}

func post(t *testing.T, tr *Transport, primary string, fallbacks ...string) *http.Response {
	t.Helper()
	ctx := WithEndpoints(context.Background(), "dia", primary, fallbacks)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, primary+"/chat/completions", strings.NewReader(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestUnreachablePrimaryFailsOverWithReplayedBody(t *testing.T) {
	tr, _, l := newTransport()
	fallback := newUpstream(t, status(http.StatusOK))
	resp := post(t, tr, deadURL(t), fallback.URL+"/v1")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if got := <-fallback.bodies; got != `/v1/chat/completions {"model":"m"}` {
		t.Fatalf("fallback saw %q", got)
	}
	if !l.contains("serving from fallback " + fallback.URL + "/v1") {
		t.Fatalf("no switch logged: %q", l.lines)
	}
}

func TestUnavailableStatusFailsOver(t *testing.T) {
	for _, code := range []int{http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
		tr, _, _ := newTransport()
		primary := newUpstream(t, status(code))
		fallback := newUpstream(t, status(http.StatusOK))
		resp := post(t, tr, primary.URL+"/v1", fallback.URL+"/v1")
		if resp.StatusCode != http.StatusOK || primary.hits.Load() != 1 || fallback.hits.Load() != 1 {
			t.Fatalf("%d: status %d primary %d fallback %d", code, resp.StatusCode, primary.hits.Load(), fallback.hits.Load())
		}
	}
}

func TestServiceAnswersDoNotFailOver(t *testing.T) {
	for _, code := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusInternalServerError} {
		tr, _, _ := newTransport()
		primary := newUpstream(t, status(code))
		fallback := newUpstream(t, status(http.StatusOK))
		resp := post(t, tr, primary.URL+"/v1", fallback.URL+"/v1")
		if resp.StatusCode != code || fallback.hits.Load() != 0 {
			t.Fatalf("%d: status %d fallback hits %d", code, resp.StatusCode, fallback.hits.Load())
		}
	}
}

func TestStartedStreamIsNotReplayed(t *testing.T) {
	tr, _, _ := newTransport()
	primary := newUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"partial\":true}\n\n")
		w.(http.Flusher).Flush()
		conn, _, _ := w.(http.Hijacker).Hijack()
		conn.Close()
	})
	fallback := newUpstream(t, status(http.StatusOK))
	resp := post(t, tr, primary.URL+"/v1", fallback.URL+"/v1")
	body, err := io.ReadAll(resp.Body)
	if err == nil {
		t.Fatalf("cut stream read cleanly: %q", body)
	}
	if !strings.Contains(string(body), "partial") || fallback.hits.Load() != 0 {
		t.Fatalf("body %q fallback hits %d", body, fallback.hits.Load())
	}
}

func TestFailedBaseCoolsDownThenReturnsToPrimary(t *testing.T) {
	tr, clk, l := newTransport()
	var primaryDown atomic.Bool
	primaryDown.Store(true)
	primary := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if primaryDown.Load() {
			status(http.StatusServiceUnavailable)(w, r)
			return
		}
		status(http.StatusOK)(w, r)
	})
	fallback := newUpstream(t, status(http.StatusOK))
	base, alt := primary.URL+"/v1", fallback.URL+"/v1"

	post(t, tr, base, alt)
	clk.advance(10 * time.Second)
	post(t, tr, base, alt)
	if primary.hits.Load() != 1 || fallback.hits.Load() != 2 {
		t.Fatalf("cooling primary was tried: primary %d fallback %d", primary.hits.Load(), fallback.hits.Load())
	}

	primaryDown.Store(false)
	clk.advance(25 * time.Second)
	post(t, tr, base, alt)
	if primary.hits.Load() != 2 || fallback.hits.Load() != 2 {
		t.Fatalf("primary not retried after cooldown: primary %d fallback %d", primary.hits.Load(), fallback.hits.Load())
	}
	if !l.contains("back on primary " + base) {
		t.Fatalf("no return logged: %q", l.lines)
	}
}

func TestAllCoolingStillTriesInOrder(t *testing.T) {
	tr, _, _ := newTransport()
	unavailableFrom := func(name string) func(http.ResponseWriter, *http.Request) {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-From", name)
			status(http.StatusServiceUnavailable)(w, r)
		}
	}
	primary := newUpstream(t, unavailableFrom("primary"))
	fallback := newUpstream(t, unavailableFrom("fallback"))
	base, alt := primary.URL+"/v1", fallback.URL+"/v1"
	post(t, tr, base, alt)
	resp := post(t, tr, base, alt)
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("X-From") != "fallback" || primary.hits.Load() != 2 || fallback.hits.Load() != 2 {
		t.Fatalf("status %d from %q primary %d fallback %d", resp.StatusCode, resp.Header.Get("X-From"), primary.hits.Load(), fallback.hits.Load())
	}
}

func TestRequestWithoutEndpointsPassesThrough(t *testing.T) {
	tr, _, _ := newTransport()
	primary := newUpstream(t, status(http.StatusServiceUnavailable))
	req, err := http.NewRequest(http.MethodGet, primary.URL+"/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || primary.hits.Load() != 1 {
		t.Fatalf("status %d hits %d", resp.StatusCode, primary.hits.Load())
	}
}

func TestRebaseMapsAPIRootOntoFallback(t *testing.T) {
	cases := []struct{ raw, from, to, want string }{
		{"http://a:1/v1/chat/completions", "http://a:1/v1", "http://b:2/v1/", "http://b:2/v1/chat/completions"},
		{"http://a:1/v1/models", "http://a:1/v1/chat/completions", "http://b:2/v1/chat/completions", "http://b:2/v1/models"},
		{"http://a:1/v1/messages", "http://a:1", "http://b:2/proxy", "http://b:2/proxy/v1/messages"},
	}
	for _, c := range cases {
		got, ok := rebase(c.raw, c.from, c.to)
		if !ok || got != c.want {
			t.Fatalf("rebase(%q, %q, %q) = %q, %v; want %q", c.raw, c.from, c.to, got, ok, c.want)
		}
	}
	if _, ok := rebase("http://other/v1/x", "http://a:1/v1", "http://b:2/v1"); ok {
		t.Fatal("unrelated URL was rebased")
	}
}
