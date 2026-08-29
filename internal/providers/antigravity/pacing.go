package antigravity

import (
	"strconv"
	"strings"
	"time"

	"prism/internal/provider"
)

const (
	RetryAttempts  = 3
	BackoffBase    = 250 * time.Millisecond
	BackoffMax     = 2 * time.Second
	AttemptTimeout = 200 * time.Second
)

func BackoffDelay(attempt int, retryAfter time.Duration, hasRetryAfter bool, rand01 float64) time.Duration {
	if hasRetryAfter {
		if retryAfter > BackoffMax {
			return BackoffMax
		}
		return retryAfter
	}
	exp := BackoffBase << attempt
	if exp > BackoffMax || exp <= 0 {
		exp = BackoffMax
	}
	return time.Duration(int64(float64(exp) * (0.8 + 0.4*rand01)))
}

func parseRetryAfter(v string) (time.Duration, bool) {
	raw := strings.TrimSpace(v)
	if raw == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(raw); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second, true
	}
	if f, err := strconv.ParseFloat(raw, 64); err == nil && f >= 0 {
		return time.Duration(f * float64(time.Second)), true
	}
	return 0, false
}

const (
	statusUnauthorized    = 401
	statusForbidden       = 403
	statusNotFound        = 404
	statusTooManyRequests = 429
	statusRequestTimeout  = 408
	statusGatewayTimeout  = 504
)

func classifyStatus(status int) (provider.ErrorClass, provider.RunErrorKind) {
	switch status {
	case statusUnauthorized, statusForbidden:
		return provider.ClassUnauthorized, provider.TerminalOmitted
	case statusNotFound:
		return provider.ClassNotFound, provider.TerminalOmitted
	case statusTooManyRequests:
		return provider.ClassRateLimited, provider.Retryable
	case statusRequestTimeout, statusGatewayTimeout:
		return provider.ClassTimeout, provider.Retryable
	}
	if status >= 500 {
		return provider.ClassServer, provider.Retryable
	}
	return provider.ClassInvalidRequest, provider.TerminalOmitted
}

var quotaExhaustedNeedles = []string{
	"quotafailure",
	"quota exceeded",
	"exceeded your current quota",
	"billing",
	"individual quota reached",
	"quota reached",
	"enable overages",
	"exhausted your capacity",
	"daily limit reached",
	"weekly limit reached",
}

var transientRateLimitPatterns = []string{
	"per minute",
	"per-minute",
	"per min",
	"rpm",
	"requests per minute",
	"too many requests",
	"rate limit",
	"retry after",
	"retry-after",
	"concurrent request limit",
}

func isQuotaExhaustedBody(body string) bool {
	lower := strings.ToLower(body)
	for _, needle := range transientRateLimitPatterns {
		if strings.Contains(lower, needle) {
			return false
		}
	}
	for _, needle := range quotaExhaustedNeedles {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func retryableStatus(status int) bool {
	return status == 429 || status == 500 || status == 502 || status == 503 || status == 504
}
