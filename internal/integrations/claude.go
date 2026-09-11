package integrations

import (
	"encoding/json"
	"fmt"
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

func claudeTransform(port int, models []Model, force bool) func(current string) ConfigTransform {
	entries := ClaudeEnvEntries(port, models)
	return func(current string) ConfigTransform {
		result := UpsertJSONScalarKeysForced(current, "env", "settings.json", entries, force)
		if result.Kind == "written" {
			return nextTransform(result.Next, result.Changed)
		}
		if result.Retryable {
			return forceableTransform(result.Reason)
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
	IO                FileIO
	CrashBeforeRename bool
	NowMS             func() int64
}

type ClaudeIntegration struct {
	id         ID
	port       int
	models     []Model
	modelsSrc  func() []Model
	io         FileIO
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
	return &ClaudeIntegration{id: Claude, port: options.Port, models: options.Models, modelsSrc: options.ModelsSource, io: withLocalIO(options.IO), configPath: options.ConfigPath, env: options.Env, home: options.Home, nowMS: nowMS}
}

func (c *ClaudeIntegration) ID() ID { return c.id }

// claudeEnvJournalHeader journals the user-owned env values displaced by a
// confirmed claude apply, so rollback restores them verbatim.
const claudeEnvJournalHeader = "prism-env"

// claudeEnvJournalPath is the sibling file holding the displaced env values.
func (c *ClaudeIntegration) envJournalPath() string {
	return filepath.Join(filepath.Dir(c.paths()), ".prism-claude-env.json")
}

// cacheJournalPath is the sibling file holding a displaced foreign gateway
// cache, restored verbatim by rollback.
func (c *ClaudeIntegration) cacheJournalPath() string {
	return filepath.Join(filepath.Dir(c.cachePath()), ".prism-gateway-models.json")
}

func (c *ClaudeIntegration) Apply() ApplyResult {
	return c.applyWith(false)
}

func (c *ClaudeIntegration) ApplyForced() ApplyResult {
	return c.applyWith(true)
}

func (c *ClaudeIntegration) applyWith(force bool) ApplyResult {
	cacheErr := c.seedGatewayCacheForced(force)
	if cacheErr != nil {
		return ToApplyResult(c.id, WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("claude apply", cacheErr), Retryable: !force && isForeignCacheError(cacheErr)})
	}
	displaced := map[string]string{}
	outcome, err := ApplyConfigTransform(c.io, c.paths(), claudeTransformForced(c.port, c.currentModels(), force, displaced), false)
	if err != nil {
		return ToApplyResult(c.id, WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("claude apply", err)})
	}
	if outcome.Kind == OutcomeWritten && len(displaced) > 0 {
		_ = AtomicWrite(c.io, c.envJournalPath(), renderClaudeEnvJournal(displaced))
	}
	return ToApplyResult(c.id, outcome)
}

func claudeTransformForced(port int, models []Model, force bool, displaced map[string]string) func(current string) ConfigTransform {
	entries := ClaudeEnvEntries(port, models)
	return func(current string) ConfigTransform {
		result := UpsertJSONScalarKeysForced(current, "env", "settings.json", entries, force)
		if result.Kind == "written" {
			if force {
				collectDisplacedEnv(current, entries, result.Next, displaced)
			}
			return nextTransform(result.Next, result.Changed)
		}
		if result.Retryable {
			return forceableTransform(result.Reason)
		}
		return refusedTransform(result.Reason)
	}
}

func collectDisplacedEnv(before string, entries []JSONScalarEntry, after string, displaced map[string]string) {
	for _, entry := range entries {
		read := ReadJSONScalarKeys(before, "env", entry.Key, "settings.json", []string{entry.Key})
		if read.Kind != jsonLeafPresent || read.Endpoint == nil || *read.Endpoint == entry.Value {
			continue
		}
		_ = after
		displaced[entry.Key] = *read.Endpoint
	}
}

func renderClaudeEnvJournal(displaced map[string]string) string {
	keys := make([]string, 0, len(displaced))
	for key := range displaced {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := []string{"{"}
	for i, key := range keys {
		comma := ","
		if i == len(keys)-1 {
			comma = ""
		}
		lines = append(lines, "  "+jsonString(key)+": "+jsonString(displaced[key])+comma)
	}
	lines = append(lines, "}")
	return strings.Join(lines, "\n") + "\n"
}

func (c *ClaudeIntegration) Status() Status {
	return ObservedIntegrationStatus(c.io, c.id, c.paths(), []string{ClaudeConfigDir(c.env, c.home)}, func(path string) ManagedRead {
		content, ok := c.io.ReadTextIfExists(path)
		if !ok {
			return ManagedRead{Kind: ManagedAbsent}
		}
		return claudeManagedRead(content)
	}, ClaudeBaseURL(c.port))
}

func (c *ClaudeIntegration) Rollback() ApplyResult {
	outcome, err := ApplyConfigTransform(c.io, c.paths(), claudeRollbackTransformForced(c.io, c.envJournalPath()), false)
	if err != nil {
		return ToRollbackResult(c.id, WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("claude rollback", err)})
	}
	c.removeGatewayCache()
	_ = c.io.Remove(c.envJournalPath())
	_ = c.io.Remove(c.cacheJournalPath())
	return ToRollbackResult(c.id, outcome)
}

func claudeRollbackTransformForced(io FileIO, journalPath string) func(current string) ConfigTransform {
	keys := claudeEnvKeyNames()
	displaced := readClaudeEnvJournal(io, journalPath)
	return func(current string) ConfigTransform {
		result := RemoveJSONScalarKeys(current, "env", "settings.json", keys)
		if result.Kind != "written" {
			return refusedTransform(result.Reason)
		}
		if len(displaced) > 0 {
			restored := restoreDisplacedEnv(result.Next, displaced)
			return nextTransform(restored, true)
		}
		return nextTransform(result.Next, result.Changed)
	}
}

func readClaudeEnvJournal(io FileIO, path string) map[string]string {
	raw, ok := io.ReadTextIfExists(path)
	if !ok {
		return nil
	}
	var decoded map[string]string
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil || len(decoded) == 0 {
		return nil
	}
	return decoded
}

func restoreDisplacedEnv(content string, displaced map[string]string) string {
	entries := make([]JSONScalarEntry, 0, len(displaced))
	for key, value := range displaced {
		entries = append(entries, JSONScalarEntry{Key: key, Value: value})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	result := UpsertJSONScalarKeys(content, "env", "settings.json", entries)
	if result.Kind == "written" {
		return result.Next
	}
	return content
}

// seedGatewayCache writes Claude Code's gateway model discovery cache when it
// is absent: without it the CLI fatals on prism's claude-prism model aliases in
// print mode before it ever fetches /v1/models itself. A cache belonging to
// another endpoint is a named refusal; one already pointing here is left
// alone so the CLI's own refreshes survive re-applies.
func (c *ClaudeIntegration) seedGatewayCache() error {
	return c.seedGatewayCacheForced(false)
}

func isForeignCacheError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "belongs to endpoint")
}

func (c *ClaudeIntegration) seedGatewayCacheForced(force bool) error {
	path := c.cachePath()
	displacedPath := c.cacheJournalPath()
	raw, exists := c.io.ReadTextIfExists(path)
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
			if !force {
				return fmt.Errorf("gateway cache %s belongs to endpoint %s", path, current.BaseURL)
			}
			if err := AtomicWrite(c.io, displacedPath, raw); err != nil {
				return fmt.Errorf("gateway cache %s belongs to endpoint %s (journal write failed: %v)", path, current.BaseURL, err)
			}
		}
	}
	return AtomicWrite(c.io, path, RenderClaudeGatewayCache(ClaudeBaseURL(c.port), c.currentModels(), c.nowMS()))
}

func (c *ClaudeIntegration) removeGatewayCache() {
	path := c.cachePath()
	raw, exists := c.io.ReadTextIfExists(path)
	if !exists {
		_ = c.io.Remove(c.cacheJournalPath())
		return
	}
	var current struct {
		BaseURL string `json:"baseUrl"`
	}
	if json.Unmarshal([]byte(raw), &current) == nil && current.BaseURL == ClaudeBaseURL(c.port) {
		_ = c.io.Remove(path)
		if journal, ok := c.io.ReadTextIfExists(c.cacheJournalPath()); ok {
			_ = AtomicWrite(c.io, path, journal)
		}
		_ = c.io.Remove(c.cacheJournalPath())
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

func RecoverClaudeConfig(io FileIO, configPath string) bool {
	return io.RecoverStaged(configPath)
}
