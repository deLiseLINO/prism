package usage

import "prism/internal/canon"

func IncompleteName(r canon.IncompleteReason) string {
	if r == 0 {
		return ""
	}
	switch r {
	case canon.IncompleteMaxOutputTokens:
		return "max_output_tokens"
	case canon.IncompleteContentFilter:
		return "content_filter"
	case canon.IncompleteUpstreamStall:
		return "upstream_stall"
	case canon.IncompleteAdapterEOF:
		return "adapter_eof"
	case canon.IncompleteClientDisconnected:
		return "client_disconnected"
	case canon.IncompleteBufferLimit:
		return "buffer_limit"
	default:
		return "unknown"
	}
}

func FailureName(r canon.FailureReason) string {
	if r == 0 {
		return ""
	}
	switch r {
	case canon.FailUnauthorized:
		return "unauthorized"
	case canon.FailForbidden:
		return "forbidden"
	case canon.FailRateLimited:
		return "rate_limited"
	case canon.FailQuotaExhausted:
		return "quota_exhausted"
	case canon.FailServerOverloaded:
		return "server_overloaded"
	case canon.FailContextLength:
		return "context_length"
	case canon.FailInvalidRequest:
		return "invalid_request"
	case canon.FailOriginRejected:
		return "origin_rejected"
	case canon.FailCyberPolicy:
		return "cyber_policy"
	case canon.FailToolUndeclared:
		return "tool_undeclared"
	case canon.FailToolArgsMalformed:
		return "tool_args_malformed"
	case canon.FailUpstreamTransport:
		return "upstream_transport"
	case canon.FailNotFound:
		return "not_found"
	case canon.FailTimeout:
		return "timeout"
	case canon.FailUnknown:
		return "unknown"
	case canon.FailClientClosed:
		return "client_closed"
	default:
		return "unknown"
	}
}
