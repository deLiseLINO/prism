package integrations

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var CodexFence = Fence{
	Begin: "# >>> prism managed block (codex) — do not edit (removed by prism rollback) >>>",
	End:   "# <<< prism managed block (codex) <<<",
}

// prismRoutingMarker names the root openai_base_url pair prism owns, in the
// same comment-above-key shape other config managers use for their injections, so
// each tool recognizes the other's bytes and backs off instead of
// overwriting them. A takeover journals the displaced pair inside the fence
// and rollback restores it verbatim.
const prismRoutingMarker = "# Managed by prism: Codex routes through the local prism proxy."

const routingJournalHeader = "# prism-routing"

var codexTableLineRe = regexp.MustCompile(`^[ \t]*\[`)
var codexCommentLineRe = regexp.MustCompile(`^[ \t]*#`)
var codexRootBaseUrlKeyRe = regexp.MustCompile(`^[ \t]*openai_base_url[ \t]*=`)
var codexRootBaseUrlValueRe = regexp.MustCompile(`^[ \t]*openai_base_url[ \t]*=[ \t]*"((?:[^"\\]|\\.)*)"[ \t]*(?:#.*)?$`)
var codexRootProviderKeyRe = regexp.MustCompile(`^[ \t]*model_provider[ \t]*=`)
var codexRootProviderValueRe = regexp.MustCompile(`^[ \t]*model_provider[ \t]*=[ \t]*"((?:[^"\\]|\\.)*)"[ \t]*(?:#.*)?$`)

// prismCatalogMarker names the root model_catalog_json pair prism owns. The
// key points codex's model picker at a catalog file prism renders next to the
// config; without it codex lists only its built-in GPT models.
const prismCatalogMarker = "# Managed by prism: model catalog listing the local prism proxy models."

const codexCatalogFileName = "prism-catalog.json"

var codexRootCatalogKeyRe = regexp.MustCompile(`^[ \t]*model_catalog_json[ \t]*=`)
var codexRootCatalogValueRe = regexp.MustCompile(`^[ \t]*model_catalog_json[ \t]*=[ \t]*"((?:[^"\\]|\\.)*)"[ \t]*(?:#.*)?$`)

// codexRoutingJournal records, inside the fence, what prism did to the root
// routing key: written is the URL prism last wrote (ownership survives a
// config reserialization that strips comments), displaced holds the foreign
// pair removed by a takeover so rollback can restore it byte-for-byte.
type codexRoutingJournal struct {
	written   string
	displaced []string
}

func parseCodexRoutingJournal(inner string) codexRoutingJournal {
	var journal codexRoutingJournal
	inJournal := false
	for _, line := range strings.Split(inner, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == routingJournalHeader:
			inJournal = true
		case !inJournal:
		case strings.HasPrefix(trimmed, "# written:"):
			journal.written = strings.TrimSpace(strings.TrimPrefix(trimmed, "# written:"))
		case strings.HasPrefix(trimmed, "# displaced:"):
			rest := strings.TrimPrefix(trimmed, "# displaced:")
			journal.displaced = append(journal.displaced, strings.TrimPrefix(rest, " "))
		}
	}
	return journal
}

func renderCodexRoutingJournal(journal codexRoutingJournal) []string {
	if journal.written == "" {
		return nil
	}
	lines := []string{routingJournalHeader, "# written: " + journal.written}
	for _, line := range journal.displaced {
		lines = append(lines, "# displaced: "+line)
	}
	return lines
}

func codexManagedBlock(port int, journal codexRoutingJournal) string {
	lines := []string{
		"[model_providers.prism]",
		`name = "prism"`,
		"base_url = " + tomlString(ProviderBaseUrl(port)),
		`wire_api = "responses"`,
	}
	lines = append(lines, renderCodexRoutingJournal(journal)...)
	return strings.Join(lines, "\n")
}

func codexRootEnd(lines []string) int {
	for i, line := range lines {
		if codexTableLineRe.MatchString(line) {
			return i
		}
	}
	return len(lines)
}

// codexRootModelProvider reads the root model_provider selection. The third
// return is false when a key exists in a form prism cannot parse; callers
// must refuse rather than guess.
func codexRootModelProvider(content string) (provider string, found bool, parseable bool) {
	lines := strings.Split(content, "\n")
	rootEnd := codexRootEnd(lines)
	for i := range rootEnd {
		if !codexRootProviderKeyRe.MatchString(lines[i]) {
			continue
		}
		match := codexRootProviderValueRe.FindStringSubmatch(lines[i])
		if match == nil {
			return "", true, false
		}
		return match[1], true, true
	}
	return "", false, true
}

func insertLinesAt(lines []string, at int, inserted ...string) []string {
	out := make([]string, 0, len(lines)+len(inserted))
	out = append(out, lines[:at]...)
	out = append(out, inserted...)
	out = append(out, lines[at:]...)
	return out
}

func lastNonBlankLine(lines []string) int {
	last := -1
	for i, line := range lines {
		if strings.TrimSpace(line) != "" {
			last = i
		}
	}
	return last
}

// upsertCodexRootRouting owns the root openai_base_url pair, the built-in
// override that points codex's default openai provider at the daemon. In
// routing mode a foreign pair under an prism marker is displaced and
// journaled; a prism pair is rewritten in place; a missing pair is inserted
// before the first table header. providerOnly (model_provider = "prism")
// removes a journaled prism pair instead, restoring any displaced bytes. A
// user-owned or ambiguous key is a named refusal and the file stays put.
func upsertCodexRootRouting(content string, port int, journal codexRoutingJournal, providerOnly bool) (string, codexRoutingJournal, string) {
	if providerOnly {
		if journal.written == "" {
			return content, journal, ""
		}
		next, changed := restoreCodexRootPair(content, journal)
		if !changed {
			return content, journal, ""
		}
		return next, codexRoutingJournal{}, ""
	}

	lines := strings.Split(content, "\n")
	rootEnd := codexRootEnd(lines)
	firstTable := -1
	if rootEnd < len(lines) {
		firstTable = rootEnd
	}
	var found []int
	for i := range rootEnd {
		if codexRootBaseUrlKeyRe.MatchString(lines[i]) {
			found = append(found, i)
		}
	}
	if len(found) > 1 {
		return content, journal, "prism: codex config has multiple root openai_base_url keys; apply left the file untouched"
	}

	keyLine := "openai_base_url = " + tomlString(ProviderBaseUrl(port))
	next := codexRoutingJournal{written: ProviderBaseUrl(port)}
	if len(found) == 1 {
		i := found[0]
		match := codexRootBaseUrlValueRe.FindStringSubmatch(lines[i])
		if match == nil {
			return content, journal, "prism: codex config has a root openai_base_url in a form prism cannot parse; apply left the file untouched"
		}
		value := match[1]
		comment := ""
		if i > 0 && codexCommentLineRe.MatchString(lines[i-1]) {
			comment = lines[i-1]
		}
		prismOwned := comment == prismRoutingMarker || (journal.written != "" && journal.written == value)
		switch {
		case prismOwned:
			if comment == prismRoutingMarker {
				lines[i] = keyLine
			} else {
				lines = insertLinesAt(lines, i, prismRoutingMarker)
				lines[i+1] = keyLine
			}
			next.displaced = journal.displaced
		case strings.Contains(strings.ToLower(comment), "prism"):
			next.displaced = []string{comment, lines[i]}
			lines[i-1] = prismRoutingMarker
			lines[i] = keyLine
		default:
			return content, journal, `prism: codex config sets a user-owned root openai_base_url; remove it, or select the prism provider with model_provider = "prism", then re-apply (file left untouched)`
		}
	} else {
		insertAt := firstTable
		if insertAt == -1 {
			insertAt = lastNonBlankLine(lines) + 1
		}
		lines = insertLinesAt(lines, insertAt, prismRoutingMarker, keyLine)
	}
	return strings.Join(lines, "\n"), next, ""
}

// restoreCodexRootPair removes a provably prism-owned root pair, restoring
// journaled displaced bytes at the same position. An unprovable pair is left
// in place.
func restoreCodexRootPair(content string, journal codexRoutingJournal) (string, bool) {
	lines := strings.Split(content, "\n")
	rootEnd := codexRootEnd(lines)
	target := -1
	count := 0
	for i := range rootEnd {
		if codexRootBaseUrlKeyRe.MatchString(lines[i]) {
			count++
			target = i
		}
	}
	if count != 1 {
		return content, false
	}
	match := codexRootBaseUrlValueRe.FindStringSubmatch(lines[target])
	if match == nil || match[1] != journal.written {
		return content, false
	}
	removeFrom := target
	if target > 0 && lines[target-1] == prismRoutingMarker {
		removeFrom = target - 1
	}
	out := make([]string, 0, len(lines)+len(journal.displaced))
	out = append(out, lines[:removeFrom]...)
	out = append(out, journal.displaced...)
	out = append(out, lines[target+1:]...)
	return strings.Join(out, "\n"), true
}

// upsertCodexRootCatalog owns the root model_catalog_json pair pointing at
// the prism-rendered catalog file. A prism pair is rewritten in place; a
// missing pair is inserted before the first table header; anything else is a
// named refusal and the file stays put.
func upsertCodexRootCatalog(content string, catalogPath string) (string, string) {
	lines := strings.Split(content, "\n")
	rootEnd := codexRootEnd(lines)
	var found []int
	for i := range rootEnd {
		if codexRootCatalogKeyRe.MatchString(lines[i]) {
			found = append(found, i)
		}
	}
	if len(found) > 1 {
		return content, "prism: codex config has multiple root model_catalog_json keys; apply left the file untouched"
	}
	keyLine := "model_catalog_json = " + tomlString(catalogPath)
	if len(found) == 1 {
		i := found[0]
		match := codexRootCatalogValueRe.FindStringSubmatch(lines[i])
		if match == nil {
			return content, "prism: codex config has a root model_catalog_json in a form prism cannot parse; apply left the file untouched"
		}
		comment := ""
		if i > 0 && codexCommentLineRe.MatchString(lines[i-1]) {
			comment = lines[i-1]
		}
		if comment != prismCatalogMarker && match[1] != catalogPath {
			return content, fmt.Sprintf("prism: codex config already selects a model catalog at %q; remove it, then re-apply (file left untouched)", match[1])
		}
		if comment == prismCatalogMarker {
			lines[i] = keyLine
		} else {
			lines = insertLinesAt(lines, i, prismCatalogMarker)
			lines[i+1] = keyLine
		}
		return strings.Join(lines, "\n"), ""
	}
	insertAt := codexRootEnd(lines)
	if insertAt == len(lines) {
		insertAt = lastNonBlankLine(lines) + 1
	}
	lines = insertLinesAt(lines, insertAt, prismCatalogMarker, keyLine)
	return strings.Join(lines, "\n"), ""
}

// restoreCodexRootCatalog removes the prism-owned model_catalog_json pair.
// A foreign or edited pair is left in place.
func restoreCodexRootCatalog(content string, catalogPath string) (string, bool) {
	lines := strings.Split(content, "\n")
	rootEnd := codexRootEnd(lines)
	target := -1
	count := 0
	for i := range rootEnd {
		if codexRootCatalogKeyRe.MatchString(lines[i]) {
			count++
			target = i
		}
	}
	if count != 1 {
		return content, false
	}
	match := codexRootCatalogValueRe.FindStringSubmatch(lines[target])
	if match == nil || (match[1] != catalogPath && !(target > 0 && lines[target-1] == prismCatalogMarker)) {
		return content, false
	}
	removeFrom := target
	if target > 0 && lines[target-1] == prismCatalogMarker {
		removeFrom = target - 1
	}
	out := append([]string{}, lines[:removeFrom]...)
	out = append(out, lines[target+1:]...)
	return strings.Join(out, "\n"), true
}

type codexReasoningLevel struct {
	Effort      string `json:"effort"`
	Description string `json:"description"`
}

type codexTruncationPolicy struct {
	Mode  string `json:"mode"`
	Limit int    `json:"limit"`
}

type codexProvenance struct {
	Provider string `json:"provider"`
	ModelID  string `json:"model_id"`
}

type codexCatalogEntry struct {
	Slug                           string                 `json:"slug"`
	DisplayName                    string                 `json:"display_name"`
	Description                    string                 `json:"description"`
	DefaultReasoningLevel          string                 `json:"default_reasoning_level"`
	SupportedReasoningLevels       []codexReasoningLevel  `json:"supported_reasoning_levels"`
	ShellType                      string                 `json:"shell_type"`
	Visibility                     string                 `json:"visibility"`
	SupportedInAPI                 bool                   `json:"supported_in_api"`
	Priority                       int                    `json:"priority"`
	IncludeSkillsUsageInstructions bool                   `json:"include_skills_usage_instructions"`
	IncludePluginUsageInstructions bool                   `json:"include_plugin_usage_instructions"`
	IncludeAppsUsageInstructions   bool                   `json:"include_apps_usage_instructions"`
	DefaultReasoningSummary        string                 `json:"default_reasoning_summary"`
	SupportVerbosity               bool                   `json:"support_verbosity"`
	DefaultVerbosity               string                 `json:"default_verbosity"`
	ApplyPatchToolType             string                 `json:"apply_patch_tool_type"`
	WebSearchToolType              string                 `json:"web_search_tool_type"`
	TruncationPolicy               codexTruncationPolicy  `json:"truncation_policy"`
	SupportsImageDetailOriginal    bool                   `json:"supports_image_detail_original"`
	CompHash                       string                 `json:"comp_hash"`
	EffectiveContextWindowPercent  int                    `json:"effective_context_window_percent"`
	ExperimentalSupportedTools     []string               `json:"experimental_supported_tools"`
	InputModalities                []string               `json:"input_modalities"`
	SupportsSearchTool             bool                   `json:"supports_search_tool"`
	NodeReplAutoReviewRequired     bool                   `json:"node_repl_auto_review_required"`
	NodeReplDisabled               bool                   `json:"node_repl_disabled"`
	BaseInstructions               string                 `json:"base_instructions"`
	SupportsParallelToolCalls      bool                   `json:"supports_parallel_tool_calls"`
	SupportsReasoningSummaries     bool                   `json:"supports_reasoning_summaries"`
	ContextWindow                  int                    `json:"context_window"`
	MaxContextWindow               int                    `json:"max_context_window"`
	AutoCompactTokenLimit          int                    `json:"auto_compact_token_limit"`
	PrismCapabilityProvenance  codexProvenance       `json:"prism_capability_provenance"`
	MultiAgentVersion              *string                `json:"multi_agent_version"`
	AutoReviewModelOverride        any                    `json:"auto_review_model_override"`
}

type codexCatalog struct {
	Models []codexCatalogEntry `json:"models"`
}

const codexCatalogDefaultContextWindow = 128000

var codexCatalogReasoningLevels = []codexReasoningLevel{
	{Effort: "low", Description: "Fast responses with lighter reasoning"},
	{Effort: "medium", Description: "Balances speed and reasoning depth for everyday tasks"},
	{Effort: "high", Description: "Greater reasoning depth for complex problems"},
	{Effort: "xhigh", Description: "Extra high reasoning depth for complex problems"},
	{Effort: "max", Description: "Maximum reasoning depth for the hardest problems"},
	{Effort: "ultra", Description: "Maximum reasoning with automatic task delegation"},
}

// RenderCodexCatalog renders the catalog file codex's model picker reads via
// the root model_catalog_json key, in the entry shape codex's strict catalog
// parser accepts (mirrors the routed-model rows prism writes).
func RenderCodexCatalog(models []Model) string {
	entries := make([]codexCatalogEntry, 0, len(models))
	for _, model := range models {
		window := model.ContextWindow
		if window <= 0 {
			window = codexCatalogDefaultContextWindow
		}
		modality := []string{"text"}
		if model.ImageInput {
			modality = append(modality, "image")
		}
		provider, _, routed := strings.Cut(model.ID, "/")
		if !routed {
			provider = ""
		}
		description := "Routed via the local prism proxy."
		if routed {
			description = fmt.Sprintf("Routed via prism → %s.", provider)
		}
		entries = append(entries, codexCatalogEntry{
			Slug:                           model.ID,
			DisplayName:                    model.ID,
			Description:                    description,
			DefaultReasoningLevel:          "medium",
			SupportedReasoningLevels:       codexCatalogReasoningLevels,
			ShellType:                      "shell_command",
			Visibility:                     "list",
			SupportedInAPI:                 true,
			Priority:                       5,
			IncludeSkillsUsageInstructions: false,
			IncludePluginUsageInstructions: false,
			IncludeAppsUsageInstructions:   false,
			DefaultReasoningSummary:        "none",
			SupportVerbosity:               true,
			DefaultVerbosity:               "low",
			ApplyPatchToolType:             "freeform",
			WebSearchToolType:              "text_and_image",
			TruncationPolicy:               codexTruncationPolicy{Mode: "tokens", Limit: 10000},
			SupportsImageDetailOriginal:    true,
			CompHash:                       "3000",
			EffectiveContextWindowPercent:  95,
			ExperimentalSupportedTools:     []string{},
			InputModalities:                modality,
			SupportsSearchTool:             true,
			NodeReplAutoReviewRequired:     true,
			NodeReplDisabled:               false,
			BaseInstructions: fmt.Sprintf(
				"You are a coding agent powered by %s. If asked which model you are, identify as %s. Do not claim to be a different model or to have a different creator. You and the user share one workspace, and your job is to collaborate with them until their intended goal is completely handled.",
				model.ID, model.ID),
			SupportsParallelToolCalls:  true,
			SupportsReasoningSummaries: false,
			ContextWindow:              window,
			MaxContextWindow:           window,
			AutoCompactTokenLimit:      window * 9 / 10,
			PrismCapabilityProvenance: codexProvenance{
				Provider: provider,
				ModelID:  strings.TrimPrefix(model.ID, provider+"/"),
			},
			MultiAgentVersion:       nil,
			AutoReviewModelOverride: nil,
		})
	}
	raw, err := json.Marshal(codexCatalog{Models: entries})
	if err != nil {
		return `{"models":[]}`
	}
	return string(raw)
}

func CodexCatalogPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), codexCatalogFileName)
}

func codexTransform(port int, catalogPath string) func(current string) ConfigTransform {
	return func(current string) ConfigTransform {
		return applyCodexConfig(current, port, catalogPath)
	}
}

func applyCodexConfig(current string, port int, catalogPath string) ConfigTransform {
	fenceLookup := FindFencedRegion(current, CodexFence)
	if fenceLookup.Kind == FencedOrphaned {
		return refusedTransform(DamagedFenceApply)
	}
	journal := codexRoutingJournal{}
	if fenceLookup.Kind == FencedFound {
		journal = parseCodexRoutingJournal(fenceLookup.Region.Inner)
	}

	provider, hasProvider, parseable := codexRootModelProvider(current)
	if !parseable {
		return refusedTransform("prism: codex config has a root model_provider in a form prism cannot parse; apply left the file untouched")
	}
	if hasProvider && provider != "openai" && provider != "prism" {
		return refusedTransform(fmt.Sprintf("prism: codex config selects the external model_provider %q; apply left the file untouched", provider))
	}

	next, journal, refusal := upsertCodexRootRouting(current, port, journal, hasProvider && provider == "prism")
	if refusal != "" {
		return refusedTransform(refusal)
	}

	if catalogPath != "" {
		next, refusal = upsertCodexRootCatalog(next, catalogPath)
		if refusal != "" {
			return refusedTransform(refusal)
		}
	}

	result := UpsertFencedBlock(next, CodexFence, codexManagedBlock(port, journal))
	if result.Kind != "written" {
		return refusedTransform(result.Reason)
	}
	return nextTransform(result.Next, result.Changed || next != current)
}

func codexRollbackTransform(catalogPath string) func(current string) ConfigTransform {
	return func(current string) ConfigTransform {
		lookup := FindFencedRegion(current, CodexFence)
		switch lookup.Kind {
		case FencedOrphaned:
			return refusedTransform(DamagedFenceRollback)
		case FencedAbsent:
			if catalogPath == "" {
				return nextTransform(current, false)
			}
			next, changedCatalog := restoreCodexRootCatalog(current, catalogPath)
			return nextTransform(next, changedCatalog)
		}
		next, changedFence := RemoveFencedBlock(current, CodexFence)
		journal := parseCodexRoutingJournal(lookup.Region.Inner)
		changedRoot := false
		if journal.written != "" {
			next, changedRoot = restoreCodexRootPair(next, journal)
		}
		changedCatalog := false
		if catalogPath != "" {
			next, changedCatalog = restoreCodexRootCatalog(next, catalogPath)
		}
		return nextTransform(next, changedFence || changedRoot || changedCatalog)
	}
}

func codexManagedRead(content string) ManagedRead {
	lookup := FindFencedRegion(content, CodexFence)
	switch lookup.Kind {
	case FencedAbsent:
		return ManagedRead{Kind: ManagedAbsent}
	case FencedOrphaned:
		return ManagedRead{Kind: ManagedDamaged, Reason: DamagedFenceApply}
	}
	endpoint, ok := ParseTomlStringField(lookup.Region.Inner, "base_url")
	if !ok {
		return ManagedRead{Kind: ManagedPresent, Endpoint: nil}
	}
	return ManagedRead{Kind: ManagedPresent, Endpoint: &endpoint}
}

func WriteCodexConfig(options CodexOptions) WriteOutcome {
	options = normalizeCodexOptions(options)
	models := options.Models
	if options.ModelsSource != nil {
		if live := options.ModelsSource(); len(live) > 0 {
			models = live
		}
	}
	catalogPath := ""
	if len(models) > 0 {
		catalogPath = CodexCatalogPath(options.ConfigPath)
		if err := AtomicWrite(catalogPath, RenderCodexCatalog(models)); err != nil {
			return WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("codex apply", err)}
		}
	}
	outcome, err := ApplyConfigTransform(options.ConfigPath, codexTransform(options.Port, catalogPath), options.CrashBeforeRename)
	if err != nil {
		_ = os.Remove(catalogPath)
		return WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("codex apply", err)}
	}
	if outcome.Kind == OutcomeRefused {
		_ = os.Remove(catalogPath)
	}
	return outcome
}

func StripCodexConfig(configPath string) WriteOutcome {
	outcome, err := ApplyConfigTransform(configPath, codexRollbackTransform(CodexCatalogPath(configPath)), false)
	if err != nil {
		return WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("codex rollback", err)}
	}
	_ = os.Remove(CodexCatalogPath(configPath))
	return outcome
}

func RecoverCodexConfig(configPath string) bool {
	return RecoverStaged(configPath)
}

type CodexOptions struct {
	Port              int
	Models            []Model
	ModelsSource      func() []Model
	ConfigPath        string
	Env               Env
	Home              string
	CrashBeforeRename bool
}

type CodexIntegration struct {
	id         ID
	port       int
	models     []Model
	modelsSrc  func() []Model
	configPath string
	env        Env
	home       string
}

func NewCodex(options CodexOptions) *CodexIntegration {
	options = normalizeCodexOptions(options)
	return &CodexIntegration{id: Codex, port: options.Port, models: options.Models, modelsSrc: options.ModelsSource, configPath: options.ConfigPath, env: options.Env, home: options.Home}
}

func normalizeCodexOptions(options CodexOptions) CodexOptions {
	if options.ConfigPath == "" {
		options.ConfigPath = CodexConfigPath(options.Env, options.Home)
	}
	return options
}

func (c *CodexIntegration) ID() ID { return c.id }

func (c *CodexIntegration) Apply() ApplyResult {
	return ToApplyResult(c.id, WriteCodexConfig(CodexOptions{Port: c.port, Models: c.models, ModelsSource: c.modelsSrc, ConfigPath: c.configPath}))
}

func (c *CodexIntegration) Status() Status {
	return ObservedIntegrationStatus(c.id, c.configPath, []string{CodexHome(c.env, c.home)}, func(path string) ManagedRead {
		content, ok := ReadTextIfExists(path)
		if !ok {
			return ManagedRead{Kind: ManagedAbsent}
		}
		return codexManagedRead(content)
	}, ProviderBaseUrl(c.port))
}

func (c *CodexIntegration) Rollback() ApplyResult {
	return ToRollbackResult(c.id, StripCodexConfig(c.configPath))
}
