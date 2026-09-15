package integrations

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

var CodexFence = Fence{
	Begin: "# >>> prism managed block (codex) — do not edit (removed by prism rollback) >>>",
	End:   "# <<< prism managed block (codex) <<<",
}

// prismRoutingMarker names the root openai_base_url pair prism owns, in the
// comment-above-key shape other config managers use for their injections, so
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
		case trimmed == catalogJournalHeader || trimmed == providerJournalHeader:
			inJournal = false
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

// codexCatalogJournal records a displaced foreign model_catalog_json pair,
// restored verbatim by rollback. Unlike routing, the catalog pair has no
// marker provenance, so the displaced bytes live in the fence only.
type codexCatalogJournal struct {
	displaced []string
}

const catalogJournalHeader = "# prism-catalog"

func parseCodexCatalogJournal(inner string) codexCatalogJournal {
	var journal codexCatalogJournal
	inJournal := false
	for _, line := range strings.Split(inner, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == catalogJournalHeader:
			inJournal = true
		case trimmed == routingJournalHeader || trimmed == providerJournalHeader:
			inJournal = false
		case !inJournal:
		case strings.HasPrefix(trimmed, "# displaced:"):
			rest := strings.TrimPrefix(trimmed, "# displaced:")
			journal.displaced = append(journal.displaced, strings.TrimPrefix(rest, " "))
		}
	}
	return journal
}

func renderCodexCatalogJournal(journal codexCatalogJournal) []string {
	if len(journal.displaced) == 0 {
		return nil
	}
	lines := []string{catalogJournalHeader}
	for _, line := range journal.displaced {
		lines = append(lines, "# displaced: "+line)
	}
	return lines
}

func codexManagedBlock(port int, journal codexRoutingJournal, catalogJournal codexCatalogJournal, providerJournal codexProviderJournal) string {
	lines := []string{
		"[model_providers.prism]",
		`name = "prism"`,
		"base_url = " + tomlString(ProviderBaseUrl(port)),
		`wire_api = "responses"`,
	}
	lines = append(lines, renderCodexRoutingJournal(journal)...)
	lines = append(lines, renderCodexCatalogJournal(catalogJournal)...)
	lines = append(lines, renderCodexProviderJournal(providerJournal)...)
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

// codexProviderJournal records a displaced foreign model_provider selection,
// restored verbatim by rollback alongside the routing journal.
type codexProviderJournal struct {
	displaced []string
}

const providerJournalHeader = "# prism-provider"

func parseCodexProviderJournal(inner string) codexProviderJournal {
	var journal codexProviderJournal
	inJournal := false
	for _, line := range strings.Split(inner, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == providerJournalHeader:
			inJournal = true
		case trimmed == routingJournalHeader || trimmed == catalogJournalHeader:
			inJournal = false
		case !inJournal:
		case strings.HasPrefix(trimmed, "# displaced:"):
			rest := strings.TrimPrefix(trimmed, "# displaced:")
			journal.displaced = append(journal.displaced, strings.TrimPrefix(rest, " "))
		}
	}
	return journal
}

func renderCodexProviderJournal(journal codexProviderJournal) []string {
	if len(journal.displaced) == 0 {
		return nil
	}
	lines := []string{providerJournalHeader}
	for _, line := range journal.displaced {
		lines = append(lines, "# displaced: "+line)
	}
	return lines
}

// switchCodexProviderSelection rewrites the single root model_provider pair
// to the prism selection; only a parseable string pair moves, with the
// comment line above the key preserved.
func switchCodexProviderSelection(content string) (string, []string, bool) {
	lines := strings.Split(content, "\n")
	rootEnd := codexRootEnd(lines)
	target := -1
	for i := range rootEnd {
		if codexRootProviderKeyRe.MatchString(lines[i]) {
			if target != -1 {
				return content, nil, false
			}
			target = i
		}
	}
	if target == -1 {
		return content, nil, false
	}
	match := codexRootProviderValueRe.FindStringSubmatch(lines[target])
	if match == nil {
		return content, nil, false
	}
	displaced := []string{lines[target]}
	keyLine := "model_provider = " + tomlString("prism")
	next := append([]string{}, lines...)
	next[target] = keyLine
	return strings.Join(next, "\n"), displaced, true
}

func restoreCodexProviderSelection(content string, journal codexProviderJournal) (string, bool) {
	if len(journal.displaced) == 0 {
		return content, false
	}
	lines := strings.Split(content, "\n")
	rootEnd := codexRootEnd(lines)
	providerIdx := -1
	providerCount := 0
	routingIdx := -1
	routingCount := 0
	for i := range rootEnd {
		if codexRootProviderKeyRe.MatchString(lines[i]) {
			providerCount++
			providerIdx = i
		}
		if codexRootBaseUrlKeyRe.MatchString(lines[i]) {
			routingCount++
			routingIdx = i
		}
	}
	if providerCount != 1 {
		return content, false
	}
	match := codexRootProviderValueRe.FindStringSubmatch(lines[providerIdx])
	if match == nil || match[1] != "prism" {
		return content, false
	}
	out := make([]string, 0, len(lines)+len(journal.displaced))
	skipRouting := routingCount == 1 && routingIdx == providerIdx+1
	if skipRouting {
		from := providerIdx
		if providerIdx > 0 && lines[providerIdx-1] == prismRoutingMarker {
			from = providerIdx - 1
		}
		out = append(out, lines[:from]...)
		out = append(out, journal.displaced...)
		out = append(out, lines[routingIdx+1:]...)
		return strings.Join(out, "\n"), true
	}
	out = append(out, lines[:providerIdx]...)
	out = append(out, journal.displaced...)
	out = append(out, lines[providerIdx+1:]...)
	return strings.Join(out, "\n"), true
}

// removePrismRootPairs strips the prism-owned root pairs written for a
// provider-switched apply: the routing marker pair and the catalog marker
// pair. Rollback restores the displaced provider line, so no prism root
// bytes may survive it.
func removePrismRootPairs(content string, catalogPath string) (string, bool) {
	lines := strings.Split(content, "\n")
	rootEnd := codexRootEnd(lines)
	routingIdx := -1
	catalogIdx := -1
	for i := range rootEnd {
		if codexRootBaseUrlKeyRe.MatchString(lines[i]) {
			if m := codexRootBaseUrlValueRe.FindStringSubmatch(lines[i]); m != nil && i > 0 && lines[i-1] == prismRoutingMarker {
				routingIdx = i
			}
		}
		if codexRootCatalogKeyRe.MatchString(lines[i]) {
			if m := codexRootCatalogValueRe.FindStringSubmatch(lines[i]); m != nil && m[1] == catalogPath && i > 0 && lines[i-1] == prismCatalogMarker {
				catalogIdx = i
			}
		}
	}
	if routingIdx == -1 && catalogIdx == -1 {
		return content, false
	}
	remove := map[int]bool{}
	if routingIdx != -1 {
		remove[routingIdx] = true
		remove[routingIdx-1] = true
	}
	if catalogIdx != -1 {
		remove[catalogIdx] = true
		remove[catalogIdx-1] = true
	}
	out := make([]string, 0, len(lines))
	for i, line := range lines {
		if remove[i] {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n"), true
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
// routing mode a foreign pair under another manager's marker is displaced and
// journaled; a prism pair is rewritten in place; a missing pair is inserted
// before the first table header. providerOnly (model_provider = "prism")
// removes a journaled prism pair instead, restoring any displaced bytes. A
// user-owned or ambiguous key is a forceable refusal and the file stays put.
func upsertCodexRootRouting(content string, port int, journal codexRoutingJournal, providerOnly bool, force bool) (string, codexRoutingJournal, string) {
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
		case strings.Contains(comment, "Managed by"):
			next.displaced = []string{comment, lines[i]}
			lines[i-1] = prismRoutingMarker
			lines[i] = keyLine
		default:
			if !force {
				return content, journal, `prism: codex config sets a user-owned root openai_base_url; remove it, or select the prism provider with model_provider = "prism", then re-apply (file left untouched)`
			}
			displaced := []string{lines[i]}
			from := i
			if comment != "" {
				displaced = []string{comment, lines[i]}
				from = i - 1
			}
			next.displaced = append(append([]string{}, journal.displaced...), displaced...)
			out := make([]string, 0, len(lines)+2-len(displaced))
			out = append(out, lines[:from]...)
			out = append(out, prismRoutingMarker, keyLine)
			out = append(out, lines[i+1:]...)
			return strings.Join(out, "\n"), next, ""
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
// missing pair is inserted before the first table header; a foreign pair is
// a forceable refusal and the file stays put. Force displaces the foreign
// pair and journals it for rollback.
func upsertCodexRootCatalog(content string, catalogPath string, force bool, catalogJournal *codexCatalogJournal) (string, string) {
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
			if !force {
				return content, fmt.Sprintf("prism: codex config already selects a model catalog at %q; remove it, then re-apply (file left untouched)", match[1])
			}
			from := i
			displaced := []string{lines[i]}
			if comment != "" {
				from = i - 1
				displaced = []string{comment, lines[i]}
			}
			out := make([]string, 0, len(lines)+2-len(displaced))
			out = append(out, lines[:from]...)
			out = append(out, prismCatalogMarker, keyLine)
			out = append(out, lines[i+1:]...)
			catalogJournal.displaced = displaced
			return strings.Join(out, "\n"), ""
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

// restoreCodexRootCatalog removes the prism-owned model_catalog_json pair,
// restoring a journaled displaced pair at the same position. A foreign or
// edited pair is left in place.
func restoreCodexRootCatalog(content string, catalogPath string, catalogJournal codexCatalogJournal) (string, bool) {
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
	if match == nil || match[1] != catalogPath {
		return content, false
	}
	removeFrom := target
	if target > 0 && lines[target-1] == prismCatalogMarker {
		removeFrom = target - 1
	}
	out := append([]string{}, lines[:removeFrom]...)
	out = append(out, catalogJournal.displaced...)
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
	Slug                           string                `json:"slug"`
	DisplayName                    string                `json:"display_name"`
	Description                    string                `json:"description"`
	DefaultReasoningLevel          string                `json:"default_reasoning_level"`
	SupportedReasoningLevels       []codexReasoningLevel `json:"supported_reasoning_levels"`
	ShellType                      string                `json:"shell_type"`
	Visibility                     string                `json:"visibility"`
	SupportedInAPI                 bool                  `json:"supported_in_api"`
	Priority                       int                   `json:"priority"`
	IncludeSkillsUsageInstructions bool                  `json:"include_skills_usage_instructions"`
	IncludePluginUsageInstructions bool                  `json:"include_plugin_usage_instructions"`
	IncludeAppsUsageInstructions   bool                  `json:"include_apps_usage_instructions"`
	DefaultReasoningSummary        string                `json:"default_reasoning_summary"`
	SupportVerbosity               bool                  `json:"support_verbosity"`
	DefaultVerbosity               string                `json:"default_verbosity"`
	ApplyPatchToolType             string                `json:"apply_patch_tool_type"`
	WebSearchToolType              string                `json:"web_search_tool_type,omitempty"`
	TruncationPolicy               codexTruncationPolicy `json:"truncation_policy"`
	SupportsImageDetailOriginal    bool                  `json:"supports_image_detail_original"`
	CompHash                       string                `json:"comp_hash"`
	EffectiveContextWindowPercent  int                   `json:"effective_context_window_percent"`
	ExperimentalSupportedTools     []string              `json:"experimental_supported_tools"`
	InputModalities                []string              `json:"input_modalities"`
	SupportsSearchTool             bool                  `json:"supports_search_tool"`
	NodeReplAutoReviewRequired     bool                  `json:"node_repl_auto_review_required"`
	NodeReplDisabled               bool                  `json:"node_repl_disabled"`
	BaseInstructions               string                `json:"base_instructions"`
	SupportsParallelToolCalls      bool                  `json:"supports_parallel_tool_calls"`
	SupportsReasoningSummaries     bool                  `json:"supports_reasoning_summaries"`
	ContextWindow                  int                   `json:"context_window"`
	MaxContextWindow               int                   `json:"max_context_window"`
	AutoCompactTokenLimit          int                   `json:"auto_compact_token_limit"`
	CapabilityProvenance           codexProvenance       `json:"capability_provenance"`
	MultiAgentVersion              *string               `json:"multi_agent_version"`
	AutoReviewModelOverride        any                   `json:"auto_review_model_override"`
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
// parser accepts.
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
			IncludeSkillsUsageInstructions: true,
			IncludePluginUsageInstructions: true,
			IncludeAppsUsageInstructions:   true,
			DefaultReasoningSummary:        "none",
			SupportVerbosity:               true,
			DefaultVerbosity:               "low",
			ApplyPatchToolType:             "freeform",
			// The wire drops web_search tools and items, so no web-search
			// capability is advertised until the daemon implements search.
			TruncationPolicy:              codexTruncationPolicy{Mode: "tokens", Limit: 10000},
			SupportsImageDetailOriginal:   true,
			CompHash:                      "3000",
			EffectiveContextWindowPercent: 95,
			ExperimentalSupportedTools:    []string{},
			InputModalities:               modality,
			SupportsSearchTool:            false,
			NodeReplAutoReviewRequired:    true,
			NodeReplDisabled:              false,
			BaseInstructions: fmt.Sprintf(
				"You are a coding agent powered by %s. If asked which model you are, identify as %s. Do not claim to be a different model or to have a different creator. You and the user share one workspace, and your job is to collaborate with them until their intended goal is completely handled.",
				model.ID, model.ID),
			SupportsParallelToolCalls:  true,
			SupportsReasoningSummaries: false,
			ContextWindow:              window,
			MaxContextWindow:           window,
			AutoCompactTokenLimit:      window * 9 / 10,
			CapabilityProvenance: codexProvenance{
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

func codexTransform(port int, catalogPath string, force bool) func(current string) ConfigTransform {
	return func(current string) ConfigTransform {
		return applyCodexConfig(current, port, catalogPath, force)
	}
}

func applyCodexConfig(current string, port int, catalogPath string, force bool) ConfigTransform {
	fenceLookup := FindFencedRegion(current, CodexFence)
	if fenceLookup.Kind == FencedOrphaned {
		return refusedTransform(DamagedFenceApply)
	}
	journal := codexRoutingJournal{}
	catalogJournal := codexCatalogJournal{}
	providerJournal := codexProviderJournal{}
	if fenceLookup.Kind == FencedFound {
		journal = parseCodexRoutingJournal(fenceLookup.Region.Inner)
		catalogJournal = parseCodexCatalogJournal(fenceLookup.Region.Inner)
		providerJournal = parseCodexProviderJournal(fenceLookup.Region.Inner)
	}

	provider, hasProvider, parseable := codexRootModelProvider(current)
	if !parseable {
		return refusedTransform("prism: codex config has a root model_provider in a form prism cannot parse; apply left the file untouched")
	}
	if hasProvider && provider != "openai" && provider != "prism" {
		if !force {
			return forceableTransform(fmt.Sprintf("prism: codex config selects the external model_provider %q; apply left the file untouched", provider))
		}
		if provider == "prism" {
			return refusedTransform("prism: codex config provider selection is already prism; apply left the file untouched")
		}
		switched, displaced, ok := switchCodexProviderSelection(current)
		if !ok {
			return refusedTransform(fmt.Sprintf("prism: codex config selects the external model_provider %q in a form prism cannot switch; remove it first (file left untouched)", provider))
		}
		providerJournal.displaced = displaced
		current = switched
		hasProvider = false
	}

	next, journal, refusal := upsertCodexRootRouting(current, port, journal, hasProvider && provider == "prism", force)
	if refusal != "" {
		return forceableTransform(refusal)
	}

	if catalogPath != "" {
		next, refusal = upsertCodexRootCatalog(next, catalogPath, force, &catalogJournal)
		if refusal != "" {
			return forceableTransform(refusal)
		}
	}

	if len(providerJournal.displaced) > 0 {
		next, _ = removePrismRootPairs(next, catalogPath)
	}

	result := UpsertFencedBlock(next, CodexFence, codexManagedBlock(port, journal, catalogJournal, providerJournal))
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
			next, changedCatalog := restoreCodexRootCatalog(current, catalogPath, codexCatalogJournal{})
			return nextTransform(next, changedCatalog)
		}
		next, _ := RemoveFencedBlock(current, CodexFence)
		providerJournal := parseCodexProviderJournal(lookup.Region.Inner)
		if len(providerJournal.displaced) > 0 {
			next, _ = restoreCodexProviderSelection(next, providerJournal)
			return nextTransform(next, true)
		}
		journal := parseCodexRoutingJournal(lookup.Region.Inner)
		if journal.written != "" {
			next, _ = restoreCodexRootPair(next, journal)
		}
		if catalogPath != "" {
			next, _ = restoreCodexRootCatalog(next, catalogPath, parseCodexCatalogJournal(lookup.Region.Inner))
		}
		return nextTransform(next, true)
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
	return writeCodexConfig(options, false)
}

func WriteCodexConfigForced(options CodexOptions) WriteOutcome {
	return writeCodexConfig(options, true)
}

func writeCodexConfig(options CodexOptions, force bool) WriteOutcome {
	options = normalizeCodexOptions(options)
	io := withLocalIO(options.IO)
	models, refusal := resolveModels(options.Models, options.ModelsSource, Codex)
	if refusal != "" {
		return WriteOutcome{Kind: OutcomeRefused, Reason: refusal}
	}
	catalogPath := ""
	if len(models) > 0 {
		catalogPath = CodexCatalogPath(options.ConfigPath)
		if err := AtomicWrite(io, catalogPath, RenderCodexCatalog(models)); err != nil {
			return WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("codex apply", err)}
		}
	}
	outcome, err := ApplyConfigTransform(io, options.ConfigPath, codexTransform(options.Port, catalogPath, force), options.CrashBeforeRename)
	if err != nil {
		_ = io.Remove(catalogPath)
		return WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("codex apply", err)}
	}
	if outcome.Kind == OutcomeRefused {
		_ = io.Remove(catalogPath)
	}
	return outcome
}

func StripCodexConfig(io FileIO, configPath string) WriteOutcome {
	outcome, err := ApplyConfigTransform(io, configPath, codexRollbackTransform(CodexCatalogPath(configPath)), false)
	if err != nil {
		return WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("codex rollback", err)}
	}
	_ = io.Remove(CodexCatalogPath(configPath))
	return outcome
}

func RecoverCodexConfig(io FileIO, configPath string) bool {
	return io.RecoverStaged(configPath)
}

type CodexOptions struct {
	Port              int
	Models            []Model
	ModelsSource      func() []Model
	ConfigPath        string
	Env               Env
	Home              string
	IO                FileIO
	CrashBeforeRename bool
}

type CodexIntegration struct {
	mu         sync.Mutex
	id         ID
	port       int
	models     []Model
	modelsSrc  func() []Model
	io         FileIO
	configPath string
	env        Env
	home       string
}

func NewCodex(options CodexOptions) *CodexIntegration {
	options = normalizeCodexOptions(options)
	return &CodexIntegration{id: Codex, port: options.Port, models: options.Models, modelsSrc: options.ModelsSource, io: withLocalIO(options.IO), configPath: options.ConfigPath, env: options.Env, home: options.Home}
}

func normalizeCodexOptions(options CodexOptions) CodexOptions {
	if options.ConfigPath == "" {
		options.ConfigPath = CodexConfigPath(options.Env, options.Home)
	}
	return options
}

func (c *CodexIntegration) ID() ID { return c.id }

func (c *CodexIntegration) Apply() ApplyResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	return ToApplyResult(c.id, WriteCodexConfig(CodexOptions{Port: c.port, Models: c.models, ModelsSource: c.modelsSrc, ConfigPath: c.configPath, IO: c.io}))
}

func (c *CodexIntegration) ApplyForced() ApplyResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	return ToApplyResult(c.id, writeCodexConfig(CodexOptions{Port: c.port, Models: c.models, ModelsSource: c.modelsSrc, ConfigPath: c.configPath, IO: c.io}, true))
}

func (c *CodexIntegration) RefreshCatalog() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	models, refusal := resolveModels(c.models, c.modelsSrc, c.id)
	if refusal != "" {
		return errors.New(refusal)
	}
	if len(models) == 0 {
		return nil
	}
	return AtomicWrite(c.io, CodexCatalogPath(c.configPath), RenderCodexCatalog(models))
}

func (c *CodexIntegration) Status() Status {
	return ObservedIntegrationStatus(c.io, c.id, c.configPath, []string{CodexHome(c.env, c.home)}, func(path string) ManagedRead {
		content, ok := c.io.ReadTextIfExists(path)
		if !ok {
			return ManagedRead{Kind: ManagedAbsent}
		}
		return codexManagedRead(content)
	}, ProviderBaseUrl(c.port))
}

func (c *CodexIntegration) Rollback() ApplyResult {
	return ToRollbackResult(c.id, StripCodexConfig(c.io, c.configPath))
}
