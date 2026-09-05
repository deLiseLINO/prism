package antigravity

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"prism/internal/quota"
)

type quotaPayload struct {
	Models map[string]json.RawMessage `json:"models"`
}

type quotaModelInfo struct {
	DisplayName     string          `json:"displayName"`
	QuotaInfo       json.RawMessage `json:"quotaInfo"`
	QuotaInfos      json.RawMessage `json:"quotaInfos"`
	QuotaInfoByTier json.RawMessage `json:"quotaInfoByTier"`
}

type quotaInfoEntry struct {
	RemainingFraction      float64 `json:"remainingFraction"`
	RemainingPercentage    float64 `json:"remainingPercentage"`
	HasRemainingFraction   bool    `json:"-"`
	HasRemainingPercentage bool    `json:"-"`
	Tier                   string  `json:"tier"`
	ResetTime              string  `json:"resetTime"`
}

type QuotaWindows struct {
	Gem *quota.Snapshot
	Cla *quota.Snapshot
}

func DecodeQuota(data []byte) (QuotaWindows, error) {
	var payload quotaPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return QuotaWindows{}, fmt.Errorf("antigravity: malformed fetchAvailableModels payload: %w", err)
	}
	if payload.Models == nil {
		return QuotaWindows{}, fmt.Errorf("antigravity: fetchAvailableModels payload has no models object")
	}
	modelIDs := make([]string, 0, len(payload.Models))
	for id := range payload.Models {
		modelIDs = append(modelIDs, id)
	}
	sort.Strings(modelIDs)
	var windows QuotaWindows
	for _, id := range modelIDs {
		var info quotaModelInfo
		if err := json.Unmarshal(payload.Models[id], &info); err != nil {
			return QuotaWindows{}, fmt.Errorf("antigravity: malformed model info for %q: %w", id, err)
		}
		for _, entry := range collectQuotaEntries(info) {
			label, ok := classifyAntigravityFamily(id, info.DisplayName, entry.Tier)
			if !ok {
				continue
			}
			snapshot, ok := snapshotFromEntry(entry)
			if !ok {
				continue
			}
			switch label {
			case "Gem":
				if windows.Gem == nil {
					gem := snapshot
					windows.Gem = &gem
				}
			case "Cla":
				if windows.Cla == nil {
					cla := snapshot
					windows.Cla = &cla
				}
			}
		}
	}
	return windows, nil
}

func collectQuotaEntries(info quotaModelInfo) []quotaInfoEntry {
	var raw []json.RawMessage
	appendArray := func(data json.RawMessage) {
		if len(data) == 0 {
			return
		}
		var arr []json.RawMessage
		if err := json.Unmarshal(data, &arr); err == nil {
			raw = append(raw, arr...)
			return
		}
		raw = append(raw, data)
	}
	appendArray(info.QuotaInfo)
	appendArray(info.QuotaInfos)
	if len(info.QuotaInfoByTier) > 0 {
		var byTier map[string]json.RawMessage
		if err := json.Unmarshal(info.QuotaInfoByTier, &byTier); err == nil {
			tiers := make([]string, 0, len(byTier))
			for tier := range byTier {
				tiers = append(tiers, tier)
			}
			sort.Strings(tiers)
			for _, tier := range tiers {
				appendArray(byTier[tier])
			}
		}
	}
	entries := make([]quotaInfoEntry, 0, len(raw))
	for _, data := range raw {
		var entry quotaInfoEntry
		if err := json.Unmarshal(data, &entry); err != nil {
			continue
		}
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(data, &probe); err != nil {
			continue
		}
		if _, ok := probe["remainingFraction"]; ok {
			entry.HasRemainingFraction = true
		} else if _, ok := probe["remainingPercentage"]; ok {
			entry.HasRemainingPercentage = true
		} else {
			continue
		}
		if tier, ok := probe["tier"]; ok {
			var s string
			if err := json.Unmarshal(tier, &s); err == nil {
				entry.Tier = s
			}
		}
		entries = append(entries, entry)
	}
	return entries
}

func classifyAntigravityFamily(modelID string, displayName string, tier string) (string, bool) {
	haystack := strings.ToLower(modelID + " " + displayName + " " + tier)
	if strings.Contains(haystack, "gemini") {
		return "Gem", true
	}
	if strings.Contains(haystack, "claude") || strings.Contains(haystack, "opus") ||
		strings.Contains(haystack, "sonnet") || strings.Contains(haystack, "gpt-oss") ||
		strings.Contains(haystack, "gpt_oss") {
		return "Cla", true
	}
	return "", false
}

func snapshotFromEntry(entry quotaInfoEntry) (quota.Snapshot, bool) {
	var remaining *float64
	if entry.HasRemainingFraction {
		v := entry.RemainingFraction * 100
		remaining = &v
	} else if entry.HasRemainingPercentage {
		v := entry.RemainingPercentage
		remaining = &v
	}
	if remaining == nil {
		return quota.Snapshot{}, false
	}
	used := normalizePercent(100 - normalizePercent(*remaining))
	limit := int64(10000)
	snapshot := quota.Snapshot{
		Used:   int64(math.Round(used * 100)),
		Limit:  &limit,
		Source: quota.SourceEndpoint,
	}
	if reset, ok := parseResetTime(entry.ResetTime); ok {
		snapshot.WindowEnd = reset
	}
	return snapshot, true
}

func normalizePercent(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

var numericStringPattern = regexp.MustCompile(`^[+-]?\d+(\.\d+)?$`)

func parseResetTime(value string) (time.Time, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return time.Time{}, false
	}
	if numericStringPattern.MatchString(trimmed) {
		if f, err := strconv.ParseFloat(trimmed, 64); err == nil && f > 0 {
			return epochMillis(f)
		}
		return time.Time{}, false
	}
	if f, err := strconv.ParseFloat(trimmed, 64); err == nil && f > 0 {
		return epochMillis(f)
	}
	if parsed, err := time.Parse(time.RFC3339, trimmed); err == nil {
		return parsed, true
	}
	return time.Time{}, false
}

func epochMillis(value float64) (time.Time, bool) {
	if math.IsNaN(value) || value <= 0 {
		return time.Time{}, false
	}
	if value > 10_000_000_000 {
		return time.UnixMilli(int64(value)), true
	}
	return time.UnixMilli(int64(value * 1000)), true
}
