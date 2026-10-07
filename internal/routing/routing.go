package routing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/execution"
	"github.com/deLiseLINO/prism/internal/provider"
	"github.com/deLiseLINO/prism/internal/providers/openaierr"
	"github.com/deLiseLINO/prism/internal/requestlog"
)

type TurnPolicy struct {
	MaxTargetFailovers int
}

// FailoverBudget is how many times a plan of the given size may move on to
// the next target after a failed one.
func (p TurnPolicy) FailoverBudget(targets int) int {
	return p.withDefaults(targets).MaxTargetFailovers
}

func (p TurnPolicy) withDefaults(targets int) TurnPolicy {
	targetFailovers := p.MaxTargetFailovers
	if targetFailovers <= 0 {
		targetFailovers = targets - 1
	}
	if targetFailovers < 0 {
		targetFailovers = 0
	}
	return TurnPolicy{MaxTargetFailovers: targetFailovers}
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
	// RetryAfter is the wait the failing upstream asked for; zero when none.
	RetryAfter time.Duration
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
	forwarded   bool
	next        provider.Sink
	finished    canon.TurnFinished
	failed      canon.TurnFailed
	hasFinished bool
	hasFailed   bool
}

func (s *captureSink) Emit(ev canon.Event) error {
	if s.hasFinished || s.hasFailed {
		return errors.New("routing: event after attempt terminal")
	}
	switch e := ev.(type) {
	case canon.TurnFailed:
		s.failed, s.hasFailed = e, true
		return nil
	case canon.TurnFinished:
		s.forwarded = true
		if err := s.next.Emit(ev); err != nil {
			return err
		}
		s.finished, s.hasFinished = e, true
		return nil
	default:
		s.forwarded = true
		return s.next.Emit(ev)
	}
}

func hopAllowed(re provider.RunError, capture *captureSink, lifecycle ResponseLifecycle) bool {
	if capture.forwarded || lifecycle.CommitState() >= provider.OutputCommitted || !re.Class.FailoverAllowed() {
		return false
	}
	if re.Kind == provider.TerminalEmitted {
		return capture.hasFailed
	}
	return re.Kind.FailoverAllowed()
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
		return turnFailed(canon.Failure{Reason: canon.FailNotFound, Message: fmt.Sprintf("routing: model %q has no enabled target", req.Model)}, 0, nil)
	}
	policy := plan.Policy.withDefaults(len(plan.Targets))
	attempts := 0
	var trace []AttemptTrace
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
			if ti+1 < len(plan.Targets) {
				continue
			}
			return fail(imageUnsupportedFailure(target).Failure)
		}
		lease, err := r.pool.Acquire(ctx, account.AcquireRequest{
			Provider:   target.Provider,
			Model:      target.Model,
			QuotaGroup: r.group,
			Session:    f.Session,
			Thread:     f.Thread,
			Policy:     target.Policy,
		})
		if err != nil {
			if errors.Is(err, account.ErrNoAccount) && ti+1 < len(plan.Targets) && targetSwitches < policy.MaxTargetFailovers {
				targetSwitches++
				continue
			}
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
		capture := &captureSink{next: sink}
		var network []requestlog.NetworkAttemptInfo
		networkDropped := 0
		observe := func(a provider.NetworkAttempt) {
			if len(network) >= requestlog.MaxNetworkAttemptsPerAttempt {
				networkDropped++
				return
			}
			outcome := requestlog.AttemptSucceeded
			msg := ""
			if a.Err != nil {
				outcome = requestlog.AttemptRejected
				if re, typed := runErrorOf(a.Err); typed {
					outcome = attemptOutcome(re)
				}
				if errors.Is(a.Err, provider.ErrUpstreamStall) {
					outcome = requestlog.AttemptUpstreamStall
				}
				msg = "upstream exchange: " + outcome.String()
			}
			network = append(network, requestlog.NetworkAttemptInfo{StartedAt: a.StartedAt, FinishedAt: a.FinishedAt, StatusCode: a.StatusCode, Outcome: outcome, Error: msg})
		}
		observeCredential := func(gen account.CredentialGeneration) {
			if gen == 0 || gen == lease.CredGen {
				return
			}
			lease.CredGen = gen
		}
		err = provider.Waiting(runner).Run(ctx, provider.RunRequest{Request: wireReq, Target: target, Lease: lease, Facts: f, AttemptObserver: observe, CredentialObserver: observeCredential}, capture)
		if capture.hasFailed {
			trace[attempts-1].Usage = capture.failed.Usage
			if err == nil {
				err = provider.RunError{Kind: provider.TerminalEmitted, Class: provider.ClassServer, Accepted: true, Cause: errors.New(capture.failed.Failure.Message)}
			}
		}
		logAttempt := func(outcome requestlog.Outcome, msg string) {
			j.Attempt(requestlog.AttemptInfo{
				Provider:               target.Provider,
				AccountID:              lease.Account,
				CredGen:                lease.CredGen,
				Version:                lease.Version,
				NetworkAttempts:        network,
				NetworkAttemptsDropped: networkDropped,
				Model:                  target.Model,
				StartedAt:              started,
				Outcome:                outcome,
				Error:                  msg,
			})
		}
		if capture.hasFinished && err != nil {
			logAttempt(requestlog.AttemptRejected, err.Error())
			_ = r.pool.Record(ctx, lease, account.RequestRejected{})
			trace[attempts-1].Usage = capture.finished.Usage
			return TurnResult{Terminal: Finished{Event: capture.finished}, Attempts: attempts, Trace: trace}
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
		if errors.Is(err, provider.ErrUpstreamStall) && !capture.hasFailed {
			_ = r.pool.Record(ctx, lease, account.RequestRejected{})
			re, _ := runErrorOf(err)
			logAttempt(requestlog.AttemptUpstreamStall, runErrorMessage(re))
			trace[attempts-1] = AttemptTrace{Provider: target.Provider, Model: target.Model, Outcome: "upstream_stall"}
			return TurnResult{Terminal: Finished{Event: canon.TurnFinished{Status: canon.Incomplete(canon.IncompleteUpstreamStall)}}, Attempts: attempts, Trace: trace}
		}
		if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) && !capture.hasFailed {
			logAttempt(requestlog.AttemptClientClosed, "routing: client closed the request")
			_ = r.pool.Record(ctx, lease, account.RequestRejected{})
			return fail(canon.Failure{Reason: canon.FailClientClosed, Message: "routing: client closed the request"})
		}
		re, typed := runErrorOf(err)
		if !typed {
			logAttempt(requestlog.AttemptRejected, err.Error())
			_ = r.pool.Record(ctx, lease, account.RequestRejected{})
			if capture.hasFailed {
				return runErrorTerminal(provider.RunError{Class: provider.ClassServer, Cause: err}, capture, attempts, trace)
			}
			return fail(canon.Failure{Reason: canon.FailUnknown, Message: fmt.Sprintf("routing: untyped runner error: %v", err)})
		}
		if capture.hasFailed && re.Kind == provider.TerminalEmitted && re.Class == provider.ClassServer && capture.failed.Failure.Provider != nil {
			re.Class = openaierr.ClassForInband(*capture.failed.Failure.Provider, re.Class)
		}
		logAttempt(attemptOutcome(re), runErrorMessage(re))
		if re.Class == provider.ClassUnauthorized {
			_ = r.pool.Record(ctx, lease, account.AuthRejected{})
		} else {
			_ = r.pool.Record(ctx, lease, account.RequestRejected{})
		}
		if capture.hasFailed {
			trace[attempts-1].Usage = capture.failed.Usage
		}
		if ctx.Err() != nil || !hopAllowed(re, capture, lifecycle) {
			return runErrorTerminal(re, capture, attempts, trace)
		}
		if ti+1 >= len(plan.Targets) || targetSwitches >= policy.MaxTargetFailovers {
			return runErrorTerminal(re, capture, attempts, trace)
		}
		targetSwitches++
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
			Usage:  term.Event.Usage,
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
	res := runErrorResult(re, capture, attempts, trace)
	res.RetryAfter = re.RetryAfter
	return res
}

func runErrorResult(re provider.RunError, capture *captureSink, attempts int, trace []AttemptTrace) TurnResult {
	if capture.hasFailed {
		failed := capture.failed
		if failed.Failure.Reason == canon.FailUnknown {
			failed.Failure.Reason = failureReason(re.Class)
		}
		return TurnResult{Terminal: Failed{Event: failed}, Attempts: attempts, Trace: trace}
	}
	failure := canon.Failure{Reason: failureReason(re.Class), Message: runErrorMessage(re)}
	if re.Reported != nil && (len(re.Reported.Error) > 0 || len(re.Reported.StatusDetails) > 0) {
		copied := *re.Reported
		failure.Provider = &copied
		failure.Message = openaierr.Text(copied)
	}
	return turnFailed(failure, attempts, trace)
}

func turnFailed(f canon.Failure, attempts int, trace []AttemptTrace) TurnResult {
	return TurnResult{Terminal: Failed{Event: canon.TurnFailed{Failure: f}}, Attempts: attempts, Trace: trace}
}
