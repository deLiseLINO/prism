package server

import (
	"context"
	"log"
	"time"

	"prism/internal/canon"
	"prism/internal/execution"
	"prism/internal/routing"
	"prism/internal/usage"
)

func (s *Server) recordUsage(facts execution.Facts, req canon.Request, proto protocol, res routing.TurnResult, start time.Time) {
	if s.usage == nil {
		return
	}
	rec := usage.Record{
		RequestID: string(facts.RequestID),
		Timestamp: start,
		Model:     string(req.Model),
		Protocol:  proto.String(),
		Duration:  s.clock.Now().Sub(start),
		Attempts:  make([]usage.Attempt, len(res.Trace)),
	}
	for i, tr := range res.Trace {
		rec.Attempts[i] = usage.Attempt{
			Provider: string(tr.Provider),
			Model:    string(tr.Model),
			Outcome:  tr.Outcome,
			Usage:    usage.Usage(tr.Usage),
		}
	}
	switch t := res.Terminal.(type) {
	case routing.Finished:
		rec.Status = "completed"
		rec.Usage = usage.Usage(t.Event.Usage)
		if reason, ok := t.Event.Status.Reason(); ok {
			rec.Status = "incomplete"
			rec.Reason = usage.IncompleteName(reason)
		}
	case routing.Failed:
		rec.Status = "failed"
		rec.Reason = usage.FailureName(t.Event.Failure.Reason)
		rec.Usage = usage.Usage(t.Event.Usage)
	}
	if rec.Usage.TotalTokens > 0 || rec.Usage.InputTokens > 0 || rec.Usage.OutputTokens > 0 ||
		rec.Usage.CachedInputTokens > 0 || rec.Usage.ReasoningTokens > 0 {
		rec.UsageKind = usage.StatusReported
	} else {
		rec.UsageKind = usage.StatusZero
	}
	if err := s.usage.Insert(context.Background(), rec); err != nil {
		log.Printf("server: usage record: %v", err)
	}
}
