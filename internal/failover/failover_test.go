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
		select {
		case u.bodies <- r.URL.Path + " " + string(b):
		default:
		}
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
	return send(t, tr, Endpoints{Provider: "dia", Primary: primary, Fallbacks: fallbacks})
}

func send(t *testing.T, tr *Transport, e Endpoints) *http.Response {
	t.Helper()
	ctx := WithEndpoints(context.Background(), e)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.Primary+"/chat/completions", strings.NewReader(`{"model":"m"}`))
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
	for _, code := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusInternalServerError} {
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

func TestStartedSpreadStreamIsNotReplayed(t *testing.T) {
	tr, _, _ := newTransport()
	cut := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"partial\":true}\n\n")
		w.(http.Flusher).Flush()
		conn, _, _ := w.(http.Hijacker).Hijack()
		conn.Close()
	}
	a, b := newUpstream(t, cut), newUpstream(t, cut)
	resp := send(t, tr, Endpoints{Provider: "dia", Primary: a.URL + "/v1", Fallbacks: []string{b.URL + "/v1"}, Spread: true, Session: "s1"})
	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Fatal("cut stream read cleanly")
	}
	if hits := a.hits.Load() + b.hits.Load(); hits != 1 {
		t.Fatalf("stream replayed: %d hits", hits)
	}
}

func TestRateLimitFailsOverAndHonoursRetryAfter(t *testing.T) {
	tr, clk, l := newTransport()
	var limited atomic.Bool
	limited.Store(true)
	primary := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if limited.Load() {
			w.Header().Set("Retry-After", "5")
			status(http.StatusTooManyRequests)(w, r)
			return
		}
		status(http.StatusOK)(w, r)
	})
	fallback := newUpstream(t, status(http.StatusOK))
	base, alt := primary.URL+"/v1", fallback.URL+"/v1"

	if resp := post(t, tr, base, alt); resp.StatusCode != http.StatusOK || fallback.hits.Load() != 1 {
		t.Fatalf("429 did not fail over: status %d fallback %d", resp.StatusCode, fallback.hits.Load())
	}
	if body := <-fallback.bodies; body != `/v1/chat/completions {"model":"m"}` {
		t.Fatalf("fallback got %q", body)
	}
	if !l.contains(base+" rate limited (429), cooling down 5s, trying "+alt) || !l.contains("serving from fallback "+alt) {
		t.Fatalf("logs %q", l.lines)
	}
	clk.advance(4 * time.Second)
	post(t, tr, base, alt)
	if primary.hits.Load() != 1 || fallback.hits.Load() != 2 {
		t.Fatalf("rate limited primary tried while cooling: primary %d fallback %d", primary.hits.Load(), fallback.hits.Load())
	}
	limited.Store(false)
	clk.advance(2 * time.Second)
	post(t, tr, base, alt)
	if primary.hits.Load() != 2 || fallback.hits.Load() != 2 || !l.contains("back on primary "+base) {
		t.Fatalf("primary not back after Retry-After: primary %d fallback %d logs %q", primary.hits.Load(), fallback.hits.Load(), l.lines)
	}
}

func TestEveryAddressRateLimitedReturnsLastAnswer(t *testing.T) {
	tr, _, l := newTransport()
	limitedFrom := func(name string) func(http.ResponseWriter, *http.Request) {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-From", name)
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_error","message":"all accounts are cooling down"}}`)
		}
	}
	primary := newUpstream(t, limitedFrom("primary"))
	fallback := newUpstream(t, limitedFrom("fallback"))
	resp := post(t, tr, primary.URL+"/v1", fallback.URL+"/v1")
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("X-From") != "fallback" || !strings.Contains(string(body), "all accounts are cooling down") {
		t.Fatalf("status %d from %q body %q", resp.StatusCode, resp.Header.Get("X-From"), body)
	}
	if primary.hits.Load() != 1 || fallback.hits.Load() != 1 || !l.contains("every address refused") {
		t.Fatalf("primary %d fallback %d logs %q", primary.hits.Load(), fallback.hits.Load(), l.lines)
	}
}

func TestSingleAddressPassesRateLimitThrough(t *testing.T) {
	for _, spread := range []bool{false, true} {
		tr, _, _ := newTransport()
		only := newUpstream(t, status(http.StatusTooManyRequests))
		resp := send(t, tr, Endpoints{Provider: "dia", Primary: only.URL + "/v1", Spread: spread, Session: "s1"})
		if resp.StatusCode != http.StatusTooManyRequests || only.hits.Load() != 1 {
			t.Fatalf("spread=%v: status %d hits %d", spread, resp.StatusCode, only.hits.Load())
		}
		resp = send(t, tr, Endpoints{Provider: "dia", Primary: only.URL + "/v1", Spread: spread, Session: "s1"})
		if resp.StatusCode != http.StatusTooManyRequests || only.hits.Load() != 2 {
			t.Fatalf("spread=%v: second request status %d hits %d", spread, resp.StatusCode, only.hits.Load())
		}
	}
}

// spreadSet is a provider with several instances that answer 200 and say
// which one they are.
type spreadSet struct {
	ups     []*upstream
	limited []atomic.Bool
}

func newSpreadSet(t *testing.T, n int) *spreadSet {
	t.Helper()
	s := &spreadSet{limited: make([]atomic.Bool, n)}
	for i := range n {
		s.ups = append(s.ups, newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-From", fmt.Sprint(i))
			if s.limited[i].Load() {
				status(http.StatusTooManyRequests)(w, r)
				return
			}
			status(http.StatusOK)(w, r)
		}))
	}
	return s
}

func (s *spreadSet) base(i int) string { return s.ups[i].URL + "/v1" }

func (s *spreadSet) endpoints(session string) Endpoints {
	var fallbacks []string
	for i := 1; i < len(s.ups); i++ {
		fallbacks = append(fallbacks, s.base(i))
	}
	return Endpoints{Provider: "dia", Primary: s.base(0), Fallbacks: fallbacks, Spread: true, Session: session}
}

// send returns which instance answered and with what status.
func (s *spreadSet) send(t *testing.T, tr *Transport, session string) (int, int) {
	t.Helper()
	resp := send(t, tr, s.endpoints(session))
	var from int
	if _, err := fmt.Sscan(resp.Header.Get("X-From"), &from); err != nil {
		t.Fatalf("no X-From: %v", err)
	}
	return from, resp.StatusCode
}

func TestSpreadKeepsSessionOnOneAddress(t *testing.T) {
	tr, _, _ := newTransport()
	s := newSpreadSet(t, 3)
	for _, session := range []string{"session:a", "session:b", "first-user:c"} {
		home, _ := s.send(t, tr, session)
		for range 5 {
			if got, _ := s.send(t, tr, session); got != home {
				t.Fatalf("%s moved from %d to %d", session, home, got)
			}
		}
	}
}

func TestSpreadDistributesSessions(t *testing.T) {
	tr, _, _ := newTransport()
	s := newSpreadSet(t, 3)
	for i := range 60 {
		s.send(t, tr, fmt.Sprintf("session:%d", i))
	}
	for i, u := range s.ups {
		if u.hits.Load() == 0 {
			t.Fatalf("instance %d got no session; hits %d/%d/%d", i, s.ups[0].hits.Load(), s.ups[1].hits.Load(), s.ups[2].hits.Load())
		}
	}
}

func TestSpreadRateLimitMovesSessionUntilCooldownEnds(t *testing.T) {
	tr, clk, l := newTransport()
	s := newSpreadSet(t, 2)
	const session = "session:omp-1"
	home, _ := s.send(t, tr, session)
	other := 1 - home
	s.limited[home].Store(true)

	if got, code := s.send(t, tr, session); got != other || code != http.StatusOK {
		t.Fatalf("rate limited session answered by %d with %d", got, code)
	}
	label := sessionLabel(session)
	if !l.contains(s.base(home)+" rate limited (429), cooling down 30s, trying "+s.base(other)) ||
		!l.contains("session "+label+" moved from "+s.base(home)+" to "+s.base(other)) {
		t.Fatalf("logs %q", l.lines)
	}
	homeHits := s.ups[home].hits.Load()
	clk.advance(20 * time.Second)
	if got, _ := s.send(t, tr, session); got != other || s.ups[home].hits.Load() != homeHits {
		t.Fatalf("cooling home tried: answered by %d, home hits %d -> %d", got, homeHits, s.ups[home].hits.Load())
	}

	s.limited[home].Store(false)
	clk.advance(11 * time.Second)
	if got, _ := s.send(t, tr, session); got != home {
		t.Fatalf("session did not return home after cooldown: answered by %d", got)
	}
	if !l.contains("session " + label + " moved from " + s.base(other) + " to " + s.base(home)) {
		t.Fatalf("return not logged: %q", l.lines)
	}
}

func TestSpreadWithoutSessionRotates(t *testing.T) {
	tr, _, _ := newTransport()
	s := newSpreadSet(t, 3)
	var order []int
	for range 6 {
		from, _ := s.send(t, tr, "")
		order = append(order, from)
	}
	if fmt.Sprint(order) != "[0 1 2 0 1 2]" {
		t.Fatalf("requests without a session went %v", order)
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
