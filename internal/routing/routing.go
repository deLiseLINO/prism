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
	"prism/internal/requestlog"
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

type AttemptTrace struct {
	Provider account.ProviderID
	Model    canon.ModelID
	Outcome  string
	Usage    canon.Usage
}

type TurnResult struct {
	Terminal Terminal
	Attempts int
	Trace    []AttemptTrace
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

func NewRouter(pool account.Pool, runners Runners, planner Planner, group account.QuotaGroup, log *requestlog.Journal) Router {
	return &router{pool: pool, runners: runners, planner: planner, group: group, log: log}
}

type router struct {
	pool    account.Pool
	runners Runners
	planner Planner
	group   account.QuotaGroup
	log     *requestlog.Journal
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
	j := r.log.Open(f, req.Model)
	res := r.turn(ctx, req, f, lifecycle, sink, j)
	j.Close(terminalOf(res))
	return res
}

func (r *router) turn(ctx context.Context, req canon.Request, f execution.Facts, lifecycle ResponseLifecycle, sink provider.Sink, j *requestlog.Turn) TurnResult {
	plan, ok := r.planner.Plan(req.Model)
	if !ok {
		return turnFailed(canon.Failure{Reason: canon.FailNotFound, Message: fmt.Sprintf("routing: no plan for model %q", req.Model)}, 0, nil)
	}
	if len(plan.Targets) == 0 {
		return turnFailed(canon.Failure{Reason: canon.FailUnknown, Message: "routing: empty plan"}, 0, nil)
	}
	policy := plan.Policy.withDefaults(len(plan.Targets))
	capture := &captureSink{next: sink}
	attempts := 0
	var trace []AttemptTrace
	accountSwitches := 0
	targetSwitches := 0
	fail := func(f canon.Failure) TurnResult {
		return turnFailed(f, attempts, trace)
	}
	exhausted := func() TurnResult {
		return fail(canon.Failure{Reason: canon.FailQuotaExhausted, Message: "routing: failover budget exhausted"})
	}
	for ti := range plan.Targets {
		target := plan.Targets[ti]
		runner, ok := r.runners.Lookup(target.Provider)
		if !ok {
			return fail(canon.Failure{Reason: canon.FailUnknown, Message: fmt.Sprintf("routing: no runner registered for provider %q", target.Provider)})
		}
		if canon.HasImage(req) && !target.ImageInput {
			return fail(imageUnsupportedFailure(target).Failure)
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
					return fail(canon.Failure{Reason: canon.FailQuotaExhausted, Message: "routing: pool exhausted"})
				}
				return fail(canon.Failure{Reason: canon.FailUnknown, Message: fmt.Sprintf("routing: acquire failed: %v", err)})
			}
			attempts++
			trace = append(trace, AttemptTrace{Provider: target.Provider, Model: target.Model, Outcome: "failed"})
			wireReq := req
			wireReq.Model = target.Model
			started := j.Now()
			err = runner.Run(ctx, provider.RunRequest{Request: wireReq, Target: target, Lease: lease, Facts: f}, capture)
			logAttempt := func(outcome requestlog.Outcome, msg string) {
				j.Attempt(requestlog.AttemptInfo{
					Provider:  target.Provider,
					AccountID: lease.Account,
					Model:     target.Model,
					StartedAt: started,
					Outcome:   outcome,
					Error:     msg,
				})
			}
			if err == nil {
				if !capture.hasFinished {
					logAttempt(requestlog.AttemptNoTerminal, "routing: runner returned without terminal event")
					_ = r.pool.Record(ctx, lease, account.RequestRejected{})
					return fail(canon.Failure{Reason: canon.FailUnknown, Message: "routing: runner returned without terminal event"})
				}
				logAttempt(requestlog.AttemptSucceeded, "")
				_ = r.pool.Record(ctx, lease, account.TurnSucceeded{Usage: capture.finished.Usage})
				trace[attempts-1] = AttemptTrace{Provider: target.Provider, Model: target.Model, Outcome: "completed", Usage: capture.finished.Usage}
				return TurnResult{Terminal: Finished{Event: capture.finished}, Attempts: attempts, Trace: trace}
			}
			if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				logAttempt(requestlog.AttemptClientClosed, "routing: client closed the request")
				return fail(canon.Failure{Reason: canon.FailClientClosed, Message: "routing: client closed the request"})
			}
			re, typed := runErrorOf(err)
			if !typed {
				logAttempt(requestlog.AttemptRejected, err.Error())
				_ = r.pool.Record(ctx, lease, account.RequestRejected{})
				return fail(canon.Failure{Reason: canon.FailUnknown, Message: fmt.Sprintf("routing: untyped runner error: %v", err)})
			}
			logAttempt(attemptOutcome(re), runErrorMessage(re))
			if o, mappable := outcomeFor(re, target.Policy); mappable {
				_ = r.pool.Record(ctx, lease, o)
			} else {
				_ = r.pool.Record(ctx, lease, account.RequestRejected{})
			}
			if lifecycle.CommitState() >= provider.OutputCommitted || !re.Kind.FailoverAllowed() || !re.Class.FailoverAllowed() {
				if capture.hasFailed {
					trace[attempts-1].Usage = capture.failed.Usage
				}
				return runErrorTerminal(re, capture, attempts, trace)
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
	var ptr *provider.RunError
	if errors.As(err, &ptr) && ptr != nil {
		return *ptr, true
	}
	return provider.RunError{}, false
}

func runErrorMessage(re provider.RunError) string {
	if re.Cause != nil {
		return re.Cause.Error()
	}
	return re.Error()
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
	case provider.ClassForbidden:
		return nil, false
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
	case provider.ClassForbidden:
		return canon.FailForbidden
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

func terminalOf(res TurnResult) requestlog.Terminal {
	switch term := res.Terminal.(type) {
	case Finished:
		if reason, incomplete := term.Event.Status.Reason(); incomplete {
			return requestlog.Terminal{Status: requestlog.StatusIncomplete, Incomplete: reason, Usage: term.Event.Usage}
		}
		return requestlog.Terminal{Status: requestlog.StatusCompleted, Usage: term.Event.Usage}
	case Failed:
		return requestlog.Terminal{
			Status: requestlog.StatusFailed,
			Failed: true,
			Reason: term.Event.Failure.Reason,
		}
	default:
		return requestlog.Terminal{Status: requestlog.StatusIncomplete}
	}
}

func attemptOutcome(re provider.RunError) requestlog.Outcome {
	switch re.Class {
	case provider.ClassUnauthorized:
		return requestlog.AttemptUnauthorized
	case provider.ClassForbidden:
		return requestlog.AttemptForbidden
	case provider.ClassRateLimited:
		return requestlog.AttemptRateLimited
	case provider.ClassQuotaExhausted:
		return requestlog.AttemptQuotaExhausted
	case provider.ClassNotFound:
		return requestlog.AttemptNotFound
	case provider.ClassTimeout:
		return requestlog.AttemptTimeout
	case provider.ClassServer:
		return requestlog.AttemptServer
	case provider.ClassTransport:
		return requestlog.AttemptTransport
	case provider.ClassInvalidRequest:
		return requestlog.AttemptInvalidRequest
	case provider.ClassContextLength:
		return requestlog.AttemptContextLength
	default:
		return requestlog.AttemptRejected
	}
}

func runErrorTerminal(re provider.RunError, capture *captureSink, attempts int, trace []AttemptTrace) TurnResult {
	if capture.hasFailed {
		return TurnResult{Terminal: Failed{Event: capture.failed}, Attempts: attempts, Trace: trace}
	}
	return turnFailed(canon.Failure{Reason: failureReason(re.Class), Message: runErrorMessage(re)}, attempts, trace)
}

func turnFailed(f canon.Failure, attempts int, trace []AttemptTrace) TurnResult {
	return TurnResult{Terminal: Failed{Event: canon.TurnFailed{Failure: f}}, Attempts: attempts, Trace: trace}
}
