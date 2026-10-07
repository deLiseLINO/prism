package provider

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const MaxRetryAfter = time.Hour

func RetryAfterDelay(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseFloat(value, 64); err == nil {
		return clampRetryAfter(seconds * float64(time.Second))
	}
	if when, err := http.ParseTime(value); err == nil {
		return clampRetryAfter(float64(when.Sub(now)))
	}
	return 0
}

func ParseRetryAfter(h http.Header, now time.Time) time.Duration {
	if ms, err := strconv.ParseFloat(strings.TrimSpace(h.Get("Retry-After-Ms")), 64); err == nil {
		return clampRetryAfter(ms * float64(time.Millisecond))
	}
	return RetryAfterDelay(h.Get("Retry-After"), now)
}

func clampRetryAfter(nanos float64) time.Duration {
	switch {
	case math.IsNaN(nanos) || nanos <= 0:
		return 0
	case nanos >= float64(MaxRetryAfter):
		return MaxRetryAfter
	default:
		return time.Duration(nanos)
	}
}

func HTTPRetryAfterDelay(resp *http.Response) time.Duration {
	if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusServiceUnavailable {
		return 0
	}
	return ParseRetryAfter(resp.Header, time.Now())
}

type NetworkAttempt struct {
	StartedAt  time.Time
	FinishedAt time.Time
	StatusCode int
	Err        error
}
