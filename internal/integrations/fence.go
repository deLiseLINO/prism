package integrations

import (
	"strings"
)

type Fence struct {
	Begin string
	End   string
}

type FencedRegion struct {
	Start int
	End   int
	Inner string
}

const (
	FencedFound    = "found"
	FencedOrphaned = "orphaned"
	FencedAbsent   = "absent"
)

// FencedLookup locates the managed block. Absent means no markers at all;
// orphaned means the fence is damaged (begin without end, end without begin,
// or more than one of either), which callers must treat as fail-closed rather
// than guessing the block's extent.
type FencedLookup struct {
	Kind   string
	Region FencedRegion
}

const DamagedFenceApply = "prism: managed fence is damaged (orphaned markers); apply refuses to guess the block extent and left the file untouched"
const DamagedFenceRollback = "prism: managed fence is damaged (orphaned markers); rollback left the file untouched"

func FindFencedRegion(content string, fence Fence) FencedLookup {
	begins := indicesOf(content, fence.Begin)
	ends := indicesOf(content, fence.End)
	if len(begins) == 0 && len(ends) == 0 {
		return FencedLookup{Kind: FencedAbsent}
	}
	if len(begins) != 1 || len(ends) != 1 {
		return FencedLookup{Kind: FencedOrphaned}
	}
	start := begins[0]
	end := ends[0] + len(fence.End)
	if end <= start {
		return FencedLookup{Kind: FencedOrphaned}
	}
	return FencedLookup{Kind: FencedFound, Region: FencedRegion{Start: start, End: end, Inner: content[start:end]}}
}

func indicesOf(text string, needle string) []int {
	var found []int
	i := strings.Index(text, needle)
	for i != -1 {
		found = append(found, i)
		next := strings.Index(text[i+1:], needle)
		if next == -1 {
			i = -1
		} else {
			i = i + 1 + next
		}
	}
	return found
}

// RenderFencedBlock renders the marker lines plus the block body, exactly as the writer emits them.
func RenderFencedBlock(fence Fence, body string) string {
	return fence.Begin + "\n" + body + "\n" + fence.End
}

type FencedUpsert struct {
	Kind    string
	Next    string
	Changed bool
	Reason  string
}

// UpsertFencedBlock replaces or inserts the fenced block. The region between
// the markers is prism-owned: re-apply rewrites it in place (new model set,
// format change, port change), matching the reference injector's rewrite-in-
// place semantics. Only a damaged fence (a lone marker) refuses. The transform
// is injective against RemoveFencedBlock: insertion adds exactly one separator
// newline, so the pre-injection bytes are always recoverable verbatim.
func UpsertFencedBlock(content string, fence Fence, body string) FencedUpsert {
	canonical := RenderFencedBlock(fence, body)
	lookup := FindFencedRegion(content, fence)
	switch lookup.Kind {
	case FencedOrphaned:
		return FencedUpsert{Kind: "refused", Reason: DamagedFenceApply}
	case FencedFound:
		if lookup.Region.Inner == canonical {
			return FencedUpsert{Kind: "written", Next: content, Changed: false}
		}
		next := content[:lookup.Region.Start] + canonical + content[lookup.Region.End:]
		return FencedUpsert{Kind: "written", Next: next, Changed: true}
	}
	if len(content) == 0 {
		return FencedUpsert{Kind: "written", Next: canonical + "\n", Changed: true}
	}
	return FencedUpsert{Kind: "written", Next: content + "\n" + canonical + "\n", Changed: true}
}

// RemoveFencedBlock removes exactly the fenced block, undoing the
// separator-newline rule above. User bytes outside the block are preserved
// verbatim; a damaged fence is left untouched.
func RemoveFencedBlock(content string, fence Fence) (string, bool) {
	lookup := FindFencedRegion(content, fence)
	if lookup.Kind != FencedFound {
		return content, false
	}
	start, end := lookup.Region.Start, lookup.Region.End
	removalEnd := end
	if strings.HasPrefix(content[removalEnd:], "\n") {
		removalEnd++
	}
	prefix := content[:start]
	rest := content[removalEnd:]
	if strings.HasSuffix(prefix, "\n\n") {
		prefix = prefix[:len(prefix)-1]
	} else if len(rest) == 0 && strings.HasSuffix(prefix, "\n") {
		prefix = prefix[:len(prefix)-1]
	}
	return prefix + rest, true
}
