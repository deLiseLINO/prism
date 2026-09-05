package integrations

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ClaudeBaseURL is the endpoint Claude Code appends /v1/messages to, so unlike
// the chat/responses clients it carries no /v1 suffix.
func ClaudeBaseURL(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

// claudeGatewayModelIDRe mirrors the CLI's gateway cache filter: only
// claude/anthropic-prefixed ids are ever listed there.
var claudeGatewayModelIDRe = regexp.MustCompile(`^(?i:claude|anthropic)`)

// ClaudeAlias maps a prism "<provider>/<model>" id onto the messages wire's
// claude-prism-<provider>--<model> alias shape.
func ClaudeAlias(modelID string) string {
	provider, model, _ := strings.Cut(modelID, "/")
	if model == "" {
		provider, model = "codex", modelID
	}
	return "claude-prism-" + provider + "--" + model
}

// ClaudeDefaultModel picks the deterministic default: the lexicographically
// smallest model id, so two applies never disagree about the env slot values.
func ClaudeDefaultModel(models []Model) (Model, bool) {
	if len(models) == 0 {
		return Model{}, false
	}
	sorted := make([]Model, len(models))
	copy(sorted, models)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	return sorted[0], true
}

// ClaudeEnvEntries renders the env members prism owns in settings.json: the
// endpoint, the loopback placeholder token, every model slot Claude Code
// honors (all pinned to one prism alias so no request escapes to a default
// claude model the messages wire would reject), and the gateway model
// discovery switch that makes Claude Code accept prism aliases by listing
// them on the daemon's /v1/models endpoint.
func ClaudeEnvEntries(port int, models []Model) []JSONScalarEntry {
	alias := "claude-prism-codex--gpt-5.2-codex"
	if defaultModel, ok := ClaudeDefaultModel(models); ok {
		alias = ClaudeAlias(defaultModel.ID)
	}
	base := ClaudeBaseURL(port)
	entries := []JSONScalarEntry{
		{Key: "ANTHROPIC_BASE_URL", Value: base},
		{Key: "ANTHROPIC_AUTH_TOKEN", Value: prismApiKey},
		{Key: "ANTHROPIC_MODEL", Value: alias},
		{Key: "ANTHROPIC_DEFAULT_OPUS_MODEL", Value: alias},
		{Key: "ANTHROPIC_DEFAULT_SONNET_MODEL", Value: alias},
		{Key: "ANTHROPIC_DEFAULT_HAIKU_MODEL", Value: alias},
		{Key: "ANTHROPIC_DEFAULT_FABLE_MODEL", Value: alias},
		{Key: "ANTHROPIC_SMALL_FAST_MODEL", Value: alias},
		{Key: "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY", Value: "1"},
	}
	return entries
}

func claudeEnvKeyNames() []string {
	entries := ClaudeEnvEntries(0, nil)
	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		keys = append(keys, entry.Key)
	}
	return keys
}

func claudeTransform(port int, models []Model) func(current string) ConfigTransform {
	entries := ClaudeEnvEntries(port, models)
	return func(current string) ConfigTransform {
		result := UpsertJSONScalarKeys(current, "env", "settings.json", entries)
		if result.Kind == "written" {
			return nextTransform(result.Next, result.Changed)
		}
		return refusedTransform(result.Reason)
	}
}

func claudeRollbackTransform() func(current string) ConfigTransform {
	keys := claudeEnvKeyNames()
	return func(current string) ConfigTransform {
		result := RemoveJSONScalarKeys(current, "env", "settings.json", keys)
		if result.Kind == "written" {
			return nextTransform(result.Next, result.Changed)
		}
		return refusedTransform(result.Reason)
	}
}

func claudeManagedRead(content string) ManagedRead {
	read := ReadJSONScalarKeys(content, "env", "ANTHROPIC_BASE_URL", "settings.json", claudeEnvKeyNames())
	switch read.Kind {
	case jsonLeafAbsent:
		return ManagedRead{Kind: ManagedAbsent}
	case jsonLeafRefused:
		return ManagedRead{Kind: ManagedUnsupported, Reason: read.Reason}
	}
	return ManagedRead{Kind: ManagedPresent, Endpoint: read.Endpoint}
}

type ClaudeOptions struct {
	Port              int
	Models            []Model
	ModelsSource      func() []Model
	ConfigPath        string
	Env               Env
	Home              string
	CrashBeforeRename bool
	NowMS             func() int64
}

type ClaudeIntegration struct {
	id         ID
	port       int
	models     []Model
	modelsSrc  func() []Model
	configPath string
	env        Env
	home       string
	nowMS      func() int64
}

func (c *ClaudeIntegration) paths() string {
	if c.configPath != "" {
		return c.configPath
	}
	return ClaudeSettingsPath(c.env, c.home)
}

func (c *ClaudeIntegration) cachePath() string {
	if c.configPath != "" {
		return filepath.Join(filepath.Dir(c.configPath), "cache", "gateway-models.json")
	}
	return ClaudeGatewayCachePath(c.env, c.home)
}

func (c *ClaudeIntegration) currentModels() []Model {
	if c.modelsSrc != nil {
		if models := c.modelsSrc(); len(models) > 0 {
			return models
		}
	}
	return c.models
}

func NewClaude(options ClaudeOptions) *ClaudeIntegration {
	nowMS := options.NowMS
	if nowMS == nil {
		nowMS = func() int64 { return time.Now().UnixMilli() }
	}
	return &ClaudeIntegration{id: Claude, port: options.Port, models: options.Models, modelsSrc: options.ModelsSource, configPath: options.ConfigPath, env: options.Env, home: options.Home, nowMS: nowMS}
}

func (c *ClaudeIntegration) ID() ID { return c.id }

func (c *ClaudeIntegration) Apply() ApplyResult {
	cacheErr := c.seedGatewayCache()
	if cacheErr != nil {
		return ToApplyResult(c.id, WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("claude apply", cacheErr)})
	}
	outcome, err := ApplyConfigTransform(c.paths(), claudeTransform(c.port, c.currentModels()), false)
	if err != nil {
		return ToApplyResult(c.id, WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("claude apply", err)})
	}
	return ToApplyResult(c.id, outcome)
}

func (c *ClaudeIntegration) Status() Status {
	return ObservedIntegrationStatus(c.id, c.paths(), []string{ClaudeConfigDir(c.env, c.home)}, func(path string) ManagedRead {
		content, ok := ReadTextIfExists(path)
		if !ok {
			return ManagedRead{Kind: ManagedAbsent}
		}
		return claudeManagedRead(content)
	}, ClaudeBaseURL(c.port))
}

func (c *ClaudeIntegration) Rollback() ApplyResult {
	outcome, err := ApplyConfigTransform(c.paths(), claudeRollbackTransform(), false)
	if err != nil {
		return ToRollbackResult(c.id, WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("claude rollback", err)})
	}
	c.removeGatewayCache()
	return ToRollbackResult(c.id, outcome)
}

// seedGatewayCache writes Claude Code's gateway model discovery cache when it
// is absent: without it the CLI fatals on prism's claude-prism model aliases in
// print mode before it ever fetches /v1/models itself. A cache belonging to
// another endpoint is a named refusal; one already pointing here is left
// alone so the CLI's own refreshes survive re-applies.
func (c *ClaudeIntegration) seedGatewayCache() error {
	path := c.cachePath()
	raw, exists := ReadTextIfExists(path)
	if exists {
		var current struct {
			BaseURL string `json:"baseUrl"`
		}
		if err := json.Unmarshal([]byte(raw), &current); err != nil {
			return fmt.Errorf("gateway cache %s is not valid JSON: %w", path, err)
		}
		switch {
		case current.BaseURL == ClaudeBaseURL(c.port):
			return nil
		case current.BaseURL != "":
			return fmt.Errorf("gateway cache %s belongs to endpoint %s", path, current.BaseURL)
		}
	}
	return AtomicWrite(path, RenderClaudeGatewayCache(ClaudeBaseURL(c.port), c.currentModels(), c.nowMS()))
}

func (c *ClaudeIntegration) removeGatewayCache() {
	path := c.cachePath()
	raw, exists := ReadTextIfExists(path)
	if !exists {
		return
	}
	var current struct {
		BaseURL string `json:"baseUrl"`
	}
	if json.Unmarshal([]byte(raw), &current) == nil && current.BaseURL == ClaudeBaseURL(c.port) {
		_ = os.Remove(path)
	}
}

// RenderClaudeGatewayCache renders the CLI's cache schema: epoch-ms fetchedAt
// and the id/display_name pairs Claude Code's picker reads.
func RenderClaudeGatewayCache(baseURL string, models []Model, nowMS int64) string {
	entries := make([]gatewayCacheModel, 0, len(models))
	for _, m := range models {
		id := ClaudeAlias(m.ID)
		if !claudeGatewayModelIDRe.MatchString(id) {
			continue
		}
		name := m.Name
		if name == "" {
			name = id
		}
		entries = append(entries, gatewayCacheModel{ID: id, DisplayName: name})
	}
	payload := struct {
		BaseURL   string              `json:"baseUrl"`
		FetchedAt int64               `json:"fetchedAt"`
		Models    []gatewayCacheModel `json:"models"`
	}{BaseURL: baseURL, FetchedAt: nowMS, Models: entries}
	if payload.Models == nil {
		payload.Models = []gatewayCacheModel{}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

type gatewayCacheModel struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

func RecoverClaudeConfig(configPath string) bool {
	return RecoverStaged(configPath)
}
