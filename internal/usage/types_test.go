package usage

import (
	"testing"

	"prism/internal/canon"
)

func TestIncompleteName(t *testing.T) {
	cases := []struct {
		reason canon.IncompleteReason
		want   string
	}{
		{0, ""},
		{canon.IncompleteMaxOutputTokens, "max_output_tokens"},
		{canon.IncompleteContentFilter, "content_filter"},
		{canon.IncompleteUpstreamStall, "upstream_stall"},
		{canon.IncompleteAdapterEOF, "adapter_eof"},
		{canon.IncompleteClientDisconnected, "client_disconnected"},
		{canon.IncompleteBufferLimit, "buffer_limit"},
		{canon.IncompleteReason(99), "unknown"},
	}
	for _, tc := range cases {
		if got := IncompleteName(tc.reason); got != tc.want {
			t.Errorf("IncompleteName(%d) = %q want %q", tc.reason, got, tc.want)
		}
	}
}

func TestFailureName(t *testing.T) {
	cases := []struct {
		reason canon.FailureReason
		want   string
	}{
		{0, ""},
		{canon.FailUnauthorized, "unauthorized"},
		{canon.FailForbidden, "forbidden"},
		{canon.FailRateLimited, "rate_limited"},
		{canon.FailQuotaExhausted, "quota_exhausted"},
		{canon.FailServerOverloaded, "server_overloaded"},
		{canon.FailContextLength, "context_length"},
		{canon.FailInvalidRequest, "invalid_request"},
		{canon.FailOriginRejected, "origin_rejected"},
		{canon.FailCyberPolicy, "cyber_policy"},
		{canon.FailToolUndeclared, "tool_undeclared"},
		{canon.FailToolArgsMalformed, "tool_args_malformed"},
		{canon.FailUpstreamTransport, "upstream_transport"},
		{canon.FailNotFound, "not_found"},
		{canon.FailTimeout, "timeout"},
		{canon.FailUnknown, "unknown"},
		{canon.FailClientClosed, "client_closed"},
		{canon.FailureReason(99), "unknown"},
	}
	for _, tc := range cases {
		if got := FailureName(tc.reason); got != tc.want {
			t.Errorf("FailureName(%d) = %q want %q", tc.reason, got, tc.want)
		}
	}
}

func TestStatusString(t *testing.T) {
	cases := []struct {
		status Status
		want   string
	}{
		{StatusReported, "reported"},
		{StatusZero, "zero"},
		{Status(0), "unknown"},
		{Status(3), "unknown"},
	}
	for _, tc := range cases {
		if got := tc.status.String(); got != tc.want {
			t.Errorf("Status(%d).String() = %q want %q", tc.status, got, tc.want)
		}
	}
}
