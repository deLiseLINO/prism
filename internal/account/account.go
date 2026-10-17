package account

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/execution"
	"github.com/deLiseLINO/prism/internal/quota"
)

type ProviderID string

type ProviderKind uint8

const (
	KindCodex ProviderKind = iota + 1
	KindAntigravity
	KindOpenAIResponses
	KindAnthropic
	KindCline
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
	ID               AccountID
	Provider         ProviderID
	Priority         int
	State            State
	Email            string
	CredGen          CredentialGeneration
	Version          StateVersion
	Quota            quota.Snapshot
	CooldownUntil    time.Time
	SoftAvoidUntil   time.Time
	InFlight         int
	rejectedGen      CredentialGeneration
	rejectedFailure  *canon.Failure
	reauthTransition reauthTransition
}

type Lease struct {
	Provider ProviderID
	Account  AccountID
	CredGen  CredentialGeneration
	Version  StateVersion
	Probe    bool
}

type reauthTransition struct {
	before Lease
	after  StateVersion
}

type QuotaGroup string

type SelectionPolicy struct {
	PinnedAccount AccountID
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

type AuthRejected struct{ Failure canon.Failure }

type RequestRejected struct{}

func (TurnSucceeded) outcome()   {}
func (AuthRejected) outcome()    {}
func (RequestRejected) outcome() {}

type Snapshot struct {
	Accounts []Account
}

type Pool interface {
	Acquire(ctx context.Context, req AcquireRequest) (Lease, error)
	Record(ctx context.Context, l Lease, o Outcome) error
	Pause(ctx context.Context, id AccountID, ifVersion StateVersion) error
	Resume(ctx context.Context, id AccountID, ifVersion StateVersion) error
	UpdatePriority(ctx context.Context, id AccountID, p int, ifVersion StateVersion) error
	DeleteAccount(ctx context.Context, id AccountID) error
	Snapshot() Snapshot
}

var (
	ErrStale     = errors.New("account: stale state version")
	ErrNotFound  = errors.New("account: account not found")
	ErrNoAccount = errors.New("account: no usable account")
)

type NoAccountReason uint8

const (
	MissingAccount NoAccountReason = iota + 1
	WrongProvider
	SelectionRequired
	AccountPaused
	AccountNeedsReauth
)

type AcquireError struct {
	Reason  NoAccountReason
	Failure *canon.Failure
}

func (e *AcquireError) Error() string {
	switch e.Reason {
	case MissingAccount:
		return "account: account not found"
	case WrongProvider:
		return "account: selected account belongs to a different provider"
	case SelectionRequired:
		return "account: select an account for this provider"
	case AccountPaused:
		return "account: selected account is paused"
	case AccountNeedsReauth:
		return "account: selected account needs re-login"
	default:
		return ErrNoAccount.Error()
	}
}

func (e *AcquireError) Is(target error) bool {
	return target == ErrNoAccount
}

var (
	// ErrNeedsReauth reports that the provider rejected the stored grant
	// (revoked, expired, or invalidated). The account must re-login.
	ErrNeedsReauth = errors.New("account: credential rejected by provider")
	// ErrRefreshTransient reports a transient credential-refresh failure
	// (network, timeout, or provider 5xx/429). The prior credential
	// generation remains valid and untouched.
	ErrRefreshTransient = errors.New("account: credential refresh failed transiently")
)

type pool struct {
	mu           sync.Mutex
	accounts     map[AccountID]*Account
	policyWriter func(Account) error
}

func New() *pool {
	return &pool{accounts: make(map[AccountID]*Account)}
}

func (p *pool) SetPolicyWriter(fn func(Account) error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.policyWriter = fn
}

func (p *pool) Register(a Account) {
	p.mu.Lock()
	defer p.mu.Unlock()
	a.rejectedGen = 0
	a.rejectedFailure = nil
	a.reauthTransition = reauthTransition{}
	if prev, ok := p.accounts[a.ID]; ok {
		if a.CredGen < prev.CredGen {
			return
		}
		if prev.Provider == a.Provider && prev.rejectedGen == a.CredGen {
			a.rejectedGen = prev.rejectedGen
			a.rejectedFailure = prev.rejectedFailure
		}
		if prev.Provider == a.Provider && prev.State == NeedsReauth && prev.rejectedGen == a.CredGen {
			a.State = NeedsReauth
			a.rejectedGen = prev.rejectedGen
		}
		a.InFlight = prev.InFlight
		a.Priority = prev.Priority
		a.Version = prev.Version
		if prev.State == Paused || a.State == Paused {
			a.State = prev.State
		}
	}
	if a.State != NeedsReauth && a.State != Paused {
		a.rejectedGen = 0
		a.rejectedFailure = nil
	} else if a.State == NeedsReauth && a.rejectedGen == 0 {
		a.rejectedGen = a.CredGen
	}
	p.accounts[a.ID] = &a
}

func (p *pool) Acquire(ctx context.Context, req AcquireRequest) (Lease, error) {
	if err := ctx.Err(); err != nil {
		return Lease{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var chosen *Account
	if req.Policy.PinnedAccount != "" {
		chosen = p.accounts[req.Policy.PinnedAccount]
		if chosen == nil {
			return Lease{}, &AcquireError{Reason: MissingAccount}
		}
		if chosen.Provider != req.Provider {
			return Lease{}, &AcquireError{Reason: WrongProvider}
		}
	} else {
		for _, a := range p.accounts {
			if a.Provider != req.Provider {
				continue
			}
			if chosen != nil {
				return Lease{}, &AcquireError{Reason: SelectionRequired}
			}
			chosen = a
		}
	}
	if chosen == nil {
		return Lease{}, &AcquireError{Reason: MissingAccount}
	}
	if chosen.State == Paused {
		return Lease{}, &AcquireError{Reason: AccountPaused}
	}
	if chosen.State == NeedsReauth {
		err := &AcquireError{Reason: AccountNeedsReauth}
		if chosen.rejectedGen == chosen.CredGen {
			err.Failure = copyFailure(chosen.rejectedFailure)
		}
		return Lease{}, err
	}
	chosen.InFlight++
	return Lease{Provider: chosen.Provider, Account: chosen.ID, CredGen: chosen.CredGen, Version: chosen.Version}, nil
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
	if l.CredGen != a.CredGen || l.Provider != a.Provider {
		return nil
	}
	if l.Version != a.Version {
		transition := a.reauthTransition
		rejected, ok := o.(AuthRejected)
		if ok && (rejected.Failure.Reason != 0 || rejected.Failure.Message != "" || rejected.Failure.Provider != nil) &&
			a.State == NeedsReauth && transition.before.CredGen != 0 &&
			transition.before.Provider == l.Provider && transition.before.Account == l.Account &&
			transition.before.CredGen == l.CredGen && transition.before.Version == l.Version &&
			transition.after == a.Version {
			a.rejectedFailure = copyFailure(&rejected.Failure)
			a.reauthTransition = reauthTransition{}
		}
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
		a.rejectedGen = 0
		a.rejectedFailure = nil
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
	a.rejectedGen = 0
	a.rejectedFailure = nil
	a.reauthTransition = reauthTransition{}
	return nil
}

func (p *pool) PublishCredential(id AccountID, gen CredentialGeneration, changed bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	a := p.accounts[id]
	if a == nil || gen < a.CredGen {
		return nil
	}
	next := *a
	if next.CredGen != gen || changed {
		next.CredGen = gen
		next.Version++
		next.rejectedGen = 0
		next.rejectedFailure = nil
		next.reauthTransition = reauthTransition{}
	}
	if changed && next.State == NeedsReauth {
		next.State = Active
		next.rejectedGen = 0
		next.CooldownUntil = time.Time{}
		next.SoftAvoidUntil = time.Time{}
		next.Version++
		if p.policyWriter != nil {
			if err := p.policyWriter(next); err != nil {
				return err
			}
		}
	}
	*a = next
	return nil
}

// MarkNeedsReauth fences grant invalidation to its lease and permits that
// lease's later rejection report to attach evidence without another transition.
func (p *pool) MarkNeedsReauth(l Lease) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	a := p.accounts[l.Account]
	if a == nil || a.State == Paused || a.State == NeedsReauth || l.CredGen == 0 ||
		l.Provider != a.Provider || l.CredGen != a.CredGen || l.Version != a.Version {
		return nil
	}
	a.State = NeedsReauth
	if a.rejectedGen != a.CredGen {
		a.rejectedFailure = nil
	}
	a.rejectedGen = a.CredGen
	a.CooldownUntil = time.Time{}
	a.SoftAvoidUntil = time.Time{}
	a.Version++
	a.reauthTransition = reauthTransition{before: l, after: a.Version}
	return nil
}

func (p *pool) Snapshot() Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	accounts := make([]Account, 0, len(p.accounts))
	for _, a := range p.accounts {
		c := *a
		c.rejectedFailure = nil
		c.reauthTransition = reauthTransition{}
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
	next := *a
	next.reauthTransition = reauthTransition{}
	fn(&next)
	if p.policyWriter != nil {
		if err := p.policyWriter(next); err != nil {
			return err
		}
	}
	*a = next
	return nil
}

func (p *pool) apply(a *Account, o Outcome) {
	a.reauthTransition = reauthTransition{}
	switch o := o.(type) {
	case AuthRejected:
		if a.State == Paused {
			return
		}
		a.State = NeedsReauth
		a.rejectedGen = a.CredGen
		a.rejectedFailure = nil
		if o.Failure.Reason != 0 || o.Failure.Message != "" || o.Failure.Provider != nil {
			a.rejectedFailure = copyFailure(&o.Failure)
		}
		a.CooldownUntil = time.Time{}
		a.SoftAvoidUntil = time.Time{}
		a.Version++
	case TurnSucceeded:
		if a.State == CoolingDown || a.State == SoftAvoid {
			a.State = Active
			a.CooldownUntil = time.Time{}
			a.SoftAvoidUntil = time.Time{}
			a.Version++
		}
	}
}

func copyFailure(f *canon.Failure) *canon.Failure {
	if f == nil {
		return nil
	}
	c := *f
	if f.Provider != nil {
		provider := *f.Provider
		provider.Error = append([]byte(nil), provider.Error...)
		provider.StatusDetails = append([]byte(nil), provider.StatusDetails...)
		c.Provider = &provider
	}
	return &c
}
