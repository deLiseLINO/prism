package codex

import (
	"context"

	"github.com/deLiseLINO/prism/internal/account"
)

const ProviderID = "codex"

const (
	ResponsesURL = "https://chatgpt.com/backend-api/codex/responses"
	CompactURL   = "https://chatgpt.com/backend-api/codex/responses/compact"
	BaseURL      = "https://chatgpt.com/backend-api/codex"
)

const (
	HeaderContentType       = "Content-Type"
	HeaderAuthorization     = "Authorization"
	HeaderChatGPTAccountID  = "chatgpt-account-id"
	HeaderOpenAIBeta        = "openai-beta"
	HeaderClientRequestID   = "x-client-request-id"
	HeaderIncludeTiming     = "x-responsesapi-include-timing-metrics"
	OpenAIBetaResponsesExpr = "responses=experimental"
	ContentTypeJSON         = "application/json"
)

const reasoningStoreNative = "codex"

var forwardHeaders = []string{
	"authorization",
	"chatgpt-account-id",
	"openai-beta",
	"originator",
	"session_id",
	"session-id",
	"thread-id",
	"x-client-request-id",
	"x-codex-beta-features",
	"x-codex-installation-id",
	"x-codex-parent-thread-id",
	"x-codex-turn-metadata",
	"x-codex-turn-state",
	"x-codex-window-id",
	"x-oai-attestation",
	"x-openai-subagent",
	"x-responsesapi-include-timing-metrics",
}

type Credential struct {
	AccessToken      string
	ChatGPTAccountID string
	Generation       account.CredentialGeneration
}

type CredentialSource interface {
	Credential(ctx context.Context, lease account.Lease) (Credential, error)
}

// CredentialRenewer is an optional CredentialSource capability: it replaces
// an access token the upstream rejected even though it had not expired.
type CredentialRenewer interface {
	RefreshRejected(ctx context.Context, lease account.Lease, rejected string) (Credential, error)
}
