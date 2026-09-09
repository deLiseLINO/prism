package routing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"prism/internal/account"
	"prism/internal/canon"
	"prism/internal/execution"
	"prism/internal/provider"
)

const defaultMaxAccountFailovers = 3

type TurnPolicy struct {
	MaxAccountFailovers int
	MaxTargetFailovers  int
}

func (p TurnPolicy) withDefaults(targets int) TurnPolicy {
	accountFailovers := p.MaxAccountFailovers
	if accountFailovers <= 0 {
		accountFailovers = defaultMaxAccountFailovers
	}
	targetFailovers := p.MaxTargetFailovers
	if targetFailovers <= 0 {
		targetFailovers = targets - 1
	}
	if targetFailovers < 0 {
		targetFailovers = 0
	}
	return TurnPolicy{MaxAccountFailovers: accountFailovers, MaxTargetFailovers: targetFailovers}
}

type Plan struct {
	Targets []provider.Target
	Policy  TurnPolicy
}

type ResponseLifecycle interface {
	CommitState() provider.CommitState
}

type Terminal interface{ terminal() }

type Finished struct{ Event canon.TurnFinished }
type Failed struct{ Event canon.TurnFailed }

func (Finished) terminal() {}
func (Failed) terminal()   {}

type TurnResult struct {
	Terminal Terminal
	Attempts int
}

type Runners interface {
	Lookup(id account.ProviderID) (provider.Runner, bool)
}

type Planner interface {
	Plan(model canon.ModelID) (Plan, bool)
}

type Router interface {
	Turn(ctx context.Context, req canon.Request, f execution.Facts, lifecycle ResponseLifecycle, sink provider.Sink) TurnResult
}

func NewRouter(pool account.Pool, runners Runners, planner Planner, group account.QuotaGroup) Router {
	return &router{pool: pool, runners: runners, planner: planner, group: group}
}

type router struct {
	pool    account.Pool
	runners Runners
	planner Planner
	group   account.QuotaGroup
}

type captureSink struct {
	next        provider.Sink
	finished    canon.TurnFinished
	failed      canon.TurnFailed
	hasFinished bool
	hasFailed   bool
}

func (s *captureSink) Emit(ev canon.Event) error {
	switch e := ev.(type) {
	case canon.TurnFinished:
		s.finished, s.hasFinished = e, true
	case canon.TurnFailed:
		s.failed, s.hasFailed = e, true
	}
	return s.next.Emit(ev)
}

func (r *router) Turn(ctx context.Context, req canon.Request, f execution.Facts, lifecycle ResponseLifecycle, sink provider.Sink) TurnResult {
	plan, ok := r.planner.Plan(req.Model)
	if !ok {
		return turnFailed(canon.Failure{Reason: canon.FailNotFound, Message: fmt.Sprintf("routing: no plan for model %q", req.Model)}, 0)
	}
	if len(plan.Targets) == 0 {
		return turnFailed(canon.Failure{Reason: canon.FailUnknown, Message: "routing: empty plan"}, 0)
	}
	policy := plan.Policy.withDefaults(len(plan.Targets))
	capture := &captureSink{next: sink}
	attempts := 0
	accountSwitches := 0
	targetSwitches := 0
	exhausted := func() TurnResult {
		return turnFailed(canon.Failure{Reason: canon.FailQuotaExhausted, Message: "routing: failover budget exhausted"}, attempts)
	}
	for ti := range plan.Targets {
		target := plan.Targets[ti]
		runner, ok := r.runners.Lookup(target.Provider)
		if !ok {
			return turnFailed(canon.Failure{Reason: canon.FailUnknown, Message: fmt.Sprintf("routing: no runner registered for provider %q", target.Provider)}, attempts)
		}
		if canon.HasImage(req) && !target.ImageInput {
			return turnFailed(imageUnsupportedFailure(target).Failure, attempts)
		}
		budget := target.MaxFailovers
		if budget <= 0 {
			budget = policy.MaxAccountFailovers
		}
		if target.Policy.AutoSwitch == account.AutoSwitchOff {
			budget = 1
		}
		for attempt := 0; attempt < budget; attempt++ {
			lease, err := r.pool.Acquire(ctx, account.AcquireRequest{
				Provider:   target.Provider,
				Model:      target.Model,
				QuotaGroup: r.group,
				Session:    f.Session,
				Thread:     f.Thread,
				Policy:     target.Policy,
			})
			if err != nil {
				if errors.Is(err, account.ErrNoAccount) {
					return turnFailed(canon.Failure{Reason: canon.FailQuotaExhausted, Message: "routing: pool exhausted"}, attempts)
				}
				return turnFailed(canon.Failure{Reason: canon.FailUnknown, Message: fmt.Sprintf("routing: acquire failed: %v", err)}, attempts)
			}
			attempts++
			wireReq := req
			wireReq.Model = target.Model
			err = runner.Run(ctx, provider.RunRequest{Request: wireReq, Target: target, Lease: lease, Facts: f}, capture)
			if err == nil {
				if !capture.hasFinished {
					_ = r.pool.Record(ctx, lease, account.RequestRejected{})
					return turnFailed(canon.Failure{Reason: canon.FailUnknown, Message: "routing: runner returned without terminal event"}, attempts)
				}
				_ = r.pool.Record(ctx, lease, account.TurnSucceeded{Usage: capture.finished.Usage})
				return TurnResult{Terminal: Finished{Event: capture.finished}, Attempts: attempts}
			}
			if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return turnFailed(canon.Failure{Reason: canon.FailClientClosed, Message: "routing: client closed the request"}, attempts)
			}
			re, typed := runErrorOf(err)
			if !typed {
				_ = r.pool.Record(ctx, lease, account.RequestRejected{})
				return turnFailed(canon.Failure{Reason: canon.FailUnknown, Message: fmt.Sprintf("routing: untyped runner error: %v", err)}, attempts)
			}
			if o, mappable := outcomeFor(re, target.Policy); mappable {
				_ = r.pool.Record(ctx, lease, o)
			} else {
				_ = r.pool.Record(ctx, lease, account.RequestRejected{})
			}
			if lifecycle.CommitState() >= provider.OutputCommitted || !re.Kind.FailoverAllowed() || !re.Class.FailoverAllowed() {
				return runErrorTerminal(re, capture, attempts)
			}
			if attempt+1 < budget && accountSwitches < policy.MaxAccountFailovers {
				accountSwitches++
				continue
			}
			break
		}
		if ti+1 < len(plan.Targets) && targetSwitches < policy.MaxTargetFailovers {
			targetSwitches++
			continue
		}
		return exhausted()
	}
	return exhausted()
}

func runErrorOf(err error) (provider.RunError, bool) {
	var re provider.RunError
	if errors.As(err, &re) {
		return re, true
	}
	return provider.RunError{}, false
}

func outcomeFor(re provider.RunError, policy account.SelectionPolicy) (account.Outcome, bool) {
	cooldown := func(retryAfter time.Duration) time.Duration {
		d := policy.CooldownDefault
		if retryAfter > 0 {
			d = retryAfter
		}
		if policy.CooldownMax > 0 && d > policy.CooldownMax {
			return policy.CooldownMax
		}
		return d
	}
	switch re.Class {
	case provider.ClassUnauthorized:
		return account.AuthRejected{}, true
	case provider.ClassRateLimited:
		return account.RateLimited{RetryAfter: cooldown(re.RetryAfter)}, true
	case provider.ClassQuotaExhausted:
		return account.QuotaExhausted{RetryAfter: cooldown(re.RetryAfter)}, true
	case provider.ClassNotFound:
		return account.NotFound{}, true
	case provider.ClassTimeout:
		return account.RequestTimeout{RetryAfter: re.RetryAfter}, true
	case provider.ClassServer:
		return account.ServerError{}, true
	case provider.ClassTransport:
		return account.TransportFailure{}, true
	default:
		return nil, false
	}
}

func failureReason(c provider.ErrorClass) canon.FailureReason {
	switch c {
	case provider.ClassUnauthorized:
		return canon.FailUnauthorized
	case provider.ClassRateLimited:
		return canon.FailRateLimited
	case provider.ClassQuotaExhausted:
		return canon.FailQuotaExhausted
	case provider.ClassNotFound:
		return canon.FailNotFound
	case provider.ClassTimeout:
		return canon.FailTimeout
	case provider.ClassServer:
		return canon.FailServerOverloaded
	case provider.ClassTransport:
		return canon.FailUpstreamTransport
	case provider.ClassInvalidRequest:
		return canon.FailInvalidRequest
	case provider.ClassContextLength:
		return canon.FailContextLength
	default:
		return canon.FailUnknown
	}
}

func runErrorTerminal(re provider.RunError, capture *captureSink, attempts int) TurnResult {
	if capture.hasFailed {
		return TurnResult{Terminal: Failed{Event: capture.failed}, Attempts: attempts}
	}
	return turnFailed(canon.Failure{Reason: failureReason(re.Class), Message: re.Error()}, attempts)
}

func turnFailed(f canon.Failure, attempts int) TurnResult {
	return TurnResult{Terminal: Failed{Event: canon.TurnFailed{Failure: f}}, Attempts: attempts}
}
