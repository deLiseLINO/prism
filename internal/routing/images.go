package routing

import (
	"context"
	"errors"
	"fmt"

	"github.com/deLiseLINO/prism/internal/account"
	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/execution"
	"github.com/deLiseLINO/prism/internal/provider"
	"github.com/deLiseLINO/prism/internal/providers/openaierr"
	"github.com/deLiseLINO/prism/internal/requestlog"
)

// ImageResult is the outcome of one image generation. Response is the
// upstream answer, success or error, when one arrived; Failure explains why
// none did.
type ImageResult struct {
	Response provider.ImageResponse
	Failure  *canon.Failure
	// Outcome is "completed" or "failed", the attempt outcome usage records.
	Outcome string
	// Reason classifies a failed outcome, local or upstream.
	Reason canon.FailureReason
	// Attempted reports whether the request was sent upstream.
	Attempted bool
}

// Images runs standalone image generation against a single target. A
// generation is paid and not idempotent, so it makes exactly one upstream
// attempt: no account or target failover.
type Images struct {
	pool    account.Pool
	runners Runners
	group   account.QuotaGroup
	log     *requestlog.Journal
}

func NewImages(pool account.Pool, runners Runners, group account.QuotaGroup, log *requestlog.Journal) *Images {
	return &Images{pool: pool, runners: runners, group: group, log: log}
}

func (g *Images) Generate(ctx context.Context, model canon.ModelID, target provider.Target, f execution.Facts, body []byte) ImageResult {
	j := g.log.Open(f, model)
	res := g.generate(ctx, target, f, body, j)
	term := requestlog.Terminal{Status: requestlog.StatusCompleted}
	if res.Outcome != "completed" {
		term = requestlog.Terminal{Status: requestlog.StatusFailed, Failed: true, Reason: res.Reason}
	}
	j.Close(term)
	return res
}

func (g *Images) generate(ctx context.Context, target provider.Target, f execution.Facts, body []byte, j *requestlog.Turn) ImageResult {
	fail := func(failure canon.Failure) ImageResult {
		return ImageResult{Failure: &failure, Outcome: "failed", Reason: failure.Reason}
	}
	runner, ok := g.runners.Lookup(target.Provider)
	generator, serves := runner.(provider.ImageGenerator)
	if !ok || !serves {
		return fail(canon.Failure{Reason: canon.FailNotFound, Message: fmt.Sprintf("routing: provider %q does not serve image generation", target.Provider)})
	}
	lease, err := g.pool.Acquire(ctx, account.AcquireRequest{
		Provider:   target.Provider,
		Model:      target.Model,
		QuotaGroup: g.group,
		Session:    f.Session,
		Thread:     f.Thread,
		Policy:     target.Policy,
	})
	if err != nil {
		if errors.Is(err, account.ErrNoAccount) {
			var unavailable *account.AcquireError
			if errors.As(err, &unavailable) && unavailable.Failure != nil {
				return fail(*unavailable.Failure)
			}
			return fail(canon.Failure{Reason: canon.FailAccountUnavailable, Message: err.Error()})
		}
		return fail(canon.Failure{Reason: canon.FailUnknown, Message: fmt.Sprintf("routing: acquire failed: %v", err)})
	}
	started := j.Now()
	var network []requestlog.NetworkAttemptInfo
	observe := func(a provider.NetworkAttempt) {
		info := requestlog.NetworkAttemptInfo{StartedAt: a.StartedAt, FinishedAt: a.FinishedAt, StatusCode: a.StatusCode, Outcome: requestlog.AttemptSucceeded}
		if a.Err != nil {
			info.Outcome = requestlog.AttemptRejected
			if re, typed := runErrorOf(a.Err); typed {
				info.Outcome = attemptOutcome(re)
			}
			info.Error = "upstream exchange: " + info.Outcome.String()
		}
		network = append(network, info)
	}
	logAttempt := func(outcome requestlog.Outcome, msg string) {
		j.Attempt(requestlog.AttemptInfo{
			Provider:        target.Provider,
			AccountID:       lease.Account,
			CredGen:         lease.CredGen,
			Version:         lease.Version,
			NetworkAttempts: network,
			Model:           target.Model,
			StartedAt:       started,
			Outcome:         outcome,
			Error:           msg,
		})
	}
	resp, err := generator.GenerateImage(ctx, provider.ImageRequest{Target: target, Lease: lease, Body: body, AttemptObserver: observe})
	res := settleImage(ctx, resp, err, logAttempt, func(outcome account.Outcome) { _ = g.pool.Record(ctx, lease, outcome) })
	res.Attempted = true
	return res
}

func settleImage(ctx context.Context, resp provider.ImageResponse, err error, logAttempt func(requestlog.Outcome, string), record func(account.Outcome)) ImageResult {
	failed := func(class provider.ErrorClass, failure canon.Failure, resp provider.ImageResponse) ImageResult {
		if class == provider.ClassUnauthorized {
			record(account.AuthRejected{Failure: failure})
		} else {
			record(account.RequestRejected{})
		}
		res := ImageResult{Response: resp, Outcome: "failed", Reason: failure.Reason}
		if resp.Status == 0 {
			res.Failure = &failure
		}
		return res
	}
	if err != nil {
		if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			logAttempt(requestlog.AttemptClientClosed, "routing: client closed the request")
			return failed(0, canon.Failure{Reason: canon.FailClientClosed, Message: "routing: client closed the request"}, provider.ImageResponse{})
		}
		re, typed := runErrorOf(err)
		if !typed {
			logAttempt(requestlog.AttemptRejected, err.Error())
			return failed(0, canon.Failure{Reason: canon.FailUnknown, Message: fmt.Sprintf("routing: untyped image generation error: %v", err)}, provider.ImageResponse{})
		}
		failure := canon.Failure{Reason: failureReason(re.Class), Message: runErrorMessage(re)}
		logAttempt(attemptOutcome(re), failure.Message)
		return failed(re.Class, failure, provider.ImageResponse{})
	}
	if resp.Status >= 200 && resp.Status < 300 {
		logAttempt(requestlog.AttemptSucceeded, "")
		record(account.TurnSucceeded{})
		return ImageResult{Response: resp, Outcome: "completed"}
	}
	reported, parsed := openaierr.Parse(resp.Body)
	reported.Status = resp.Status
	class := openaierr.ClassForStatus(resp.Status, openaierr.Code(reported))
	failure := canon.Failure{Reason: failureReason(class), Message: fmt.Sprintf("upstream status %d", resp.Status)}
	if parsed {
		failure.Provider = &reported
		if message := openaierr.Text(reported); message != "" {
			failure.Message = message
		}
	}
	logAttempt(attemptOutcome(provider.RunError{Class: class}), failure.Message)
	return failed(class, failure, resp)
}
