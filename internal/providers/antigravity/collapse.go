package antigravity

import (
	"regexp"
	"sort"
	"strings"
)

// Effort-tier variant collapsing
//  
// Upstream advertises sibling
// SKUs like {family}-{effort} / -tiered; prism collapses
// them into one logical model id with per-effort routing to the wire ids.
//
// The table is plain Go data, not a parsed vocabulary: no KDL, no new
// dependencies. The only files that know raw member ids are this one and
// models.go (discovery); the rest of prism stores and routes logical ids.

// effort is the antigravity-side effort domain, including "off" — the
// thinking-off choice, which is a routing target but never the default.
type effort string

const (
	effortOff     effort = "off"
	effortMinimal effort = "minimal"
	effortLow     effort = "low"
	effortMedium  effort = "medium"
	effortHigh    effort = "high"
	effortXHigh   effort = "xhigh"
	effortMax     effort = "max"
)

// family is one reviewed collapse family. members is priority order: the
// first live non-retired member is the default wire id unless defaultMember
// names one explicitly. routing maps effort -> raw member id; its "off" entry
// is the thinking-off wire choice, never the default. thinking is
// "google-level", "budget", or "" for surface-less families (keep reasoning,
// drop thinking config).
type family struct {
	id                         string
	name                       string
	members                    []string
	routing                    map[effort]string
	retired                    []string
	defaultMember              string
	thinking                   string
	suppressWhenOff            bool
	requiresEffort             bool
	preserveAbsentEffortRoutes bool
	aliases                    []string
	effortBudgets              map[effort]int
	efforts                    []effort
}

// families is the reviewed google-antigravity table
// the gemini-{rev}-flash template lives in templateFamilies.
var families = []*family{
	{
		id:      "gemini-3.5-flash",
		name:    "Gemini 3.5 Flash",
		members: []string{"gemini-3.5-flash-extra-low", "gemini-3.5-flash-low", "gemini-3-flash-agent"},
		routing: map[effort]string{
			effortOff:     "gemini-3.5-flash-extra-low",
			effortMinimal: "gemini-3.5-flash-extra-low",
			effortLow:     "gemini-3.5-flash-extra-low",
			effortMedium:  "gemini-3.5-flash-low",
			effortHigh:    "gemini-3-flash-agent",
		},
		thinking:        "budget",
		efforts:         []effort{effortMinimal, effortLow, effortMedium, effortHigh},
		effortBudgets:   map[effort]int{effortMinimal: 1000, effortLow: 1000, effortMedium: 4000, effortHigh: 10000},
		aliases:         []string{"gemini-3-flash"},
		suppressWhenOff: true,
	},
	{
		id:      "gemini-3.1-pro",
		name:    "Gemini 3.1 Pro",
		members: []string{"gemini-3.1-pro-low", "gemini-pro-agent", "gemini-3.1-pro-high"},
		routing: map[effort]string{
			effortOff:  "gemini-3.1-pro-low",
			effortLow:  "gemini-3.1-pro-low",
			effortHigh: "gemini-pro-agent",
		},
		thinking:        "budget",
		efforts:         []effort{effortLow, effortHigh},
		effortBudgets:   map[effort]int{effortLow: 1001, effortHigh: 10001},
		retired:         []string{"gemini-3.1-pro-high"},
		suppressWhenOff: true,
	},
	{
		id:      "gemini-3-pro",
		name:    "Gemini 3 Pro",
		members: []string{"gemini-3-pro-low", "gemini-3-pro-high"},
		routing: map[effort]string{
			effortOff:  "gemini-3-pro-low",
			effortLow:  "gemini-3-pro-low",
			effortHigh: "gemini-3-pro-high",
		},
		thinking:        "google-level",
		efforts:         []effort{effortLow, effortHigh},
		suppressWhenOff: true,
	},
	{
		id:       "gpt-oss-120b",
		name:     "GPT-OSS 120B",
		members:  []string{"gpt-oss-120b-medium"},
		thinking: "budget",
		efforts:  []effort{effortMinimal, effortLow, effortMedium, effortHigh},
	},
	{
		id:       "claude-sonnet-4-6",
		name:     "Claude Sonnet 4.6",
		members:  []string{"claude-sonnet-4-6", "claude-sonnet-4-6-thinking"},
		thinking: "budget",
		efforts:  []effort{effortMinimal, effortLow, effortMedium, effortHigh},
		retired:  []string{"claude-sonnet-4-6-thinking"},
	},
	{
		id:       "claude-opus-4-6",
		name:     "Claude Opus 4.6",
		members:  []string{"claude-opus-4-6-thinking", "claude-opus-4-6"},
		thinking: "budget",
		efforts:  []effort{effortMinimal, effortLow, effortMedium, effortHigh},
		retired:  []string{"claude-opus-4-6"},
	},
	{
		id:      "claude-sonnet-4-5",
		name:    "Claude Sonnet 4.5",
		members: []string{"claude-sonnet-4-5", "claude-sonnet-4-5-thinking"},
		routing: map[effort]string{
			effortOff:     "claude-sonnet-4-5",
			effortMinimal: "claude-sonnet-4-5-thinking",
			effortLow:     "claude-sonnet-4-5-thinking",
			effortMedium:  "claude-sonnet-4-5-thinking",
			effortHigh:    "claude-sonnet-4-5-thinking",
		},
		thinking:                   "budget",
		efforts:                    []effort{effortMinimal, effortLow, effortMedium, effortHigh},
		preserveAbsentEffortRoutes: true,
	},
	{
		id:      "claude-opus-4-5",
		name:    "Claude Opus 4.5",
		members: []string{"claude-opus-4-5", "claude-opus-4-5-thinking"},
		routing: map[effort]string{
			effortOff:     "claude-opus-4-5",
			effortMinimal: "claude-opus-4-5-thinking",
			effortLow:     "claude-opus-4-5-thinking",
			effortMedium:  "claude-opus-4-5-thinking",
			effortHigh:    "claude-opus-4-5-thinking",
		},
		thinking:                   "budget",
		efforts:                    []effort{effortMinimal, effortLow, effortMedium, effortHigh},
		preserveAbsentEffortRoutes: true,
	},
	{
		id:      "gemini-2.5-flash",
		name:    "Gemini 2.5 Flash",
		members: []string{"gemini-2.5-flash", "gemini-2.5-flash-thinking"},
		routing: map[effort]string{
			effortOff:     "gemini-2.5-flash",
			effortMinimal: "gemini-2.5-flash-thinking",
			effortLow:     "gemini-2.5-flash-thinking",
			effortMedium:  "gemini-2.5-flash-thinking",
			effortHigh:    "gemini-2.5-flash-thinking",
		},
		thinking:                   "budget",
		efforts:                    []effort{effortMinimal, effortLow, effortMedium, effortHigh},
		preserveAbsentEffortRoutes: true,
	},
}

// templateFamily is the gemini-{rev}-flash revision template
//  
// One concrete family is instantiated per revision discovered.
type templateFamily struct {
	family
	minRevision [3]int
}

var geminiFlashTemplate = templateFamily{
	family: family{
		id:      "gemini-{rev}-flash",
		name:    "Gemini {rev} Flash",
		members: []string{"gemini-{rev}-flash-low", "gemini-{rev}-flash-medium", "gemini-{rev}-flash-high", "gemini-{rev}-flash-tiered"},
		routing: map[effort]string{
			effortMinimal: "gemini-{rev}-flash-low",
			effortLow:     "gemini-{rev}-flash-low",
			effortMedium:  "gemini-{rev}-flash-medium",
			effortHigh:    "gemini-{rev}-flash-high",
		},
		thinking:       "google-level",
		efforts:        []effort{effortMinimal, effortLow, effortMedium, effortHigh},
		requiresEffort: true,
	},
	minRevision: [3]int{3, 6, 0},
}

// revisionCapture mirrors upstream's `(\d+(?:\.\d+){0,2})` capture on every
// templated id (id, members, aliases) as an anchored case-insensitive
// alternation. Instantiate substitutes {rev} everywhere.
var revisionCapture = regexp.MustCompile(`(\d+(?:\.\d+){0,2})`)

// templatePattern builds the anchored alternation for one templated id. Each
// literal part is escaped; {rev} becomes the capture.
func templatePattern(id string) *regexp.Regexp {
	parts := strings.Split(id, "{rev}")
	for i, part := range parts {
		parts[i] = regexp.QuoteMeta(part)
	}
	return regexp.MustCompile(`^(?:` + strings.Join(parts, `(\d+(?:\.\d+){0,2})`) + `)$`)
}

// templatePatterns are the alternations for the template's id and members, in
// declaration order; the first capture group in a match is the revision.
var templatePatterns = func() []*regexp.Regexp {
	t := &geminiFlashTemplate.family
	ids := append([]string{t.id}, t.members...)
	ids = append(ids, t.aliases...)
	out := make([]*regexp.Regexp, 0, len(ids))
	for _, id := range ids {
		out = append(out, templatePattern(id))
	}
	return out
}()

// compareRevision is lexicographic triple comparison.
func compareRevision(a, b [3]int) int {
	for i := range 3 {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// parseRevision parses one to three dot-separated components, each an
// unsigned byte value; omitted components are zero. Returns ok=false on
// malformed input.
func parseRevision(value string) (rev [3]int, ok bool) {
	parts := strings.Split(value, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return rev, false
	}
	for i, part := range parts {
		if part == "" {
			return rev, false
		}
		n := 0
		for _, r := range part {
			if r < '0' || r > '9' {
				return rev, false
			}
			n = n*10 + int(r-'0')
			if n > 255 {
				return rev, false
			}
		}
		rev[i] = n
	}
	return rev, true
}

// fill substitutes the revision for {rev} throughout one templated id.
func fill(id, rev string) string {
	return strings.ReplaceAll(id, "{rev}", rev)
}

// instantiate yields the concrete family for a revision, or nil when the
// revision is below the template's constraint.
func (t *templateFamily) instantiate(rev string) *family {
	parsed, ok := parseRevision(rev)
	if !ok || compareRevision(parsed, t.minRevision) < 0 {
		return nil
	}
	src := &t.family
	out := &family{
		id:                         fill(src.id, rev),
		name:                       fill(src.name, rev),
		members:                    make([]string, len(src.members)),
		routing:                    make(map[effort]string, len(src.routing)),
		retired:                    nil,
		defaultMember:              fillIfSet(src.defaultMember, rev),
		thinking:                   src.thinking,
		suppressWhenOff:            src.suppressWhenOff,
		requiresEffort:             src.requiresEffort,
		preserveAbsentEffortRoutes: src.preserveAbsentEffortRoutes,
		aliases:                    nil,
		effortBudgets:              nil,
		efforts:                    src.efforts,
	}
	for i, m := range src.members {
		out.members[i] = fill(m, rev)
	}
	for k, v := range src.routing {
		out.routing[k] = fill(v, rev)
	}
	if len(src.aliases) > 0 {
		out.aliases = make([]string, len(src.aliases))
		for i, a := range src.aliases {
			out.aliases[i] = fill(a, rev)
		}
	}
	if len(src.retired) > 0 {
		out.retired = make([]string, len(src.retired))
		for i, r := range src.retired {
			out.retired[i] = fill(r, rev)
		}
	}
	return out
}

func fillIfSet(id, rev string) string {
	if id == "" {
		return ""
	}
	return fill(id, rev)
}

// instantiateTemplates returns one concrete family per unique revision
// discovered in ids, skipping revisions whose family id a concrete family in
// the table already owns.
func instantiateTemplates(ids []string) []*family {
	seen := make(map[string]bool, len(families))
	for _, f := range families {
		seen[f.id] = true
	}
	var out []*family
	for _, id := range ids {
		rev, ok := templateRevision(id)
		if !ok {
			continue
		}
		f := geminiFlashTemplate.instantiate(rev)
		if f == nil || seen[f.id] {
			continue
		}
		seen[f.id] = true
		out = append(out, f)
	}
	return out
}

// templateRevision matches an id against the template's alternation (id,
// members, aliases) and extracts the revision capture.
func templateRevision(id string) (string, bool) {
	for _, pattern := range templatePatterns {
		m := pattern.FindStringSubmatch(strings.ToLower(id))
		if m == nil {
			continue
		}
		for _, g := range m[1:] {
			if g != "" {
				return g, true
			}
		}
	}
	return "", false
}

// pairTokens are the thinking-variant suffix tokens for auto-pair derivation.
var pairTokens = []string{"thinking", "reasoning", "reasoner"}

// defaultPairEfforts is the fallback surface when neither pair member carries
// thinking metadata (prism never has discovery metadata, so it always applies).
var defaultPairEfforts = []effort{effortMinimal, effortLow, effortMedium, effortHigh}

// stripThinkingVariantSuffix removes the first thinking-variant suffix token
// from a model id: trailing or separator-following only (a token run into by
// an alphanumeric char is not a variant), and never when the preceding word
// is a negation ("non", "no"). Returns ok=false when no variant suffix strips.
func stripThinkingVariantSuffix(model string) (base string, ok bool) {
	lower := strings.ToLower(model)
	for _, token := range pairTokens {
		needle := "-" + token
		searchFrom := 0
		for searchFrom < len(lower) {
			index := strings.Index(lower[searchFrom:], needle)
			if index == -1 {
				break
			}
			index += searchFrom
			end := index + len(needle)
			followedByAlnum := false
			if end < len(lower) {
				c := lower[end]
				followedByAlnum = (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z')
			}
			wordStart := index
			for wordStart > 0 {
				c := lower[wordStart-1]
				if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z')) {
					break
				}
				wordStart--
			}
			preceding := lower[wordStart:index]
			if !followedByAlnum && preceding != "non" && preceding != "no" {
				stripped := model[:index] + model[end:]
				if stripped != "" {
					return stripped, true
				}
			}
			searchFrom = index + 1
		}
	}
	return "", false
}

// familyClaims reports whether id is owned by the reviewed table: a concrete
// family id, a member, an alias, or an id a template would instantiate.
func familyClaims(ids []string, id string) bool {
	for _, f := range allFamilies(ids) {
		if f.id == id {
			return true
		}
		for _, m := range f.members {
			if m == id {
				return true
			}
		}
		for _, a := range f.aliases {
			if a == id {
				return true
			}
		}
	}
	return false
}

// derivePairFamilies is the global automatic rule: derive an X + X-thinking
// family for every pair where both ids are live in the discovery list. Ids
// claimed by the reviewed table are skipped (curation wins); the api-match and
// price-match gates are upstream-only (prism has no metadata at discovery).
// The surface is the budget default: off routes to the bare id, every
// supported effort routes to the thinking id.
func derivePairFamilies(ids []string) []*family {
	byID := make(map[string]bool, len(ids))
	for _, id := range ids {
		byID[id] = true
	}
	var out []*family
	for _, id := range ids {
		base, ok := stripThinkingVariantSuffix(id)
		if !ok || base == id {
			continue
		}
		if !byID[base] {
			continue
		}
		if familyClaims(ids, id) || familyClaims(ids, base) {
			continue
		}
		routing := map[effort]string{effortOff: base}
		for _, e := range defaultPairEfforts {
			routing[e] = id
		}
		out = append(out, &family{
			id:       base,
			name:     base,
			members:  []string{base, id},
			routing:  routing,
			thinking: "budget",
			efforts:  defaultPairEfforts,
		})
	}
	return out
}

// allFamilies returns the concrete table plus the families the discovery ids
// instantiate from the template.
func allFamilies(ids []string) []*family {
	instantiated := instantiateTemplates(ids)
	if len(instantiated) == 0 {
		return families
	}
	out := make([]*family, 0, len(families)+len(instantiated))
	out = append(out, families...)
	out = append(out, instantiated...)
	return out
}

// collapseIds runs the collapse main loop over a raw discovery list: each
// family with live members is emitted once at its first occurrence as the
// logical id, raw member ids are deduped away, and families with no live
// members are inert (bare ids pass through untouched). A family id that
// appears in the raw list alongside live members maps to the collapsed row
// too (mixed input: the collapsed entry wins). The output preserves input
// order; callers sort when they need sorted output. Routing survival and
// default-wire selection are presence-sensitive at request time and live in
// resolveWireModel.
func collapseIds(raw []string) []string {
	present := make(map[string]bool, len(raw))
	for _, id := range raw {
		present[id] = true
	}
	// logicalOf maps every family-owned id (live members, and the family id
	// itself when present) to the logical id that replaces it.
	logicalOf := make(map[string]string)
	for _, f := range allFamilies(raw) {
		// Upstream excludes the family id from rawPresent only when the input
		// already carries a collapsed entry for it; a raw discovery list never
		// does, so a bare member (claude-sonnet-4-5) counts as live and the
		// family collapses on it alone.
		live := false
		for _, m := range f.members {
			if present[m] {
				live = true
				break
			}
		}
		if !live {
			continue
		}
		for _, m := range f.members {
			if present[m] {
				logicalOf[m] = f.id
			}
		}
		if present[f.id] {
			logicalOf[f.id] = f.id
		}
	}
	// Auto-pair families collapse after the table; derivation already skips
	// ids the table claims (curation wins), so their mappings never collide
	// with the table's. Two variants of one base (X-thinking and X-reasoning)
	// derive the same logical id and simply agree on it.
	for _, f := range derivePairFamilies(raw) {
		for _, m := range f.members {
			logicalOf[m] = f.id
		}
	}
	// Emit: each family's logical id replaces the first occurrence of any of
	// its owned ids; later occurrences dedupe away; untouched ids pass
	// through.
	done := make(map[string]bool)
	out := make([]string, 0, len(raw))
	for _, id := range raw {
		logical, owned := logicalOf[id]
		if !owned {
			out = append(out, id)
			continue
		}
		if done[logical] {
			continue
		}
		done[logical] = true
		out = append(out, logical)
	}
	return out
}

// familyForLogical returns the collapse family a logical id resolves against
// the present map (template families need the discovery list to instantiate).
// Derived pair families are found only when their members are present, which
// mirrors how they were derived at collapse time. The returned family is
// shared table data; callers must not mutate it.
func familyForLogical(logical string, present map[string]bool) *family {
	ids := make([]string, 0, len(present))
	for id := range present {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, f := range allFamilies(ids) {
		if f.id == logical {
			return f
		}
	}
	for _, f := range derivePairFamilies(ids) {
		if f.id == logical {
			return f
		}
	}
	return nil
}

// resolveWireModel returns the raw wire id a logical model routes to at the
// given effort. present is the last-discovery presence map; an empty present
// map makes every family inert and the logical id passes through unchanged.
// A routing entry applies iff its target is declared, present (or the effort
// is not "off" and the family preserves absent effort routes), and not
// retired — the same survival rule the collapse loop applies. Anything else
// falls back to the family's default wire id; the envelope clamps requiresEffort
// efforts before an unset effort ever reaches the default.
func resolveWireModel(logical string, ef effort, present map[string]bool) string {
	if len(present) == 0 {
		return logical
	}
	f := familyForLogical(logical, present)
	if f == nil {
		return logical
	}
	retired := retiredSet(f)
	if target, ok := f.routing[ef]; ok {
		targetPresent := present[target]
		preserveAbsent := ef != effortOff && f.preserveAbsentEffortRoutes
		if (targetPresent || preserveAbsent) && !retired[target] {
			return target
		}
	}
	if wire := defaultWireFor(f, present); wire != "" {
		return wire
	}
	return logical
}

// effortsFor returns the supported efforts for a logical model, lowest first,
// or nil when the model is not a family (no surface to show). Template
// families (gemini-{rev}-flash) match by id shape with their declared
// revision constraint.
func effortsFor(logical string) []effort {
	for _, f := range families {
		if f.id == logical {
			return f.efforts
		}
	}
	if rev, ok := templateRevision(logical); ok {
		if f := geminiFlashTemplate.instantiate(rev); f != nil {
			return f.efforts
		}
	}
	return nil
}

// rawMembersFor returns the live raw member wire ids for a logical model:
// non-retired members filtered by the present map when it is non-empty, else
// all non-retired members. Discovery-populated presence keeps the list
// faithful to what the account advertises; the table default serves the
// pre-sync and stale-snapshot cases.
func rawMembersFor(logical string, present map[string]bool) []string {
	var f *family
	if len(present) == 0 {
		f = familyForLogicalNoPresent(logical)
	} else {
		f = familyForLogical(logical, present)
	}
	if f == nil {
		return nil
	}
	retired := retiredSet(f)
	var out []string
	for _, m := range f.members {
		if retired[m] {
			continue
		}
		if len(present) > 0 && !present[m] && m != f.id {
			continue
		}
		if !containsString(out, m) {
			out = append(out, m)
		}
	}
	return out
}

// familyForLogicalNoPresent resolves a family without discovery presence:
// the concrete table plus any family the template can instantiate from the
// logical id itself.
func familyForLogicalNoPresent(logical string) *family {
	for _, f := range families {
		if f.id == logical {
			return f
		}
	}
	if rev, ok := templateRevision(logical); ok {
		return geminiFlashTemplate.instantiate(rev)
	}
	return nil
}

func retiredSet(f *family) map[string]bool {
	retired := make(map[string]bool, len(f.retired))
	for _, r := range f.retired {
		retired[r] = true
	}
	return retired
}

// defaultWireFor applies the reviewed-table default-member rule against the
// present map.
func defaultWireFor(f *family, present map[string]bool) string {
	retired := retiredSet(f)
	if f.defaultMember != "" && present[f.defaultMember] && !retired[f.defaultMember] {
		return f.defaultMember
	}
	for _, m := range f.members {
		if present[m] && !retired[m] {
			return m
		}
	}
	for _, m := range f.members {
		if present[m] {
			return m
		}
	}
	return ""
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
