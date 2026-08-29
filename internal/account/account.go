package account

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash"
	"sort"
	"sync"
	"time"

	"prism/internal/execution"
	"prism/internal/quota"
)

type ProviderID string

type ProviderKind uint8

const (
	KindCodex ProviderKind = iota + 1
	KindAntigravity
	KindOpenAIResponses
	KindAnthropic
)

type Instance struct {
	ID   ProviderID
	Kind ProviderKind
}

type AccountID string

type State uint8

const (
	Active State = iota + 1
	CoolingDown
	NeedsReauth
	SoftAvoid
	Paused
)

type CredentialGeneration uint64

type StateVersion uint64

type Account struct {
	ID             AccountID
	Provider       ProviderID
	Priority       int
	State          State
	CredGen        CredentialGeneration
	Version        StateVersion
	Quota          quota.Snapshot
	CooldownUntil  time.Time
	SoftAvoidUntil time.Time
	InFlight       int
}

type Lease struct {
	Provider ProviderID
	Account  AccountID
	CredGen  CredentialGeneration
	Version  StateVersion
	Probe    bool
}

type QuotaGroup string

type ModelID string

type Usage struct {
	InputTokens       int64
	OutputTokens      int64
	CachedInputTokens int64
	ReasoningTokens   int64
	TotalTokens       int64
}

type AcquireRequest struct {
	Provider   ProviderID
	Model      ModelID
	QuotaGroup QuotaGroup
	Session    execution.SessionKey
	Thread     execution.ThreadKey
}

type Outcome interface{ outcome() }

type TurnSucceeded struct{ Usage Usage }

type AuthRejected struct{}

type RateLimited struct{ RetryAfter time.Duration }

type QuotaExhausted struct{ RetryAfter time.Duration }

type NotFound struct{}

type RequestTimeout struct{ RetryAfter time.Duration }

type ServerError struct{}

type TransportFailure struct{}

type ProbeSucceeded struct{}

type ProbeFailed struct{}

func (TurnSucceeded) outcome()    {}
func (AuthRejected) outcome()     {}
func (RateLimited) outcome()      {}
func (QuotaExhausted) outcome()   {}
func (NotFound) outcome()         {}
func (RequestTimeout) outcome()   {}
func (ServerError) outcome()      {}
func (TransportFailure) outcome() {}
func (ProbeSucceeded) outcome()   {}
func (ProbeFailed) outcome()      {}

type Snapshot struct {
	Accounts []Account
}

type Pool interface {
	Acquire(ctx context.Context, req AcquireRequest) (Lease, error)
	Record(ctx context.Context, l Lease, o Outcome) error
	Pause(ctx context.Context, id AccountID, ifVersion StateVersion) error
	Resume(ctx context.Context, id AccountID, ifVersion StateVersion) error
	UpdatePriority(ctx context.Context, id AccountID, p int, ifVersion StateVersion) error
	Snapshot() Snapshot
}

var (
	ErrStale     = errors.New("account: stale state version")
	ErrNotFound  = errors.New("account: account not found")
	ErrNoAccount = errors.New("account: no usable account")
)

const (
	affinityCapacity = 2048
	affinityIdleTTL  = 24 * time.Hour
	probeInterval    = 5 * time.Minute
	softAvoidWindow  = 10 * time.Minute
)

type affinityEntry struct {
	Account    AccountID
	LastAccess time.Time
}

type pool struct {
	mu        sync.Mutex
	secret    []byte
	now       func() time.Time
	accounts  map[AccountID]*Account
	affinity  map[[32]byte]affinityEntry
	lastProbe map[AccountID]time.Time
}

func New(secret []byte, now func() time.Time) *pool {
	if now == nil {
		now = time.Now
	}
	return &pool{
		secret:    append([]byte(nil), secret...),
		now:       now,
		accounts:  make(map[AccountID]*Account),
		affinity:  make(map[[32]byte]affinityEntry),
		lastProbe: make(map[AccountID]time.Time),
	}
}

func (p *pool) Register(a Account) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if prev, ok := p.accounts[a.ID]; ok {
		a.InFlight = prev.InFlight
	}
	p.accounts[a.ID] = &a
}

func (p *pool) Acquire(ctx context.Context, req AcquireRequest) (Lease, error) {
	if err := ctx.Err(); err != nil {
		return Lease{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	key := p.affinityKey(req)
	if e, ok := p.affinity[key]; ok {
		if now.Sub(e.LastAccess) >= affinityIdleTTL {
			delete(p.affinity, key)
		} else if a := p.accounts[e.Account]; a != nil && p.usable(a, now) {
			e.LastAccess = now
			p.affinity[key] = e
			a.InFlight++
			return p.lease(a, false), nil
		} else {
			delete(p.affinity, key)
		}
	}
	if a := p.selectBest(now); a != nil {
		p.sweepAffinity(now)
		p.affinity[key] = affinityEntry{Account: a.ID, LastAccess: now}
		a.InFlight++
		return p.lease(a, false), nil
	}
	if a := p.selectProbe(now); a != nil {
		p.lastProbe[a.ID] = now
		a.InFlight++
		return p.lease(a, true), nil
	}
	return Lease{}, ErrNoAccount
}

func (p *pool) Record(ctx context.Context, l Lease, o Outcome) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	a := p.accounts[l.Account]
	if a == nil {
		return nil
	}
	if a.InFlight > 0 {
		a.InFlight--
	}
	if l.Version != a.Version {
		return nil
	}
	p.apply(a, o)
	return nil
}

func (p *pool) Pause(ctx context.Context, id AccountID, ifVersion StateVersion) error {
	return p.mutate(ctx, id, ifVersion, func(a *Account) {
		a.State = Paused
		a.Version++
	})
}

func (p *pool) Resume(ctx context.Context, id AccountID, ifVersion StateVersion) error {
	return p.mutate(ctx, id, ifVersion, func(a *Account) {
		a.State = Active
		a.CooldownUntil = time.Time{}
		a.SoftAvoidUntil = time.Time{}
		a.Version++
	})
}

func (p *pool) UpdatePriority(ctx context.Context, id AccountID, prio int, ifVersion StateVersion) error {
	return p.mutate(ctx, id, ifVersion, func(a *Account) {
		a.Priority = prio
		a.Version++
	})
}

func (p *pool) Snapshot() Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	accounts := make([]Account, 0, len(p.accounts))
	for _, a := range p.accounts {
		c := *a
		if a.Quota.Limit != nil {
			v := *a.Quota.Limit
			c.Quota.Limit = &v
		}
		accounts = append(accounts, c)
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].ID < accounts[j].ID })
	return Snapshot{Accounts: accounts}
}

func (p *pool) mutate(ctx context.Context, id AccountID, ifVersion StateVersion, fn func(*Account)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	a := p.accounts[id]
	if a == nil {
		return ErrNotFound
	}
	if a.Version != ifVersion {
		return ErrStale
	}
	fn(a)
	return nil
}

func (p *pool) apply(a *Account, o Outcome) {
	switch v := o.(type) {
	case TurnSucceeded:
		p.setActive(a)
	case AuthRejected:
		a.State = NeedsReauth
		a.CooldownUntil = time.Time{}
		a.SoftAvoidUntil = time.Time{}
		p.dropAffinity(a.ID)
		a.Version++
	case RateLimited:
		p.startCooldown(a, v.RetryAfter)
	case QuotaExhausted:
		p.startCooldown(a, v.RetryAfter)
	case NotFound:
	case RequestTimeout:
	case ServerError:
		p.softAvoid(a)
	case TransportFailure:
		p.softAvoid(a)
	case ProbeSucceeded:
		if a.State == CoolingDown || a.State == SoftAvoid {
			p.setActive(a)
		}
	case ProbeFailed:
	}
}

func (p *pool) setActive(a *Account) {
	if a.State == Active && a.CooldownUntil.IsZero() && a.SoftAvoidUntil.IsZero() {
		return
	}
	a.State = Active
	a.CooldownUntil = time.Time{}
	a.SoftAvoidUntil = time.Time{}
	a.Version++
}

func (p *pool) startCooldown(a *Account, retryAfter time.Duration) {
	until := p.now().Add(retryAfter)
	if a.State == CoolingDown && until.Before(a.CooldownUntil) {
		until = a.CooldownUntil
	}
	a.State = CoolingDown
	a.CooldownUntil = until
	a.SoftAvoidUntil = time.Time{}
	p.dropAffinity(a.ID)
	a.Version++
}

func (p *pool) softAvoid(a *Account) {
	until := p.now().Add(softAvoidWindow)
	if a.State == SoftAvoid && until.Before(a.SoftAvoidUntil) {
		until = a.SoftAvoidUntil
	}
	a.State = SoftAvoid
	a.CooldownUntil = time.Time{}
	a.SoftAvoidUntil = until
	a.Version++
}

func (p *pool) dropAffinity(id AccountID) {
	for k, e := range p.affinity {
		if e.Account == id {
			delete(p.affinity, k)
		}
	}
}

func (p *pool) lease(a *Account, probe bool) Lease {
	return Lease{
		Provider: a.Provider,
		Account:  a.ID,
		CredGen:  a.CredGen,
		Version:  a.Version,
		Probe:    probe,
	}
}

func (p *pool) usable(a *Account, now time.Time) bool {
	if a.State == Paused || a.State == NeedsReauth {
		return false
	}
	if a.State == CoolingDown && now.Before(a.CooldownUntil) {
		return false
	}
	if a.State == SoftAvoid && now.Before(a.SoftAvoidUntil) {
		return false
	}
	return true
}

func (p *pool) selectBest(now time.Time) *Account {
	var best *Account
	for _, a := range p.accounts {
		if !p.usable(a, now) {
			continue
		}
		if best == nil || p.better(a, best) {
			best = a
		}
	}
	return best
}

func (p *pool) selectProbe(now time.Time) *Account {
	var best *Account
	for _, a := range p.accounts {
		if a.State != CoolingDown && a.State != SoftAvoid {
			continue
		}
		if p.usable(a, now) {
			continue
		}
		if lp, ok := p.lastProbe[a.ID]; ok && now.Sub(lp) < probeInterval {
			continue
		}
		if best == nil || p.better(a, best) {
			best = a
		}
	}
	return best
}

func (p *pool) better(a, b *Account) bool {
	if a.Priority != b.Priority {
		return a.Priority > b.Priority
	}
	ah, bh := headroom(a), headroom(b)
	if ah != bh {
		return ah > bh
	}
	return a.ID < b.ID
}

func headroom(a *Account) int64 {
	if a.Quota.Limit == nil || *a.Quota.Limit == 0 {
		return 1<<63 - 1
	}
	h := *a.Quota.Limit - a.Quota.Used
	if h < 0 {
		return 0
	}
	return h
}

func (p *pool) sweepAffinity(now time.Time) {
	for k, e := range p.affinity {
		if now.Sub(e.LastAccess) >= affinityIdleTTL {
			delete(p.affinity, k)
		}
	}
	for len(p.affinity) >= affinityCapacity {
		var lruKey [32]byte
		var lru time.Time
		first := true
		for k, e := range p.affinity {
			if first || e.LastAccess.Before(lru) {
				lruKey = k
				lru = e.LastAccess
				first = false
			}
		}
		delete(p.affinity, lruKey)
	}
}

func (p *pool) affinityKey(req AcquireRequest) [32]byte {
	h := hmac.New(sha256.New, p.secret)
	writeField(h, string(req.Provider))
	writeField(h, string(req.QuotaGroup))
	writeField(h, string(req.Session))
	writeField(h, string(req.Thread))
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func writeField(h hash.Hash, s string) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(len(s)))
	h.Write(b[:])
	h.Write([]byte(s))
}
