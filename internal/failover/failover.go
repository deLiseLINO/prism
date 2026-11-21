// Package failover sends an upstream request to the next base URL of a
// provider when the current one cannot answer. A runner opts in per request
// by carrying the provider's base URLs in the request context; requests
// without them pass straight through.
package failover

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/deLiseLINO/prism/internal/providers/openaierr"
)

const (
	DefaultCooldown       = 30 * time.Second
	DefaultConnectTimeout = 10 * time.Second
)

var errConnectTimeout = errors.New("connect timeout")

type endpointsKey struct{}

type endpoints struct {
	provider string
	bases    []string
}

// WithEndpoints marks requests made under ctx as calls to provider, whose
// service answers at primary and, in order, at each fallback.
func WithEndpoints(ctx context.Context, provider, primary string, fallbacks []string) context.Context {
	if primary == "" || len(fallbacks) == 0 {
		return ctx
	}
	bases := make([]string, 0, 1+len(fallbacks))
	bases = append(bases, primary)
	bases = append(bases, fallbacks...)
	return context.WithValue(ctx, endpointsKey{}, endpoints{provider: provider, bases: bases})
}

// Transport tries a marked request on each base URL in order until one
// answers. A connection failure, a connect timeout, or a 502/503/504 moves on
// to the next base; any other response, 4xx and 429 included, is the
// service's answer. Once response headers are returned nothing is retried,
// so a stream that already reached the caller is never replayed. A base that
// failed is tried after the healthy ones for Cooldown.
type Transport struct {
	Base           http.RoundTripper
	Cooldown       time.Duration
	ConnectTimeout time.Duration
	Logf           func(format string, args ...any)
	Now            func() time.Time

	mu      sync.Mutex
	down    map[string]time.Time
	serving map[string]string
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
		if err == nil && !unavailable(resp.StatusCode) {
			t.served(ep, c.base)
			return resp, nil
		}
		t.markDown(c.base)
		if i == len(cands)-1 {
			return resp, err
		}
		reason := ""
		if err != nil {
			reason = err.Error()
		} else {
			reason = resp.Status
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
		}
		t.logf("prism: provider %s: %s unavailable (%s), trying %s", ep.provider, c.base, reason, cands[i+1].base)
	}
	return nil, errors.New("failover: no candidate base URL")
}

func unavailable(status int) bool {
	return status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}

// candidates maps the request URL onto every base, healthy bases first, each
// group in configured order. A base the URL cannot be mapped onto is left out.
func (t *Transport) candidates(ep endpoints, raw string) []candidate {
	primary := ep.bases[0]
	var healthy, cooling []candidate
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, base := range ep.bases {
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

func (t *Transport) markDown(base string) {
	cooldown := t.Cooldown
	if cooldown <= 0 {
		cooldown = DefaultCooldown
	}
	until := t.now().Add(cooldown)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.down == nil {
		t.down = map[string]time.Time{}
	}
	t.down[base] = until
}

// served records which base answered for the provider and logs when that
// moves off the primary or back to it.
func (t *Transport) served(ep endpoints, base string) {
	primary := ep.bases[0]
	t.mu.Lock()
	delete(t.down, base)
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
