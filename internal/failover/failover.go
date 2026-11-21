// Package failover sends an upstream request to another base URL of a
// provider when the current one cannot answer. A runner opts in per request
// by carrying the provider's base URLs in the request context; requests
// without them pass straight through.
package failover

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/deLiseLINO/prism/internal/providers/openaierr"
)

const (
	DefaultCooldown       = 30 * time.Second
	DefaultConnectTimeout = 10 * time.Second
	// MaxRateLimitCooldown bounds how long a Retry-After keeps a base at the
	// back of the order.
	MaxRateLimitCooldown = 5 * time.Minute
)

// sessionMemory bounds how many sessions per transport have their last
// base remembered for move logs.
const sessionMemory = 4096

var errConnectTimeout = errors.New("connect timeout")

type endpointsKey struct{}

// Endpoints are the addresses of one provider's service. In failover mode
// Primary is tried first and Fallbacks follow in order. In Spread mode each
// Session prefers its own base, picked by rendezvous hashing over all of
// them; requests without a Session rotate through the bases.
type Endpoints struct {
	Provider  string
	Primary   string
	Fallbacks []string
	Spread    bool
	Session   string
}

type endpoints struct {
	provider string
	bases    []string
	spread   bool
	session  string
}

// WithEndpoints marks requests made under ctx as calls to e.Provider. A
// provider with a single address is left unmarked.
func WithEndpoints(ctx context.Context, e Endpoints) context.Context {
	if e.Primary == "" || len(e.Fallbacks) == 0 {
		return ctx
	}
	bases := make([]string, 0, 1+len(e.Fallbacks))
	bases = append(bases, e.Primary)
	bases = append(bases, e.Fallbacks...)
	return context.WithValue(ctx, endpointsKey{}, endpoints{provider: e.Provider, bases: bases, spread: e.Spread, session: e.Session})
}

// Transport tries a marked request on each base URL in turn until one
// answers. A connection failure, a connect timeout, a 502/503/504 or a 429
// moves on to the next base; any other response is the service's answer.
// Once response headers are returned nothing is retried, so a stream that
// already reached the caller is never replayed. A base that failed is tried
// after the healthy ones for Cooldown, or for the 429's Retry-After. When
// every base fails, the last answer is returned as it is.
type Transport struct {
	Base           http.RoundTripper
	Cooldown       time.Duration
	ConnectTimeout time.Duration
	Logf           func(format string, args ...any)
	Now            func() time.Time

	mu       sync.Mutex
	down     map[string]time.Time
	serving  map[string]string
	sessions map[string]string
	next     map[string]int
}

type candidate struct {
	base string
	url  *url.URL
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	ep, ok := req.Context().Value(endpointsKey{}).(endpoints)
	if !ok || req.Body != nil && req.Body != http.NoBody && req.GetBody == nil {
		return t.base().RoundTrip(req)
	}
	cands := t.candidates(ep, req.URL.String())
	if len(cands) < 2 {
		return t.base().RoundTrip(req)
	}
	for i, c := range cands {
		resp, err := t.attempt(req, c, i > 0)
		if req.Context().Err() != nil {
			return resp, err
		}
		if err == nil && !retryable(resp.StatusCode) {
			t.served(ep, c.base)
			return resp, nil
		}
		cooldown := t.cooldown()
		if err == nil && resp.StatusCode == http.StatusTooManyRequests {
			cooldown = retryAfter(resp.Header, t.now(), cooldown)
		}
		t.markDown(c.base, cooldown)
		last := i == len(cands)-1
		switch {
		case err != nil:
			if !last {
				t.logf("prism: provider %s: %s unavailable (%s), trying %s", ep.provider, c.base, err, cands[i+1].base)
			}
		case resp.StatusCode == http.StatusTooManyRequests:
			if last {
				t.logf("prism: provider %s: %s rate limited (429), cooling down %s; every address refused", ep.provider, c.base, cooldown)
			} else {
				t.logf("prism: provider %s: %s rate limited (429), cooling down %s, trying %s", ep.provider, c.base, cooldown, cands[i+1].base)
			}
		case !last:
			t.logf("prism: provider %s: %s unavailable (%s), trying %s", ep.provider, c.base, resp.Status, cands[i+1].base)
		}
		if last {
			return resp, err
		}
		if resp != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
		}
	}
	return nil, errors.New("failover: no candidate base URL")
}

func retryable(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusBadGateway ||
		status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}

// retryAfter reads a 429's Retry-After, in seconds or as an HTTP date, and
// falls back to def when it is absent or not in the future.
func retryAfter(h http.Header, now time.Time, def time.Duration) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return def
	}
	var d time.Duration
	if secs, err := strconv.Atoi(v); err == nil {
		d = time.Duration(secs) * time.Second
	} else if at, err := http.ParseTime(v); err == nil {
		d = at.Sub(now)
	}
	if d <= 0 {
		return def
	}
	return min(d, MaxRateLimitCooldown)
}

// candidates maps the request URL onto every base in the mode's order,
// healthy bases first. A base the URL cannot be mapped onto is left out.
func (t *Transport) candidates(ep endpoints, raw string) []candidate {
	primary := ep.bases[0]
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	var healthy, cooling []candidate
	for _, base := range t.order(ep) {
		mapped, ok := rebase(raw, primary, base)
		if !ok {
			if base == primary {
				return nil
			}
			continue
		}
		u, err := url.Parse(mapped)
		if err != nil {
			continue
		}
		c := candidate{base: base, url: u}
		if until, ok := t.down[base]; ok && now.Before(until) {
			cooling = append(cooling, c)
		} else {
			healthy = append(healthy, c)
		}
	}
	return append(healthy, cooling...)
}

// order lists the bases in the order the mode prefers them. Callers hold mu.
func (t *Transport) order(ep endpoints) []string {
	if !ep.spread {
		return ep.bases
	}
	if ep.session == "" {
		if t.next == nil {
			t.next = map[string]int{}
		}
		start := t.next[ep.provider] % len(ep.bases)
		t.next[ep.provider] = (start + 1) % len(ep.bases)
		return append(slices.Clone(ep.bases[start:]), ep.bases[:start]...)
	}
	out := slices.Clone(ep.bases)
	scores := make(map[string]uint64, len(out))
	for _, base := range out {
		scores[base] = rendezvous(ep.session, base)
	}
	slices.SortStableFunc(out, func(a, b string) int {
		switch sa, sb := scores[a], scores[b]; {
		case sa > sb:
			return -1
		case sa < sb:
			return 1
		}
		return 0
	})
	return out
}

func rendezvous(session, base string) uint64 {
	h := sha256.New()
	h.Write([]byte(session))
	h.Write([]byte{0})
	h.Write([]byte(base))
	return binary.BigEndian.Uint64(h.Sum(nil))
}

// rebase rewrites raw, a URL built from the base from, onto the base to.
// Runners either append to the configured base or to its API root, so both
// spellings are tried.
func rebase(raw, from, to string) (string, bool) {
	if from == to {
		return raw, true
	}
	for _, pair := range [][2]string{
		{strings.TrimRight(strings.TrimSpace(from), "/"), strings.TrimRight(strings.TrimSpace(to), "/")},
		{openaierr.APIBase(from), openaierr.APIBase(to)},
	} {
		if rest, ok := strings.CutPrefix(raw, pair[0]); ok && (rest == "" || strings.ContainsRune("/?#", rune(rest[0]))) {
			return pair[1] + rest, true
		}
	}
	return "", false
}

// attempt sends req to c. A replay reads a fresh copy of the body; the first
// attempt consumes the caller's.
func (t *Transport) attempt(req *http.Request, c candidate, replay bool) (*http.Response, error) {
	ctx, cancel := context.WithCancelCause(req.Context())
	timeout := t.ConnectTimeout
	if timeout <= 0 {
		timeout = DefaultConnectTimeout
	}
	timer := time.AfterFunc(timeout, func() { cancel(errConnectTimeout) })
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) { timer.Stop() }})
	r := req.Clone(ctx)
	if r.URL.String() != c.url.String() {
		r.URL = c.url
		r.Host = ""
	}
	if replay && req.Body != nil && req.Body != http.NoBody {
		body, err := req.GetBody()
		if err != nil {
			timer.Stop()
			cancel(nil)
			return nil, err
		}
		r.Body = body
	}
	resp, err := t.base().RoundTrip(r)
	timer.Stop()
	if err != nil {
		if errors.Is(context.Cause(ctx), errConnectTimeout) {
			err = fmt.Errorf("%w after %s: %w", errConnectTimeout, timeout, err)
		}
		cancel(nil)
		return nil, err
	}
	resp.Body = cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelCauseFunc
}

func (b cancelOnClose) Close() error {
	err := b.ReadCloser.Close()
	b.cancel(nil)
	return err
}

func (t *Transport) cooldown() time.Duration {
	if t.Cooldown > 0 {
		return t.Cooldown
	}
	return DefaultCooldown
}

func (t *Transport) markDown(base string, cooldown time.Duration) {
	until := t.now().Add(cooldown)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.down == nil {
		t.down = map[string]time.Time{}
	}
	if until.After(t.down[base]) {
		t.down[base] = until
	}
}

// served records which base answered and logs a move: per provider in
// failover mode, per session in spread mode.
func (t *Transport) served(ep endpoints, base string) {
	t.mu.Lock()
	delete(t.down, base)
	if ep.spread {
		if ep.session == "" {
			t.mu.Unlock()
			return
		}
		key := ep.provider + "\x00" + ep.session
		prev, ok := t.sessions[key]
		if t.sessions == nil || len(t.sessions) >= sessionMemory && !ok {
			t.sessions = map[string]string{}
		}
		t.sessions[key] = base
		t.mu.Unlock()
		if ok && prev != base {
			t.logf("prism: provider %s: session %s moved from %s to %s", ep.provider, sessionLabel(ep.session), prev, base)
		}
		return
	}
	primary := ep.bases[0]
	prev, ok := t.serving[ep.provider]
	if !ok {
		prev = primary
	}
	if t.serving == nil {
		t.serving = map[string]string{}
	}
	t.serving[ep.provider] = base
	t.mu.Unlock()
	switch {
	case prev == base:
	case base == primary:
		t.logf("prism: provider %s: back on primary %s", ep.provider, base)
	default:
		t.logf("prism: provider %s: serving from fallback %s", ep.provider, base)
	}
}

// sessionLabel names a session in logs without printing the client's id.
func sessionLabel(session string) string {
	sum := sha256.Sum256([]byte(session))
	return hex.EncodeToString(sum[:4])
}

func (t *Transport) base() http.RoundTripper {
	if t.Base != nil {
		return t.Base
	}
	return http.DefaultTransport
}

func (t *Transport) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

func (t *Transport) logf(format string, args ...any) {
	if t.Logf != nil {
		t.Logf(format, args...)
	}
}
