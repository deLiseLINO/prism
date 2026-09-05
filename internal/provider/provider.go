package provider

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"prism/internal/account"
	"prism/internal/canon"
	"prism/internal/execution"
	"prism/internal/quota"
)

type Wire uint8

const (
	WireCodex Wire = iota + 1
	WireAntigravity
	WireResponses
	WireMessages
	WireChat
)

type Target struct {
	Provider     account.ProviderID
	Wire         Wire
	BaseURL      string
	APIKeyRef    string
	Model        canon.ModelID
	Timeout      time.Duration
	MaxFailovers int
	Policy       account.SelectionPolicy
}

type CommitState uint8

const (
	NotStarted CommitState = iota
	ResponseStarted
	OutputCommitted
)

type RunErrorKind uint8

const (
	TerminalEmitted RunErrorKind = iota + 1
	TerminalOmitted
	Retryable
	UnsafeReplay
)

type ErrorClass uint8

const (
	ClassUnauthorized ErrorClass = iota + 1
	ClassRateLimited
	ClassQuotaExhausted
	ClassNotFound
	ClassTimeout
	ClassServer
	ClassTransport
	ClassInvalidRequest
	ClassContextLength
)

type RunError struct {
	Kind       RunErrorKind
	Class      ErrorClass
	Accepted   bool
	ReplaySafe bool
	RetryAfter time.Duration
	Cause      error
}

func (e RunError) Error() string {
	return fmt.Sprintf("provider: run error kind=%d class=%d accepted=%t replaySafe=%t retryAfter=%s", e.Kind, e.Class, e.Accepted, e.ReplaySafe, e.RetryAfter)
}

func (e RunError) Unwrap() error {
	return e.Cause
}

// CredentialRunError maps a credential-source failure to a RunError. A
// provider-rejected grant fails over as unauthorized; transient refresh
// failures stay retryable; unknown sources of failure remain retryable
// transport-class so a store hiccup never marks an account for re-auth.
func CredentialRunError(err error) RunError {
	switch {
	case errors.Is(err, account.ErrNeedsReauth):
		return RunError{Kind: TerminalOmitted, Class: ClassUnauthorized, ReplaySafe: true, Cause: err}
	case errors.Is(err, account.ErrRefreshTransient):
		return RunError{Kind: Retryable, Class: ClassTransport, ReplaySafe: true, Cause: err}
	default:
		return RunError{Kind: Retryable, Class: ClassTransport, ReplaySafe: true, Cause: err}
	}
}

func (k RunErrorKind) FailoverAllowed() bool {
	switch k {
	case TerminalEmitted, UnsafeReplay:
		return false
	case TerminalOmitted, Retryable:
		return true
	default:
		return false
	}
}

func (c ErrorClass) FailoverAllowed() bool {
	switch c {
	case ClassInvalidRequest, ClassContextLength:
		return false
	default:
		return true
	}
}

type Sink interface {
	Emit(canon.Event) error
}

type RunRequest struct {
	Request canon.Request
	Target  Target
	Lease   account.Lease
	Facts   execution.Facts
}

type Runner interface {
	Run(ctx context.Context, req RunRequest, sink Sink) error
}

type ModelCaps struct {
	Reasoning     bool
	CustomTools   bool
	LocalShell    bool
	ToolSearch    bool
	Compaction    bool
	CountTokens   bool
	ParallelTools bool
}

type Model struct {
	ID    canon.ModelID
	Alias string
	Caps  ModelCaps
}

type Catalog interface {
	Models(ctx context.Context) ([]Model, error)
}

type QuotaSource interface {
	Quota(ctx context.Context, id account.AccountID) (quota.Snapshot, error)
}

type CompactRequest struct {
	Target Target
	Lease  account.Lease
	Facts  execution.Facts
	Input  []canon.Item
	Kept   []canon.ItemID
	Budget int
}

type CompactResult struct {
	Summary canon.Message
	Usage   canon.Usage
}

type Compactor interface {
	Compact(ctx context.Context, req CompactRequest) (CompactResult, error)
}

type CountTokensRequest struct {
	Target       Target
	Lease        account.Lease
	Facts        execution.Facts
	Instructions []canon.Content
	Input        []canon.Item
	Tools        []canon.Tool
}

type TokenCount struct{ InputTokens int64 }

type TokenCounter interface {
	CountTokens(ctx context.Context, req CountTokensRequest) (TokenCount, error)
}

type Registry struct {
	mu      sync.RWMutex
	runners map[account.ProviderID]Runner
}

func NewRegistry() *Registry {
	return &Registry{runners: make(map[account.ProviderID]Runner)}
}

var ErrDuplicateProvider = errors.New("provider: duplicate registration")

func (r *Registry) Register(id account.ProviderID, runner Runner) error {
	if id == "" {
		return errors.New("provider: empty provider id")
	}
	if runner == nil {
		return errors.New("provider: nil runner")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.runners[id]; ok {
		return ErrDuplicateProvider
	}
	r.runners[id] = runner
	return nil
}

func (r *Registry) Lookup(id account.ProviderID) (Runner, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	runner, ok := r.runners[id]
	return runner, ok
}
