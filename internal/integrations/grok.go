package integrations

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var GrokFence = Fence{
	Begin: "# >>> prism managed block (grok) — do not edit (removed by prism rollback) >>>",
	End:   "# <<< prism managed block (grok) <<<",
}

const prismApiKey = "prism-loopback"

const keySegment = `(?:[A-Za-z0-9_-]+|"(?:[^"\\]|\\.)*"|'[^']*')`

var modelTableHeaderRe = regexp.MustCompile(
	`(?m)^[ \t]*\[\[?[ \t]*(` + keySegment + `)[ \t]*\.[ \t]*(` + keySegment + `)[ \t]*(?:\.[^\]\r\n]*)?\]\]?[ \t]*(?:#.*)?$`,
)

var tomlEscapeRe = regexp.MustCompile(`\\(u[0-9A-Fa-f]{4}|U[0-9A-Fa-f]{8}|.)`)

func decodeTomlBasicString(body string) string {
	return tomlEscapeRe.ReplaceAllStringFunc(body, func(match string) string {
		esc := match[1:]
		switch esc[0] {
		case 'u':
			code, err := strconv.ParseUint(esc[1:], 16, 32)
			if err != nil {
				return match
			}
			return string(rune(code))
		case 'U':
			code, err := strconv.ParseUint(esc[1:], 16, 32)
			if err != nil || code > 0x10ffff {
				return match
			}
			return string(rune(code))
		}
		switch esc {
		case "b":
			return "\b"
		case "t":
			return "\t"
		case "n":
			return "\n"
		case "f":
			return "\f"
		case "r":
			return "\r"
		case `"`:
			return `"`
		case "\\":
			return "\\"
		}
		return match
	})
}

func canonicalKeySegment(raw string) string {
	if strings.HasPrefix(raw, `"`) {
		return decodeTomlBasicString(raw[1 : len(raw)-1])
	}
	if strings.HasPrefix(raw, "'") {
		return raw[1 : len(raw)-1]
	}
	return raw
}

type GrokOptions struct {
	Port              int
	Models            []Model
	ModelsSource      func() []Model
	ConfigPath        string
	Env               Env
	Home              string
	CrashBeforeRename bool
}

var grokAliasSanitizeRe = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// GrokAlias sanitizes the model id into a prism alias, skipping reserved names.
func GrokAlias(modelID string, reserved map[string]bool) string {
	base := "prism-" + grokAliasSanitizeRe.ReplaceAllString(modelID, "-")
	if !reserved[base] {
		return base
	}
	count := 1
	for reserved[fmt.Sprintf("%s-%d", base, count+1)] {
		count++
	}
	return fmt.Sprintf("%s-%d", base, count+1)
}

// GrokAliases returns the aliases one apply emits, deduplicated among themselves only.
func GrokAliases(models []Model) []string {
	taken := map[string]bool{}
	aliases := make([]string, 0, len(models))
	for _, model := range models {
		alias := GrokAlias(model.ID, taken)
		taken[alias] = true
		aliases = append(aliases, alias)
	}
	return aliases
}

// UserModelTables collects aliases of `[model.<alias>]` (or `[[model.<alias>]]`)
// tables outside the prism fence, with TOML key spellings canonicalized so
// bare, basic-string, and literal-string segments address the same table.
func UserModelTables(content string, fence Fence) map[string]bool {
	lookup := FindFencedRegion(content, fence)
	inFence := lookup.Kind == FencedFound
	owned := map[string]bool{}
	for _, loc := range modelTableHeaderRe.FindAllStringSubmatchIndex(content, -1) {
		if canonicalKeySegment(content[loc[2]:loc[3]]) != "model" {
			continue
		}
		headerStart, headerEnd := loc[0], loc[1]
		if inFence && headerStart >= lookup.Region.Start && headerEnd <= lookup.Region.End {
			continue
		}
		owned[canonicalKeySegment(content[loc[4]:loc[5]])] = true
	}
	return owned
}

var prismLoopbackKeyRe = regexp.MustCompile(`(?m)^[ \t]*api_key[ \t]*=[ \t]*"prism-loopback"[ \t]*(?:#.*)?$`)
var prismGrokHeaderRe = regexp.MustCompile(`(?m)^[ \t]*x-prism-grok[ \t]*=[ \t]*"?1"?[ \t]*(?:#.*)?$`)

type modelTableSpan struct {
	start int
	end   int
	body  string
}

func modelTableSpans(content string) []modelTableSpan {
	headers := modelTableHeaderRe.FindAllStringSubmatchIndex(content, -1)
	spans := make([]modelTableSpan, 0, len(headers))
	for hi, loc := range headers {
		if canonicalKeySegment(content[loc[2]:loc[3]]) != "model" {
			continue
		}
		end := len(content)
		for nh := hi + 1; nh < len(headers); nh++ {
			if headers[nh][0] >= loc[1] {
				end = headers[nh][0]
				break
			}
		}
		spans = append(spans, modelTableSpan{start: loc[0], end: end, body: content[loc[0]:end]})
	}
	return spans
}

// removePrismLegacyTables deletes pre-fence `[model.<alias>]` tables that
// Prism itself wrote before fenced blocks existed, identified by their
// prism-loopback credentials rather than their names, so user tables with
// prism-shaped names stay untouched.
func removePrismLegacyTables(content string, fence Fence) (string, bool) {
	lookup := FindFencedRegion(content, fence)
	inFence := lookup.Kind == FencedFound
	spans := modelTableSpans(content)
	var out strings.Builder
	prev := 0
	removed := false
	for _, span := range spans {
		if inFence && span.start >= lookup.Region.Start && span.start < lookup.Region.End {
			continue
		}
		end := span.end
		if inFence && span.start < lookup.Region.Start && end > lookup.Region.Start {
			end = lookup.Region.Start
		}
		body := content[span.start:end]
		if !prismLoopbackKeyRe.MatchString(body) && !prismGrokHeaderRe.MatchString(body) {
			continue
		}
		out.WriteString(content[prev:span.start])
		prev = end
		removed = true
	}
	if !removed {
		return content, false
	}
	out.WriteString(content[prev:])
	return out.String(), true
}

func GrokManagedBlock(port int, models []Model) string {
	baseURL := ProviderBaseUrl(port)
	taken := map[string]bool{}
	tables := make([]string, 0, len(models))
	for _, model := range models {
		alias := GrokAlias(model.ID, taken)
		taken[alias] = true
		lines := []string{
			"[model." + alias + "]",
			"model = " + tomlString(model.ID),
			"base_url = " + tomlString(baseURL),
			`api_backend = "responses"`,
			"api_key = " + tomlString(prismApiKey),
			"name = " + tomlString("prism "+model.Name),
			// Grok forwards extra_headers verbatim on inference calls; prismd
			// classifies the client as grok by this marker.
			`extra_headers = { "x-prism-grok" = "1" }`,
		}
		if model.ContextWindow > 0 {
			lines = append(lines, fmt.Sprintf("context_window = %d", model.ContextWindow))
		}
		efforts := effortsFor(model, responsesEffortVocabulary)
		if rung := defaultEffort(efforts, model.DefaultReasoningEffort); rung != "" {
			// The daemon steers reasoning effort on the responses wire for
			// every model, so the picker menu is honest; rungs stay inside
			// grok's vocabulary or the CLI rejects them. Array-of-tables must
			// follow every parent keyval above.
			lines = append(lines,
				"supports_reasoning_effort = true",
				"reasoning_effort = "+tomlString(rung),
			)
			for _, effort := range efforts {
				meta := grokEffortMeta[effort]
				lines = append(lines,
					"",
					"[[model."+alias+".reasoning_efforts]]",
					"id = "+tomlString(effort),
					"value = "+tomlString(effort),
					"label = "+tomlString(meta.label),
					"description = "+tomlString(meta.description),
					fmt.Sprintf("default = %t", effort == rung),
				)
			}
		}
		tables = append(tables, strings.Join(lines, "\n"))
	}
	return strings.Join(tables, "\n\n")
}

// grokEffortMeta carries the picker copy for each accepted rung, mirroring
// the menu shape grok renders from [[model.*.reasoning_efforts]] tables.
var grokEffortMeta = map[string]struct {
	label       string
	description string
}{
	"off":    {"Off", "No reasoning"},
	"low":    {"Low", "Quick, fast implementations"},
	"medium": {"Medium", "Balanced effort"},
	"high":   {"High", "Highest quality with extensive reasoning"},
	"xhigh":  {"XHigh", "Extra high reasoning effort"},
	"max":    {"Max", "Maximum reasoning effort"},
}

// grokRenamesHeader journals the user-owned tables renamed by a confirmed
// grok apply, so rollback restores their original names verbatim.
const grokRenamesHeader = "# prism-renames"

func grokTransform(models []Model, port int, force bool) func(current string) ConfigTransform {
	return func(current string) ConfigTransform {
		migrated, migratedChanged := removePrismLegacyTables(current, GrokFence)
		userTables := UserModelTables(migrated, GrokFence)
		renames := map[string]string{}
		if force {
			taken := map[string]bool{}
			for alias := range userTables {
				taken[alias] = true
			}
			for _, alias := range GrokAliases(models) {
				taken[alias] = true
			}
			for _, alias := range GrokAliases(models) {
				if userTables[alias] {
					renamed := alias + "-user"
					n := 2
					for taken[renamed] {
						renamed = fmt.Sprintf("%s-user-%d", alias, n)
						n++
					}
					taken[renamed] = true
					renames[alias] = renamed
				}
			}
			if len(renames) > 0 {
				migrated = renameUserModelTables(migrated, GrokFence, renames)
			}
		} else {
			var collisions []string
			for _, alias := range GrokAliases(models) {
				if userTables[alias] {
					collisions = append(collisions, alias)
				}
			}
			if len(collisions) > 0 {
				parts := make([]string, len(collisions))
				for i, alias := range collisions {
					parts[i] = "[model." + alias + "]"
				}
				return forceableTransform("prism: grok apply refused — emitted " + strings.Join(parts, ", ") + " collides with a user-owned model table outside the prism fence")
			}
		}
		result := UpsertFencedBlock(migrated, GrokFence, GrokManagedBlock(port, models)+renderGrokRenamesJournal(renames))
		if result.Kind == "written" {
			return nextTransform(result.Next, result.Changed || migratedChanged || len(renames) > 0)
		}
		return refusedTransform(result.Reason)
	}
}

func grokRollbackTransform() func(current string) ConfigTransform {
	return func(current string) ConfigTransform {
		lookup := FindFencedRegion(current, GrokFence)
		if lookup.Kind == FencedOrphaned {
			return refusedTransform(DamagedFenceRollback)
		}
		renames := parseGrokRenamesJournal(current)
		next, changed := RemoveFencedBlock(current, GrokFence)
		if len(renames) > 0 {
			next = restoreUserModelTables(next, renames)
			changed = true
		}
		return nextTransform(next, changed)
	}
}

// renameUserModelTables renames user-owned [model.<alias>] headers outside
// the fence, matching bare, basic-string, and literal-string spellings.
func renameUserModelTables(content string, fence Fence, renames map[string]string) string {
	if len(renames) == 0 {
		return content
	}
	lookup := FindFencedRegion(content, fence)
	inFence := lookup.Kind == FencedFound
	locs := modelTableHeaderRe.FindAllStringSubmatchIndex(content, -1)
	var b strings.Builder
	prev := 0
	for _, loc := range locs {
		if canonicalKeySegment(content[loc[2]:loc[3]]) != "model" {
			continue
		}
		alias := canonicalKeySegment(content[loc[4]:loc[5]])
		renamed, ok := renames[alias]
		if !ok {
			continue
		}
		headerStart := loc[0]
		if inFence && headerStart >= lookup.Region.Start && headerStart < lookup.Region.End {
			continue
		}
		b.WriteString(content[prev:loc[4]])
		b.WriteString(renamed)
		prev = loc[5]
	}
	b.WriteString(content[prev:])
	return b.String()
}

func renderGrokRenamesJournal(renames map[string]string) string {
	if len(renames) == 0 {
		return ""
	}
	aliases := make([]string, 0, len(renames))
	for alias := range renames {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	lines := []string{"", grokRenamesHeader}
	for _, alias := range aliases {
		lines = append(lines, "# renamed: "+alias+" -> "+renames[alias])
	}
	return strings.Join(lines, "\n")
}

func parseGrokRenamesJournal(content string) map[string]string {
	lookup := FindFencedRegion(content, GrokFence)
	if lookup.Kind != FencedFound {
		return nil
	}
	renames := map[string]string{}
	for _, line := range strings.Split(lookup.Region.Inner, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "# renamed:") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "# renamed:"))
		parts := strings.Split(rest, " -> ")
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			renames[parts[0]] = parts[1]
		}
	}
	if len(renames) == 0 {
		return nil
	}
	return renames
}

func restoreUserModelTables(content string, renames map[string]string) string {
	reverse := make(map[string]string, len(renames))
	for from, to := range renames {
		reverse[to] = from
	}
	locs := modelTableHeaderRe.FindAllStringSubmatchIndex(content, -1)
	var b strings.Builder
	prev := 0
	for _, loc := range locs {
		if canonicalKeySegment(content[loc[2]:loc[3]]) != "model" {
			continue
		}
		alias := canonicalKeySegment(content[loc[4]:loc[5]])
		original, ok := reverse[alias]
		if !ok {
			continue
		}
		b.WriteString(content[prev:loc[4]])
		b.WriteString(original)
		prev = loc[5]
	}
	b.WriteString(content[prev:])
	return b.String()
}

func grokManagedRead(content string) ManagedRead {
	lookup := FindFencedRegion(content, GrokFence)
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

func WriteGrokConfig(options GrokOptions) WriteOutcome {
	return writeGrokConfig(options, false)
}

func WriteGrokConfigForced(options GrokOptions) WriteOutcome {
	return writeGrokConfig(options, true)
}

func writeGrokConfig(options GrokOptions, force bool) WriteOutcome {
	models, refusal := resolveModels(options.Models, options.ModelsSource, Grok)
	if refusal != "" {
		return WriteOutcome{Kind: OutcomeRefused, Reason: refusal}
	}
	outcome, err := ApplyConfigTransform(options.ConfigPath, grokTransform(models, options.Port, force), options.CrashBeforeRename)
	if err != nil {
		return WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("grok apply", err)}
	}
	return outcome
}

func StripGrokConfig(configPath string) WriteOutcome {
	outcome, err := ApplyConfigTransform(configPath, grokRollbackTransform(), false)
	if err != nil {
		return WriteOutcome{Kind: OutcomeRefused, Reason: failureReason("grok rollback", err)}
	}
	return outcome
}

func RecoverGrokConfig(configPath string) bool {
	return RecoverStaged(configPath)
}

type GrokIntegration struct {
	id         ID
	port       int
	models     []Model
	modelsSrc  func() []Model
	configPath string
	env        Env
	home       string
}

func (g *GrokIntegration) Apply() ApplyResult {
	return ToApplyResult(g.id, WriteGrokConfig(GrokOptions{Port: g.port, Models: g.models, ModelsSource: g.modelsSrc, ConfigPath: g.configPath}))
}

func (g *GrokIntegration) ApplyForced() ApplyResult {
	return ToApplyResult(g.id, WriteGrokConfigForced(GrokOptions{Port: g.port, Models: g.models, ModelsSource: g.modelsSrc, ConfigPath: g.configPath}))
}

func NewGrok(options GrokOptions) *GrokIntegration {
	if options.ConfigPath == "" {
		options.ConfigPath = GrokConfigPath(options.Env, options.Home)
	}
	return &GrokIntegration{id: Grok, port: options.Port, models: options.Models, modelsSrc: options.ModelsSource, configPath: options.ConfigPath, env: options.Env, home: options.Home}
}

func (g *GrokIntegration) ID() ID { return g.id }

func (g *GrokIntegration) Status() Status {
	return ObservedIntegrationStatus(g.id, g.configPath, []string{GrokHome(g.env, g.home)}, func(path string) ManagedRead {
		content, ok := ReadTextIfExists(path)
		if !ok {
			return ManagedRead{Kind: ManagedAbsent}
		}
		return grokManagedRead(content)
	}, ProviderBaseUrl(g.port))
}

func (g *GrokIntegration) Rollback() ApplyResult {
	return ToRollbackResult(g.id, StripGrokConfig(g.configPath))
}
