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

	"prism/internal/canon"
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

type Strategy uint8

const (
	StrategyQuota Strategy = iota
	StrategyRoundRobin
	StrategyFillFirst
)

type AffinityMode uint8

const (
	AffinitySticky AffinityMode = iota
	AffinityOff
)

type AutoSwitch uint8

const (
	AutoSwitchOn AutoSwitch = iota
	AutoSwitchOff
)

type SelectionPolicy struct {
	Strategy            Strategy
	AutoSwitch          AutoSwitch
	AutoSwitchThreshold float64
	Affinity            AffinityMode
	PinnedAccount       AccountID
	CooldownDefault     time.Duration
	CooldownMax         time.Duration
}

type AcquireRequest struct {
	Provider   ProviderID
	Model      canon.ModelID
	QuotaGroup QuotaGroup
	Session    execution.SessionKey
	Thread     execution.ThreadKey
	Policy     SelectionPolicy
}

type Outcome interface{ outcome() }

type TurnSucceeded struct{ Usage canon.Usage }

type AuthRejected struct{}

type RateLimited struct{ RetryAfter time.Duration }

type QuotaExhausted struct{ RetryAfter time.Duration }

type NotFound struct{}

type RequestTimeout struct{ RetryAfter time.Duration }

type ServerError struct{}

type TransportFailure struct{}

type RequestRejected struct{}

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
func (RequestRejected) outcome()  {}
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

var (
	// ErrNeedsReauth reports that the provider rejected the stored grant
	// (revoked, expired, or invalidated). The account must re-login.
	ErrNeedsReauth = errors.New("account: credential rejected by provider")
	// ErrRefreshTransient reports a transient credential-refresh failure
	// (network, timeout, or provider 5xx/429). The prior credential
	// generation remains valid and untouched.
	ErrRefreshTransient = errors.New("account: credential refresh failed transiently")
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
	rr        map[ProviderID]uint64
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
		rr:        make(map[ProviderID]uint64),
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
	if a := p.pinned(req, now); a != nil {
		if req.Policy.Affinity == AffinitySticky {
			p.sweepAffinity(now)
			p.affinity[p.affinityKey(req)] = affinityEntry{Account: a.ID, LastAccess: now}
		}
		a.InFlight++
		return p.lease(a, false), nil
	}
	key := p.affinityKey(req)
	if req.Policy.Affinity == AffinitySticky {
		if e, ok := p.affinity[key]; ok {
			if now.Sub(e.LastAccess) >= affinityIdleTTL {
				delete(p.affinity, key)
			} else if a := p.accounts[e.Account]; a != nil && a.Provider == req.Provider && p.eligible(a, req, now) {
				e.LastAccess = now
				p.affinity[key] = e
				a.InFlight++
				return p.lease(a, false), nil
			} else {
				delete(p.affinity, key)
			}
		}
	}
	if a := p.selectBest(req, now); a != nil {
		if req.Policy.Affinity == AffinitySticky {
			p.sweepAffinity(now)
			p.affinity[key] = affinityEntry{Account: a.ID, LastAccess: now}
		}
		a.InFlight++
		return p.lease(a, false), nil
	}
	if a := p.selectProbe(req, now); a != nil {
		p.lastProbe[a.ID] = now
		a.InFlight++
		return p.lease(a, true), nil
	}
	return Lease{}, ErrNoAccount
}

func (p *pool) Record(_ context.Context, l Lease, o Outcome) error {
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

// AdvanceGeneration moves the runtime credential generation forward to gen.
// Accounts absent from the pool are ignored and a pool already at or beyond
// gen is left alone, so a re-login that registered a newer generation never
// regresses. A pool behind the repository-referenced generation (a crash
// between the repository write and this update) is aligned to gen.
func (p *pool) AdvanceGeneration(id AccountID, gen CredentialGeneration) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	a := p.accounts[id]
	if a == nil || a.CredGen >= gen {
		return nil
	}
	a.CredGen = gen
	return nil
}

// MarkNeedsReauth moves the account to NeedsReauth and drops its affinity
// entries, mirroring the AuthRejected outcome without requiring a lease.
func (p *pool) MarkNeedsReauth(id AccountID) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	a := p.accounts[id]
	if a == nil {
		return nil
	}
	a.State = NeedsReauth
	a.CooldownUntil = time.Time{}
	a.SoftAvoidUntil = time.Time{}
	p.dropAffinity(a.ID)
	a.Version++
	return nil
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

// DeleteAccount removes the account and every pool-owned index entry that
// points at it. A repeated deletion reports ErrNotFound.
func (p *pool) DeleteAccount(ctx context.Context, id AccountID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.accounts[id] == nil {
		return ErrNotFound
	}
	delete(p.accounts, id)
	p.dropAffinity(id)
	delete(p.lastProbe, id)
	return nil
}

// UpdateQuota stores a quota snapshot for an account, deep-copying
// pointer-backed limit data. It never bumps the state version.
func (p *pool) UpdateQuota(id AccountID, snap quota.Snapshot) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	a := p.accounts[id]
	if a == nil {
		return ErrNotFound
	}
	stored := snap
	if snap.Limit != nil {
		v := *snap.Limit
		stored.Limit = &v
	}
	a.Quota = stored
	return nil
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

func (p *pool) eligible(a *Account, req AcquireRequest, now time.Time) bool {
	return p.usable(a, now) && !p.overThreshold(a, req.Policy.AutoSwitchThreshold)
}

func (p *pool) overThreshold(a *Account, threshold float64) bool {
	if threshold <= 0 || a.Quota.Limit == nil || *a.Quota.Limit <= 0 {
		return false
	}
	return float64(a.Quota.Used)/float64(*a.Quota.Limit) >= threshold
}

func (p *pool) pinned(req AcquireRequest, now time.Time) *Account {
	if req.Policy.PinnedAccount == "" {
		return nil
	}
	a := p.accounts[req.Policy.PinnedAccount]
	if a == nil || a.Provider != req.Provider || !p.eligible(a, req, now) {
		return nil
	}
	return a
}

func (p *pool) selectBest(req AcquireRequest, now time.Time) *Account {
	var candidates []*Account
	for _, a := range p.accounts {
		if a.Provider != req.Provider || !p.eligible(a, req, now) {
			continue
		}
		candidates = append(candidates, a)
	}
	if len(candidates) == 0 {
		return nil
	}
	switch req.Policy.Strategy {
	case StrategyRoundRobin:
		return p.roundRobin(req.Provider, candidates)
	case StrategyFillFirst:
		best := candidates[0]
		for _, a := range candidates[1:] {
			if fillFirstBefore(a, best) {
				best = a
			}
		}
		return best
	default:
		best := candidates[0]
		for _, a := range candidates[1:] {
			if p.better(a, best) {
				best = a
			}
		}
		return best
	}
}

func (p *pool) roundRobin(providerID ProviderID, candidates []*Account) *Account {
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Priority != candidates[j].Priority {
			return candidates[i].Priority > candidates[j].Priority
		}
		return candidates[i].ID < candidates[j].ID
	})
	tier := candidates[0].Priority
	n := 0
	for n < len(candidates) && candidates[n].Priority == tier {
		n++
	}
	i := p.rr[providerID] % uint64(n)
	p.rr[providerID]++
	return candidates[i]
}

func (p *pool) selectProbe(req AcquireRequest, now time.Time) *Account {
	var best *Account
	for _, a := range p.accounts {
		if a.Provider != req.Provider {
			continue
		}
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

func fillFirstBefore(a, b *Account) bool {
	if a.Priority != b.Priority {
		return a.Priority > b.Priority
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
