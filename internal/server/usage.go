package server

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/execution"
	"github.com/deLiseLINO/prism/internal/routing"
	"github.com/deLiseLINO/prism/internal/usage"
)

// Only a local gateway may supply an accounting label. It must overwrite
// or remove the header on every route it proxies.
func usageClientLabel(r *http.Request) string {
	if !loopback(r.RemoteAddr) {
		return ""
	}
	values := r.Header.Values("X-Prism-Client")
	if len(values) != 1 {
		return ""
	}
	label := values[0]
	if len(label) > 128 {
		return ""
	}
	for _, c := range label {
		if c < 0x21 || c > 0x7e || c == ',' {
			return ""
		}
	}
	return label
}

func (s *Server) recordUsage(clientLabel string, facts execution.Facts, req canon.Request, proto protocol, res routing.TurnResult, start time.Time) {
	if s.usage == nil {
		return
	}
	rec := usage.Record{
		RequestID:   string(facts.RequestID),
		ClientLabel: clientLabel,
		Timestamp:   start,
		Model:       string(req.Model),
		Protocol:    proto.String(),
		Duration:    s.clock.Now().Sub(start),
		Attempts:    make([]usage.Attempt, len(res.Trace)),
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
		rec.Usage.CachedInputTokens > 0 || rec.Usage.CacheWriteInputTokens > 0 || rec.Usage.ReasoningTokens > 0 {
		rec.UsageKind = usage.StatusReported
	} else {
		rec.UsageKind = usage.StatusZero
	}
	if err := s.usage.Insert(context.Background(), rec); err != nil {
		log.Printf("server: usage record: %v", err)
	}
}
