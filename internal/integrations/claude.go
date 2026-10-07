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
// claude-<provider>--<model> alias shape.
func ClaudeAlias(modelID string) string {
	provider, model, _ := strings.Cut(modelID, "/")
	if model == "" {
		provider, model = "codex", modelID
	}
	return "claude-" + provider + "--" + model
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
	alias := "claude-codex--gpt-5.2-codex"
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
	unlock := lockMutation(c.paths())
	defer unlock()
	refuse := func(err error) ApplyResult { return ApplyResult{ID: c.id, Reason: failureReason("claude apply", err)} }
	raw, _, err := c.io.ReadText(c.paths())
	if err != nil {
		return refuse(err)
	}
	cache, err := readState(c.io, c.cachePath())
	if err != nil {
		return refuse(err)
	}
	if _, _, err = c.io.ReadText(c.envJournalPath()); err != nil {
		return refuse(err)
	}
	if _, _, err = c.io.ReadText(c.cacheJournalPath()); err != nil {
		return refuse(err)
	}
	journal, err := c.inheritedJournal(raw, cache)
	if err != nil {
		return refuse(err)
	}
	active, err := loadJournal(c.io, c.paths())
	if err != nil {
		return refuse(err)
	}
	if active != nil {
		journal = active
	}
	endpoint := ""
	if cache.Present {
		var value struct {
			BaseURL string `json:"baseUrl"`
		}
		if json.Unmarshal([]byte(cache.Text), &value) != nil {
			return refuse(fmt.Errorf("gateway cache is not valid JSON"))
		}
		endpoint = value.BaseURL
		if endpoint != "" && endpoint != ClaudeBaseURL(c.port) && !c.cacheOwned(journal, cache) && !force {
			result := refuse(fmt.Errorf("gateway cache belongs to endpoint %s", endpoint))
			result.Retryable = true
			return result
		}
	}
	side := map[string]fileState{}
	if !cache.Present || endpoint != ClaudeBaseURL(c.port) {
		side[c.cachePath()] = fileState{Present: true, Text: RenderClaudeGatewayCache(ClaudeBaseURL(c.port), c.currentModels(), c.nowMS())}
	}
	outcome, err := applyRestorationUnlocked(c.io, c.paths(), claudeTransform(c.port, c.currentModels(), force), side, false, journal)
	if err != nil {
		return refuse(err)
	}
	return ToApplyResult(c.id, outcome)
}

func (c *ClaudeIntegration) cacheOwned(j *restorationJournal, cache fileState) bool {
	if j == nil || j.Retired {
		return false
	}
	for _, f := range j.Files {
		if f.Path == c.cachePath() && (cache == f.Written || managedCacheState(f.Written, cache) || f.Pending != nil && cache == *f.Pending) {
			return true
		}
	}
	return false
}

func (c *ClaudeIntegration) inheritedJournal(raw string, cache fileState) (*restorationJournal, error) {
	active, err := loadJournal(c.io, c.paths())
	if err != nil || active != nil {
		return active, err
	}
	legacy, present, err := c.io.ReadText(c.envJournalPath())
	if err != nil {
		return nil, err
	}
	backup, err := readState(c.io, c.cacheJournalPath())
	if err != nil {
		return nil, err
	}
	if !present && !backup.Present {
		return nil, nil
	}
	var versioned struct {
		Version int    `json:"version"`
		Target  string `json:"target"`
	}
	if present && json.Unmarshal([]byte(legacy), &versioned) == nil && versioned.Version != 0 {
		return c.inheritVersionedJournal(raw, cache, backup, legacy)
	}
	var displaced map[string]string
	if present && json.Unmarshal([]byte(legacy), &displaced) != nil {
		return nil, fmt.Errorf("historical env journal invalid; original bytes retained")
	}
	var settings map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &settings) != nil {
		return nil, fmt.Errorf("historical settings are unreadable")
	}
	var env map[string]string
	if json.Unmarshal(settings["env"], &env) != nil || env["ANTHROPIC_AUTH_TOKEN"] != prismApiKey {
		return nil, fmt.Errorf("historical restoration journal conflicts with settings")
	}
	entries := ClaudeEnvEntries(c.port, c.currentModels())
	alias := env["ANTHROPIC_MODEL"]
	if !strings.HasPrefix(alias, "claude-") {
		return nil, fmt.Errorf("historical written model is not managed")
	}
	for _, e := range entries {
		if e.Key == "ANTHROPIC_BASE_URL" {
			continue
		}
		want := e.Value
		if strings.Contains(e.Key, "MODEL") && e.Key != "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY" {
			want = alias
		}
		if env[e.Key] != want {
			return nil, fmt.Errorf("historical restoration journal written value conflicts at %s", e.Key)
		}
	}
	original := claudeRollbackTransform()(ApplyEol(raw, EolLF))
	if original.Refused != "" {
		return nil, fmt.Errorf("historical settings cannot be restored")
	}
	var restore []JSONScalarEntry
	for key, value := range displaced {
		allowed := false
		for _, e := range entries {
			allowed = allowed || e.Key == key
		}
		if !allowed {
			return nil, fmt.Errorf("historical restoration journal contains an unknown key")
		}
		restore = append(restore, JSONScalarEntry{Key: key, Value: value})
	}
	sort.Slice(restore, func(i, j int) bool { return restore[i].Key < restore[j].Key })
	if len(restore) > 0 {
		patched := UpsertJSONScalarKeysForced(original.Next, "env", "settings.json", restore, true)
		if patched.Kind != "written" {
			return nil, fmt.Errorf("historical original values cannot be restored")
		}
		original.Next = patched.Next
	}
	j := &restorationJournal{Version: 1, Target: filepath.Clean(c.paths()), Files: []journalFile{{Path: c.paths(), Original: fileState{Present: true, Text: ApplyEol(original.Next, DominantEol(raw))}, Written: fileState{Present: true, Text: raw}}}}
	if cache.Present {
		var value struct {
			BaseURL string `json:"baseUrl"`
		}
		if json.Unmarshal([]byte(cache.Text), &value) != nil || value.BaseURL != env["ANTHROPIC_BASE_URL"] {
			return nil, fmt.Errorf("historical restoration journal conflicts with cache")
		}
		j.Files = append(j.Files, journalFile{Path: c.cachePath(), Original: backup, Written: cache})
	}
	if backup.Present && !cache.Present {
		return nil, fmt.Errorf("historical written cache absent; originals retained")
	}
	return j, nil
}

func (c *ClaudeIntegration) Status() Status {
	return ObservedIntegrationStatus(c.io, c.id, c.paths(), []string{ClaudeConfigDir(c.env, c.home)}, func(path string) ManagedRead {
		content, ok, err := c.io.ReadText(path)
		if err != nil {
			return ManagedRead{Kind: ManagedUnsupported, Reason: failureReason("status read", err)}
		}
		if !ok {
			return ManagedRead{Kind: ManagedAbsent}
		}
		return claudeManagedRead(content)
	}, ClaudeBaseURL(c.port))
}

func (c *ClaudeIntegration) Rollback() ApplyResult {
	unlock := lockMutation(c.paths())
	defer unlock()
	raw, _, err := c.io.ReadText(c.paths())
	if err != nil {
		return ApplyResult{ID: c.id, Reason: failureReason("claude rollback", err)}
	}
	cache, err := readState(c.io, c.cachePath())
	if err != nil {
		return ApplyResult{ID: c.id, Reason: failureReason("claude rollback", err)}
	}
	j, err := c.inheritedJournal(raw, cache)
	if err != nil {
		return ApplyResult{ID: c.id, Reason: failureReason("claude rollback", err)}
	}
	if j != nil {
		if _, present, err := c.io.ReadText(restorationPath(c.paths())); err != nil {
			return ApplyResult{ID: c.id, Reason: failureReason("claude rollback", err)}
		} else if !present {
			if err = publishJournal(c.io, j); err != nil {
				return ApplyResult{ID: c.id, Reason: failureReason("claude rollback", err)}
			}
		}
	}
	outcome, err := rollbackRestorationUnlocked(c.io, c.paths(), claudeRollbackTransform())
	if err != nil {
		return ApplyResult{ID: c.id, Reason: failureReason("claude rollback", err)}
	}
	return ToRollbackResult(c.id, outcome)
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
	return recoverConfigStage(io, configPath)
}
