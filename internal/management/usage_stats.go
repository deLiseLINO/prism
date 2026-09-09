package management

import (
	"context"
	"net/http"
	"time"

	"prism/internal/usage"
)

type UsageSource interface {
	Overview(ctx context.Context, since time.Time) (usage.Aggregate, error)
	ByModel(ctx context.Context, since time.Time) ([]usage.ModelAggregate, error)
	ByProvider(ctx context.Context, since time.Time) ([]usage.ProviderAggregate, error)
}

var statsRanges = map[string]time.Duration{
	"1h":  time.Hour,
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
}

func (s *Server) SetUsageStore(u usage.Store) {
	s.stats = u
}

func (s *Server) statsHandler(w http.ResponseWriter, r *http.Request) {
	if s.stats == nil {
		writeError(w, http.StatusServiceUnavailable, "stats_unavailable", "usage store not configured")
		return
	}
	raw := r.URL.Query().Get("range")
	if raw == "" {
		raw = "1h"
	}
	since := time.Time{}
	if raw != "all" {
		d, ok := statsRanges[raw]
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_range", "range must be one of 1h, 24h, 7d, 30d, all")
			return
		}
		since = time.Now().Add(-d)
	}
	ctx := r.Context()
	overview, err := s.stats.Overview(ctx, since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	models, err := s.stats.ByModel(ctx, since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	providers, err := s.stats.ByProvider(ctx, since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ms := make([]StatsModel, 0, len(models))
	for _, m := range models {
		ms = append(ms, StatsModel{Model: m.Model, Provider: m.Provider, StatsOverview: statsOverviewFrom(m.Aggregate)})
	}
	ps := make([]StatsProvider, 0, len(providers))
	for _, p := range providers {
		ps = append(ps, StatsProvider{Provider: p.Provider, StatsOverview: statsOverviewFrom(p.Aggregate)})
	}
	writeJSON(w, http.StatusOK, StatsResponse{
		Range:     raw,
		Overview:  statsOverviewFrom(overview),
		Models:    ms,
		Providers: ps,
	})
}

func statsOverviewFrom(a usage.Aggregate) StatsOverview {
	return StatsOverview{
		Requests:        a.Requests,
		Completed:       a.Completed,
		Failed:          a.Failed,
		InputTokens:     a.InputTokens,
		OutputTokens:    a.OutputTokens,
		CachedTokens:    a.CachedTokens,
		ReasoningTokens: a.ReasoningTokens,
		TotalTokens:     a.TotalTokens,
		Measured:        a.Measured,
	}
}
