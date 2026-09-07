package codex

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"prism/internal/quota"
)

const (
	monthlyWindowMinMinutes = 28 * 24 * 60
	weeklyWindowMinMinutes  = 24 * 60
	resetSecondsThreshold   = 10_000_000_000
)

type QuotaResult struct {
	Snapshot quota.Snapshot
	OK       bool
	Warnings []string
}

type windowReading struct {
	percent      float64
	percentSet   bool
	resetAt      float64
	resetPresent bool
	minutes      float64
	minutesSet   bool
}

func ParseQuotaHeaders(h http.Header) QuotaResult {
	var warnings []string
	primary := readWindow(h, "primary", &warnings)
	secondary := readWindow(h, "secondary", &warnings)
	tertiary := readWindow(h, "tertiary", &warnings)

	primaryMonthly := primary.minutesSet && primary.minutes >= monthlyWindowMinMinutes
	primaryShort := primary.minutesSet && primary.minutes > 0 && primary.minutes < weeklyWindowMinMinutes

	var windows []windowReading
	switch {
	case primaryMonthly:
		if primary.percentSet {
			windows = append(windows, primary)
		}
		if secondary.percentSet {
			windows = append(windows, secondary)
		}
	case primaryShort:
		if secondary.percentSet {
			windows = append(windows, secondary)
		}
		if primary.percentSet {
			windows = append(windows, primary)
		}
	default:
		if primary.percentSet {
			windows = append(windows, primary)
		} else if secondary.percentSet {
			windows = append(windows, secondary)
		}
		if tertiary.percentSet {
			windows = append(windows, tertiary)
		}
	}

	governing, ok := governingWindow(windows)
	if !ok {
		return QuotaResult{Warnings: warnings}
	}
	snapshot := snapshotFromReading(governing, quota.SourceHeader)
	snapshot.Windows = windowsFromReadings(windows)
	return QuotaResult{Snapshot: snapshot, OK: true, Warnings: warnings}
}

func governingWindow(windows []windowReading) (windowReading, bool) {
	var best windowReading
	found := false
	for _, w := range windows {
		if !found || w.percent > best.percent {
			best = w
			found = true
		}
	}
	return best, found
}

func readWindow(h http.Header, slot string, warnings *[]string) windowReading {
	var w windowReading
	if v, ok := headerValue(h, "x-codex-"+slot+"-used-percent"); ok {
		pct, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || math.IsNaN(pct) || math.IsInf(pct, 0) {
			*warnings = append(*warnings, "quota_used_percent_unparseable:"+slot)
		} else {
			w.percent = math.Max(0, math.Min(100, pct))
			w.percentSet = true
		}
	}
	if v, ok := headerValue(h, "x-codex-"+slot+"-reset-at"); ok {
		reset, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || math.IsNaN(reset) || reset < 0 {
			*warnings = append(*warnings, "quota_reset_at_unparseable:"+slot)
		} else {
			w.resetAt = reset
			w.resetPresent = true
		}
	}
	if v, ok := headerValue(h, "x-codex-"+slot+"-window-minutes"); ok {
		minutes, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || math.IsNaN(minutes) || minutes <= 0 {
			*warnings = append(*warnings, "quota_window_minutes_unparseable:"+slot)
		} else {
			w.minutes = minutes
			w.minutesSet = true
		}
	}
	return w
}

func resetTime(raw float64) time.Time {
	ms := raw
	if raw <= resetSecondsThreshold {
		ms = raw * 1000
	}
	secs := int64(ms / 1000)
	nanos := int64((ms - float64(secs)*1000) * 1_000_000)
	return time.Unix(secs, nanos).UTC()
}

func headerValue(h http.Header, name string) (string, bool) {
	v := h.Get(name)
	if strings.TrimSpace(v) == "" {
		return "", false
	}
	return v, true
}

func newInt64(v int64) *int64 {
	out := v
	return &out
}
