package management

import (
	"time"

	"prism/internal/config"
)

type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

type HealthResponse struct {
	Status string `json:"status"`
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

type QuotaView struct {
	Used      int64     `json:"used"`
	Limit     *int64    `json:"limit,omitempty"`
	WindowEnd time.Time `json:"windowEnd"`
	Source    string    `json:"source"`
}

type Account struct {
	ID                   string     `json:"id"`
	Provider             string     `json:"provider"`
	State                string     `json:"state"`
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
	Account   string    `json:"account"`
	Provider  string    `json:"provider"`
	State     string    `json:"state"`
	Used      int64     `json:"used"`
	Limit     *int64    `json:"limit"`
	WindowEnd time.Time `json:"windowEnd"`
	Source    string    `json:"source"`
}

type UsageResponse struct {
	Accounts []UsageAccount `json:"accounts"`
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
