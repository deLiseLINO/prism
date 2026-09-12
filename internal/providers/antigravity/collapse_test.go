package antigravity

import (
	"reflect"
	"testing"
)

func presentOf(ids ...string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

func TestCollapseTableEachFamily(t *testing.T) {
	cases := []struct {
		name string
		raw  []string
		want []string
	}{
		{
			// A live alias row (gemini-3-flash) is never claimed by the emission
			// loop: upstream maps only members into familyIdBySpecId, so the
			// alias passes through as its own row ahead of the collapsed one.
			name: "gemini-3.5-flash members collapse; live alias row passes through",
			raw:  []string{"gemini-3-flash", "gemini-3.5-flash-extra-low", "gemini-3.5-flash-low", "gemini-3-flash-agent"},
			want: []string{"gemini-3-flash", "gemini-3.5-flash"},
		},
		{
			name: "gemini-3.1-pro collapses, retired member deduped",
			raw:  []string{"gemini-3.1-pro-low", "gemini-pro-agent", "gemini-3.1-pro-high"},
			want: []string{"gemini-3.1-pro"},
		},
		{
			name: "gemini-3-pro collapses",
			raw:  []string{"gemini-3-pro-low", "gemini-3-pro-high"},
			want: []string{"gemini-3-pro"},
		},
		{
			name: "gpt-oss-120b single member collapses",
			raw:  []string{"gpt-oss-120b-medium"},
			want: []string{"gpt-oss-120b"},
		},
		{
			name: "claude-sonnet-4-6 with retired thinking member",
			raw:  []string{"claude-sonnet-4-6", "claude-sonnet-4-6-thinking"},
			want: []string{"claude-sonnet-4-6"},
		},
		{
			name: "claude-opus-4-6 with retired bare member",
			raw:  []string{"claude-opus-4-6-thinking", "claude-opus-4-6"},
			want: []string{"claude-opus-4-6"},
		},
		{
			name: "claude-sonnet-4-5 bare and thinking collapse",
			raw:  []string{"claude-sonnet-4-5", "claude-sonnet-4-5-thinking"},
			want: []string{"claude-sonnet-4-5"},
		},
		{
			name: "claude-opus-4-5 bare and thinking collapse",
			raw:  []string{"claude-opus-4-5", "claude-opus-4-5-thinking"},
			want: []string{"claude-opus-4-5"},
		},
		{
			name: "gemini-2.5-flash bare and thinking collapse",
			raw:  []string{"gemini-2.5-flash", "gemini-2.5-flash-thinking"},
			want: []string{"gemini-2.5-flash"},
		},
		{
			name: "template family gemini-3.7-flash from members",
			raw:  []string{"gemini-3.7-flash-low", "gemini-3.7-flash-medium", "gemini-3.7-flash-high", "gemini-3.7-flash-tiered"},
			want: []string{"gemini-3.7-flash"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := collapseIds(tc.raw)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("collapseIds(%v) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestCollapseInertRule(t *testing.T) {
	// Bare family id with no members present: inert, passes through untouched.
	// Output preserves input order; callers sort when they need sorted output.
	got := collapseIds([]string{"gemini-3.7-flash", "claude-opus-4"})
	want := []string{"gemini-3.7-flash", "claude-opus-4"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("inert family must pass through: got %v want %v", got, want)
	}
	// Non-family ids pass through untouched.
	got = collapseIds([]string{"some-random-model", "another-one"})
	want = []string{"some-random-model", "another-one"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("non-family ids must pass through: got %v want %v", got, want)
	}
}

func TestCollapseMixedInputCollapsedEntryWins(t *testing.T) {
	// The logical id plus raw members in one list: the collapsed entry wins,
	// raw members dedupe away.
	raw := []string{"claude-opus-4-5-thinking", "claude-opus-4-5", "claude-opus-4-5-thinking"}
	got := collapseIds(raw)
	want := []string{"claude-opus-4-5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mixed input: got %v want %v", got, want)
	}
}

func TestCollapsePreservesOrderAndUntouchedIds(t *testing.T) {
	raw := []string{
		"zzz-other-model",
		"gemini-3-pro-high",
		"aaa-other-model",
		"gemini-3-pro-low",
	}
	got := collapseIds(raw)
	want := []string{"zzz-other-model", "gemini-3-pro", "aaa-other-model"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order: got %v want %v", got, want)
	}
}

func TestCollapseDefaultMemberRule(t *testing.T) {
	// resolveWireModel on a family whose default derives from members order.
	present := presentOf("gemini-3.1-pro-low", "gemini-pro-agent", "gemini-3.1-pro-high")
	// Members order: -low first live non-retired member is the default.
	if got := resolveWireModel("gemini-3.1-pro", effortOff, present); got != "gemini-3.1-pro-low" {
		t.Fatalf("off route: got %q want gemini-3.1-pro-low", got)
	}
	if got := resolveWireModel("gemini-3.1-pro", effortLow, present); got != "gemini-3.1-pro-low" {
		t.Fatalf("low route: got %q want gemini-3.1-pro-low", got)
	}
	if got := resolveWireModel("gemini-3.1-pro", effortHigh, present); got != "gemini-pro-agent" {
		t.Fatalf("high route: got %q want gemini-pro-agent", got)
	}
	// Retired target dropped from routing: high routes to the agent member,
	// and the retired -high never becomes the default even when present.
	if got := resolveWireModel("gemini-3.1-pro", effortMedium, present); got != "gemini-3.1-pro-low" {
		// medium has no route; falls back to default wire (-low).
		t.Fatalf("medium unroute: got %q want gemini-3.1-pro-low", got)
	}
}

func TestResolveWireModelRoutingDropsAbsentTargets(t *testing.T) {
	// gemini-3.5-flash: minimal/low route to extra-low, but only -low is
	// present: absent targets drop from routing, so low falls back to the
	// default wire (first live member, gemini-3.5-flash-low).
	present := presentOf("gemini-3.5-flash-low")
	if got := resolveWireModel("gemini-3.5-flash", effortLow, present); got != "gemini-3.5-flash-low" {
		t.Fatalf("absent target drop: got %q want gemini-3.5-flash-low", got)
	}
	// preserveAbsentEffortRoutes: claude-sonnet-4-5 routes efforts to the
	// thinking id even when only the bare id is present... but routing's off
	// entry (the bare id) survives and effort routes survive via the flag.
	present = presentOf("claude-sonnet-4-5")
	if got := resolveWireModel("claude-sonnet-4-5", effortMedium, present); got != "claude-sonnet-4-5-thinking" {
		t.Fatalf("preserve-absent: got %q want claude-sonnet-4-5-thinking", got)
	}
	if got := resolveWireModel("claude-sonnet-4-5", effortOff, present); got != "claude-sonnet-4-5" {
		t.Fatalf("off route preserved: got %q want claude-sonnet-4-5", got)
	}
}

func TestResolveWireModelNonFamilyPassthrough(t *testing.T) {
	present := presentOf("some-model")
	if got := resolveWireModel("some-model", effortHigh, present); got != "some-model" {
		t.Fatalf("non-family must pass through: got %q", got)
	}
	// Empty presence map: family treated as inert, bare id passes through.
	if got := resolveWireModel("gemini-3-pro", effortHigh, presentOf()); got != "gemini-3-pro" {
		t.Fatalf("empty present must be inert: got %q", got)
	}
}

func TestResolveWireModelTemplateFamily(t *testing.T) {
	present := presentOf("gemini-3.7-flash-low", "gemini-3.7-flash-medium", "gemini-3.7-flash-high", "gemini-3.7-flash-tiered")
	if got := resolveWireModel("gemini-3.7-flash", effortMinimal, present); got != "gemini-3.7-flash-low" {
		t.Fatalf("minimal: got %q", got)
	}
	if got := resolveWireModel("gemini-3.7-flash", effortLow, present); got != "gemini-3.7-flash-low" {
		t.Fatalf("low: got %q", got)
	}
	if got := resolveWireModel("gemini-3.7-flash", effortMedium, present); got != "gemini-3.7-flash-medium" {
		t.Fatalf("medium: got %q", got)
	}
	if got := resolveWireModel("gemini-3.7-flash", effortHigh, present); got != "gemini-3.7-flash-high" {
		t.Fatalf("high: got %q", got)
	}
	// Off: no off route declared; requiresEffort families never route off to
	// a thinking-off wire id — the default wire (first live member) serves.
	if got := resolveWireModel("gemini-3.7-flash", effortOff, present); got != "gemini-3.7-flash-low" {
		t.Fatalf("off on requiresEffort: got %q want default wire", got)
	}
}

func TestResolveWireModelRetiredTargetAlwaysDropped(t *testing.T) {
	// claude-opus-4-6: members -thinking then bare; bare is retired. Routing
	// carries no entries, so every effort falls to the default wire: the only
	// live non-retired member is claude-opus-4-6-thinking.
	present := presentOf("claude-opus-4-6-thinking", "claude-opus-4-6")
	if got := resolveWireModel("claude-opus-4-6", effortHigh, present); got != "claude-opus-4-6-thinking" {
		t.Fatalf("retired never default: got %q want claude-opus-4-6-thinking", got)
	}
}

func TestTemplateRevisionConstraint(t *testing.T) {
	// rev 3.5 < 3.6 must not instantiate; the bare id stays untouched.
	raw := []string{"gemini-3.5-flash-low"}
	// Note: gemini-3.5-flash is a concrete family too; use a rev the concrete
	// table does not own. 3.2: below the >=3.6 constraint.
	got := collapseIds([]string{"gemini-3.2-flash-low", "gemini-3.2-flash-medium"})
	want := []string{"gemini-3.2-flash-low", "gemini-3.2-flash-medium"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("below-constraint revision must not instantiate: got %v want %v", got, want)
	}
	_ = raw
	// rev 4 satisfies >=3.6.
	got = collapseIds([]string{"gemini-4-flash-low", "gemini-4-flash-high"})
	want = []string{"gemini-4-flash"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rev 4 must instantiate: got %v want %v", got, want)
	}
}

func TestCollapseAutoPair(t *testing.T) {
	// An X + X-thinking pair not claimed by the table derives a family.
	got := collapseIds([]string{"grok-5", "grok-5-thinking"})
	want := []string{"grok-5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("auto pair: got %v want %v", got, want)
	}
	// Off routes to the bare id; efforts route to the thinking id.
	present := presentOf("grok-5", "grok-5-thinking")
	if got := resolveWireModel("grok-5", effortOff, present); got != "grok-5" {
		t.Fatalf("pair off: got %q", got)
	}
	if got := resolveWireModel("grok-5", effortHigh, present); got != "grok-5-thinking" {
		t.Fatalf("pair high: got %q", got)
	}
}

func TestCollapseAutoPairSkippedWhenClaimed(t *testing.T) {
	// claude-sonnet-4-5-thinking is claimed by the reviewed table; no pair
	// derivation double-collapse. The table family wins (id stays logical).
	got := collapseIds([]string{"claude-sonnet-4-5", "claude-sonnet-4-5-thinking"})
	want := []string{"claude-sonnet-4-5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("claimed pair: got %v want %v", got, want)
	}
}

func TestCollapseAutoPairNegationAndInfixRules(t *testing.T) {
	// "non-thinking" is a negation, not a variant: no pair derivation.
	got := collapseIds([]string{"model-x", "model-x-non-thinking"})
	want := []string{"model-x", "model-x-non-thinking"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("negation must not pair: got %v want %v", got, want)
	}
	// Infix token pairs when both ids are live: "some-thinking-model" strips
	// to "some-model".
	got = collapseIds([]string{"some-model", "some-thinking-model"})
	want = []string{"some-model"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("infix pair: got %v want %v", got, want)
	}
	// A token followed by an alphanumeric char is not a variant suffix
	// (thinking2 is a distinct word, not the -thinking token).
	got = collapseIds([]string{"model-y", "model-y-thinking2"})
	want = []string{"model-y", "model-y-thinking2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("alnum-follow must not pair: got %v want %v", got, want)
	}
}

func TestStripThinkingVariantSuffix(t *testing.T) {
	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"grok-5-thinking", "grok-5", true},
		{"some-thinking-model", "some-model", true},
		{"model-x-non-thinking", "", false},
		{"model-x-no-thinking", "", false},
		{"model-y-thinking2", "", false},
		{"plain-model", "", false},
	}
	for _, tc := range cases {
		got, ok := stripThinkingVariantSuffix(tc.in)
		if ok != tc.wantOK || (ok && got != tc.want) {
			t.Fatalf("strip(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestEffortsFor(t *testing.T) {
	if got := effortsFor("gemini-3-pro"); !reflect.DeepEqual(got, []effort{effortLow, effortHigh}) {
		t.Fatalf("gemini-3-pro efforts: %v", got)
	}
	if got := effortsFor("gemini-3.5-flash"); !reflect.DeepEqual(got, []effort{effortMinimal, effortLow, effortMedium, effortHigh}) {
		t.Fatalf("gemini-3.5-flash efforts: %v", got)
	}
	// Template families resolve by id shape under the revision constraint.
	if got := effortsFor("gemini-3.7-flash"); !reflect.DeepEqual(got, []effort{effortMinimal, effortLow, effortMedium, effortHigh}) {
		t.Fatalf("gemini-3.7-flash efforts: %v", got)
	}
	if got := effortsFor("gemini-3.2-flash"); got != nil {
		t.Fatalf("below-constraint template efforts must be nil: %v", got)
	}
	if got := effortsFor("not-a-family"); got != nil {
		t.Fatalf("non-family efforts must be nil: %v", got)
	}
}

func TestRawMembersFor(t *testing.T) {
	// Present map filters live members; retired members never appear.
	present := presentOf("gemini-3.1-pro-low", "gemini-pro-agent", "gemini-3.1-pro-high")
	got := rawMembersFor("gemini-3.1-pro", present)
	want := []string{"gemini-3.1-pro-low", "gemini-pro-agent"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rawMembersFor present-filtered: got %v want %v", got, want)
	}
	// Empty present map: all non-retired members (table default).
	got = rawMembersFor("gemini-3.1-pro", presentOf())
	want = []string{"gemini-3.1-pro-low", "gemini-pro-agent"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rawMembersFor table default: got %v want %v", got, want)
	}
	// Non-family: nil.
	if got := rawMembersFor("some-model", presentOf("some-model")); got != nil {
		t.Fatalf("non-family raw members must be nil: %v", got)
	}
	// Template family resolves against discovery presence.
	present = presentOf("gemini-3.7-flash-low", "gemini-3.7-flash-medium", "gemini-3.7-flash-high", "gemini-3.7-flash-tiered")
	got = rawMembersFor("gemini-3.7-flash", present)
	want = []string{"gemini-3.7-flash-low", "gemini-3.7-flash-medium", "gemini-3.7-flash-high", "gemini-3.7-flash-tiered"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("template rawMembersFor: got %v want %v", got, want)
	}
}
