package management

import (
	"time"

	"prism/internal/canon"
	"prism/internal/config"
	"prism/internal/execution"
	"prism/internal/requestlog"
)

type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

type HealthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version,omitempty"`
}

type Caps struct {
	Reasoning     bool `json:"reasoning"`
	CustomTools   bool `json:"customTools"`
	LocalShell    bool `json:"localShell"`
	ToolSearch    bool `json:"toolSearch"`
	Compaction    bool `json:"compaction"`
	CountTokens   bool `json:"countTokens"`
	ParallelTools bool `json:"parallelTools"`
}

type Model struct {
	ID    string `json:"id"`
	Alias string `json:"alias,omitempty"`
	Caps  Caps   `json:"caps"`
}

type ModelsResponse struct {
	Models []Model `json:"models"`
}

type ProviderCredential struct {
	State string `json:"state"`
}

type Provider struct {
	ID             string                          `json:"id"`
	Wire           string                          `json:"wire"`
	BaseURL        string                          `json:"baseURL,omitempty"`
	DefaultModel   string                          `json:"defaultModel,omitempty"`
	Models         []string                        `json:"models,omitempty"`
	DisabledModels []string                        `json:"disabledModels,omitempty"`
	SyncedModels   []string                        `json:"syncedModels,omitempty"`
	ModelSettings  map[string]config.ModelSettings `json:"modelSettings,omitempty"`
	Enabled        *bool                           `json:"enabled,omitempty"`
	Pool           *config.PoolSettings            `json:"pool,omitempty"`
	Credential     ProviderCredential              `json:"credential"`
}

type ProvidersResponse struct {
	Generation    uint64     `json:"generation"`
	ContextWindow int        `json:"contextWindow"`
	Providers     []Provider `json:"providers"`
}

type ContextWindowWrite struct {
	ContextWindow      int    `json:"contextWindow"`
	ExpectedGeneration uint64 `json:"expectedGeneration"`
}

type ProviderMutationResponse struct {
	Generation uint64   `json:"generation"`
	Provider   Provider `json:"provider"`
}

type GenerationResponse struct {
	Generation uint64 `json:"generation"`
}

type ProviderWrite struct {
	ID                 string                           `json:"id"`
	Wire               string                           `json:"wire"`
	BaseURL            *string                          `json:"baseURL,omitempty"`
	APIKeyRef          *string                          `json:"apiKeyRef,omitempty"`
	DefaultModel       *string                          `json:"defaultModel,omitempty"`
	Models             []string                         `json:"models"`
	DisabledModels     []string                         `json:"disabledModels"`
	SyncedModels       *[]string                        `json:"syncedModels,omitempty"`
	ModelSettings      *map[string]config.ModelSettings `json:"modelSettings,omitempty"`
	Enabled            *bool                            `json:"enabled,omitempty"`
	Pool               *config.PoolSettings             `json:"pool,omitempty"`
	Credential         string                           `json:"credential,omitempty"`
	ExpectedGeneration uint64                           `json:"expectedGeneration"`
}

type VisionSidecarWrite struct {
	Enabled            bool   `json:"enabled"`
	Target             string `json:"target,omitempty"`
	ExpectedGeneration uint64 `json:"expectedGeneration"`
}

type QuotaView struct {
	Used      int64             `json:"used"`
	Limit     *int64            `json:"limit,omitempty"`
	WindowEnd time.Time         `json:"windowEnd"`
	Source    string            `json:"source"`
	Windows   []QuotaWindowView `json:"windows,omitempty"`
}

type QuotaWindowView struct {
	Label     string    `json:"label"`
	Used      int64     `json:"used"`
	Limit     *int64    `json:"limit,omitempty"`
	WindowEnd time.Time `json:"windowEnd,omitempty"`
}

type Account struct {
	ID                   string     `json:"id"`
	Provider             string     `json:"provider"`
	State                string     `json:"state"`
	Email                string     `json:"email,omitempty"`
	Priority             int        `json:"priority"`
	Version              uint64     `json:"version"`
	CredentialGeneration uint64     `json:"credentialGeneration"`
	Quota                QuotaView  `json:"quota"`
	CooldownUntil        *time.Time `json:"cooldownUntil,omitempty"`
	SoftAvoidUntil       *time.Time `json:"softAvoidUntil,omitempty"`
	InFlight             int        `json:"inFlight"`
}

type AccountsResponse struct {
	Accounts []Account `json:"accounts"`
}

type VersionWrite struct {
	Version uint64 `json:"version"`
}

type PriorityWrite struct {
	Version  uint64 `json:"version"`
	Priority int    `json:"priority"`
}

type QuotaResponse struct {
	Account string    `json:"account"`
	Quota   QuotaView `json:"quota"`
}

type Target struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Weight   int    `json:"weight"`
}

type Combo struct {
	ID          string   `json:"id"`
	Targets     []Target `json:"targets"`
	Strategy    string   `json:"strategy"`
	StickyLimit int      `json:"stickyLimit"`
	Alias       string   `json:"alias,omitempty"`
	NativeAlias string   `json:"nativeAlias,omitempty"`
	DisplayName string   `json:"displayName,omitempty"`
	ImageInput  *bool    `json:"imageInput,omitempty"`
}

type CombosResponse struct {
	Generation uint64  `json:"generation"`
	Combos     []Combo `json:"combos"`
}

type ComboWrite struct {
	Targets            []Target `json:"targets"`
	Strategy           string   `json:"strategy"`
	StickyLimit        int      `json:"stickyLimit"`
	Alias              string   `json:"alias,omitempty"`
	NativeAlias        string   `json:"nativeAlias,omitempty"`
	DisplayName        string   `json:"displayName,omitempty"`
	ImageInput         *bool    `json:"imageInput,omitempty"`
	ExpectedGeneration uint64   `json:"expectedGeneration"`
}

type RoutesResponse struct {
	Generation uint64            `json:"generation"`
	Routes     map[string]string `json:"routes"`
}

type RouteWrite struct {
	Value              string `json:"value"`
	ExpectedGeneration uint64 `json:"expectedGeneration"`
}

type UsageAccount struct {
	Account  string    `json:"account"`
	Provider string    `json:"provider"`
	State    string    `json:"state"`
	Email    string    `json:"email,omitempty"`
	Quota    QuotaView `json:"quota"`
}

type UsageResponse struct {
	Accounts []UsageAccount `json:"accounts"`
}

type StatsOverview struct {
	Requests        int64 `json:"requests"`
	Completed       int64 `json:"completed"`
	Failed          int64 `json:"failed"`
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	CachedTokens    int64 `json:"cached_tokens"`
	ReasoningTokens int64 `json:"reasoning_tokens"`
	TotalTokens     int64 `json:"total_tokens"`
	Measured        int64 `json:"measured"`
}

type StatsModel struct {
	Model    string `json:"model"`
	Provider string `json:"provider"`
	StatsOverview
}

type StatsProvider struct {
	Provider string `json:"provider"`
	StatsOverview
}

type StatsResponse struct {
	Range     string          `json:"range"`
	Overview  StatsOverview   `json:"overview"`
	Models    []StatsModel    `json:"models"`
	Providers []StatsProvider `json:"providers"`
}

type AuthStartResponse struct {
	Session string `json:"session"`
	URL     string `json:"url"`
}

type AuthCallbackWrite struct {
	Session string `json:"session"`
	Code    string `json:"code"`
	State   string `json:"state"`
}

type AuthStatusResponse struct {
	Provider string `json:"provider"`
	State    string `json:"state"`
}

type UsageBreakdownView struct {
	Input     int64 `json:"input"`
	Output    int64 `json:"output"`
	Cached    int64 `json:"cached"`
	Reasoning int64 `json:"reasoning"`
	Total     int64 `json:"total"`
}

type AttemptView struct {
	Provider   string `json:"provider"`
	Account    string `json:"account"`
	Model      string `json:"model"`
	StartedAt  string `json:"startedAt"`
	DurationMS int64  `json:"durationMs"`
	Outcome    string `json:"outcome"`
	Error      string `json:"error,omitempty"`
}

type RequestView struct {
	Seq        uint64             `json:"seq"`
	RequestID  string             `json:"requestId,omitempty"`
	Client     string             `json:"client"`
	Session    string             `json:"session,omitempty"`
	Model      string             `json:"model"`
	StartedAt  string             `json:"startedAt"`
	DurationMS int64              `json:"durationMs"`
	Status     string             `json:"status"`
	Reason     string             `json:"reason,omitempty"`
	Usage      UsageBreakdownView `json:"usage"`
	Attempts   []AttemptView      `json:"attempts"`
}

type RequestsResponse struct {
	Requests []RequestView `json:"requests"`
	Dropped  uint64        `json:"dropped"`
}

func requestView(e requestlog.Entry) RequestView {
	views := make([]AttemptView, 0, len(e.Attempts))
	for _, a := range e.Attempts {
		views = append(views, AttemptView{
			Provider:   string(a.Provider),
			Account:    string(a.AccountID),
			Model:      string(a.Model),
			StartedAt:  a.StartedAt.UTC().Format(time.RFC3339),
			DurationMS: a.Duration.Milliseconds(),
			Outcome:    a.Outcome.String(),
			Error:      a.Error,
		})
	}
	v := RequestView{
		Seq:        e.Seq,
		RequestID:  string(e.RequestID),
		Client:     clientName(e.Client),
		Session:    string(e.Session),
		Model:      string(e.Model),
		StartedAt:  e.StartedAt.UTC().Format(time.RFC3339),
		DurationMS: e.Duration.Milliseconds(),
		Status:     statusName(e),
		Reason:     reasonName(e.Terminal),
		Usage: UsageBreakdownView{
			Input:     e.Terminal.Usage.InputTokens,
			Output:    e.Terminal.Usage.OutputTokens,
			Cached:    e.Terminal.Usage.CachedInputTokens,
			Reasoning: e.Terminal.Usage.ReasoningTokens,
			Total:     e.Terminal.Usage.TotalTokens,
		},
		Attempts: views,
	}
	return v
}

func statusName(e requestlog.Entry) string {
	switch e.Status {
	case requestlog.StatusOpen:
		return "open"
	case requestlog.StatusCompleted:
		return "completed"
	case requestlog.StatusIncomplete:
		return "incomplete"
	case requestlog.StatusFailed:
		return "failed"
	default:
		return "unknown"
	}
}

func reasonName(term requestlog.Terminal) string {
	if term.Failed {
		return failureName(term.Reason)
	}
	if term.Status == requestlog.StatusIncomplete {
		return incompleteName(term.Incomplete)
	}
	return ""
}

func failureName(r canon.FailureReason) string {
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

func incompleteName(r canon.IncompleteReason) string {
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

func clientName(c execution.Client) string {
	switch c {
	case execution.ClientCodex:
		return "codex"
	case execution.ClientGrok:
		return "grok"
	case execution.ClientOMP:
		return "omp"
	case execution.ClientAnthropic:
		return "anthropic"
	default:
		return "unknown"
	}
}
