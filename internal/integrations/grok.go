package integrations

import (
	"fmt"
	"regexp"
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
		tables = append(tables, strings.Join(lines, "\n"))
	}
	return strings.Join(tables, "\n\n")
}

func grokTransform(models []Model, port int) func(current string) ConfigTransform {
	return func(current string) ConfigTransform {
		migrated, migratedChanged := removePrismLegacyTables(current, GrokFence)
		userTables := UserModelTables(migrated, GrokFence)
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
			return refusedTransform("prism: grok apply refused — emitted " + strings.Join(parts, ", ") + " collides with a user-owned model table outside the prism fence")
		}
		result := UpsertFencedBlock(migrated, GrokFence, GrokManagedBlock(port, models))
		if result.Kind == "written" {
			return nextTransform(result.Next, result.Changed || migratedChanged)
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
		next, changed := RemoveFencedBlock(current, GrokFence)
		return nextTransform(next, changed)
	}
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
	outcome, err := ApplyConfigTransform(options.ConfigPath, grokTransform(options.Models, options.Port), options.CrashBeforeRename)
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

func (g *GrokIntegration) currentModels() []Model {
	if g.modelsSrc != nil {
		if models := g.modelsSrc(); len(models) > 0 {
			return models
		}
	}
	return g.models
}

func NewGrok(options GrokOptions) *GrokIntegration {
	if options.ConfigPath == "" {
		options.ConfigPath = GrokConfigPath(options.Env, options.Home)
	}
	return &GrokIntegration{id: Grok, port: options.Port, models: options.Models, modelsSrc: options.ModelsSource, configPath: options.ConfigPath, env: options.Env, home: options.Home}
}

func (g *GrokIntegration) ID() ID { return g.id }

func (g *GrokIntegration) Apply() ApplyResult {
	return ToApplyResult(g.id, WriteGrokConfig(GrokOptions{Port: g.port, Models: g.currentModels(), ConfigPath: g.configPath}))
}

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
