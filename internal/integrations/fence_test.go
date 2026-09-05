package integrations

import (
	"strings"
	"testing"
)

// userTOML is the shared Codex seed the fence must preserve byte-for-byte.
const userTOML = "# user configuration\n" +
	"top_setting = \"keep\"\n" +
	"\n" +
	"[profile.default]\n" +
	"model = \"gpt-5.2\"\n" +
	"\n"

const prismBlockBody = `[model_providers.prism]
name = "prism"
base_url = "http://127.0.0.1:9999/v1"
wire_api = "responses"`

func TestFencedAbsent(t *testing.T) {
	got := FindFencedRegion("hello world", CodexFence)
	if got.Kind != FencedAbsent {
		t.Fatalf("expected absent, got %s", got.Kind)
	}
}

func TestFencedOrphanedTooManyBegins(t *testing.T) {
	content := CodexFence.Begin + "x" + CodexFence.End + "\n" + CodexFence.Begin + "y" + CodexFence.End
	got := FindFencedRegion(content, CodexFence)
	if got.Kind != FencedOrphaned {
		t.Fatalf("expected orphaned, got %s", got.Kind)
	}
}

func TestFencedOrphanedBeginWithoutEnd(t *testing.T) {
	content := "user\n" + CodexFence.Begin + "\nbody without an end"
	got := FindFencedRegion(content, CodexFence)
	if got.Kind != FencedOrphaned {
		t.Fatalf("expected orphaned, got %s", got.Kind)
	}
}

func TestFencedFound(t *testing.T) {
	content := "user\n" + CodexFence.Begin + "\nbody\n" + CodexFence.End + "\nafter"
	got := FindFencedRegion(content, CodexFence)
	if got.Kind != FencedFound {
		t.Fatalf("expected found, got %s", got.Kind)
	}
	if !strings.Contains(got.Region.Inner, "body") {
		t.Fatalf("inner missing body: %q", got.Region.Inner)
	}
}

func TestUpsertFencedBlockEmpty(t *testing.T) {
	got := UpsertFencedBlock("", CodexFence, "body")
	if got.Kind != "written" || !got.Changed {
		t.Fatalf("expected written+changed on empty, got %+v", got)
	}
	if !strings.HasPrefix(got.Next, CodexFence.Begin) {
		t.Fatalf("expected begin at start: %q", got.Next)
	}
}

func TestUpsertFencedBlockSeparatesWithOneNewline(t *testing.T) {
	got := UpsertFencedBlock("user bytes", CodexFence, "body")
	if !strings.Contains(got.Next, "user bytes\n"+CodexFence.Begin) {
		t.Fatalf("expected single separator newline: %q", got.Next)
	}
}

func TestUpsertRemoveRoundTripPreservesMissingFinalLF(t *testing.T) {
	seeds := []string{
		"user bytes",
		"user bytes\n",
		"user bytes\n\n",
		"",
	}
	for _, seed := range seeds {
		upsert := UpsertFencedBlock(seed, CodexFence, prismBlockBody)
		if upsert.Kind != "written" {
			t.Fatalf("upsert %q: %+v", seed, upsert)
		}
		next, changed := RemoveFencedBlock(upsert.Next, CodexFence)
		if !changed {
			t.Fatalf("remove %q: expected changed", seed)
		}
		if next != seed {
			t.Fatalf("round trip not byte-identical for %q:\nwant: %q\ngot:  %q", seed, seed, next)
		}
	}
}

func TestUpsertFencedBlockIdempotent(t *testing.T) {
	first := UpsertFencedBlock(userTOML, CodexFence, prismBlockBody)
	if first.Kind != "written" || !first.Changed {
		t.Fatalf("expected first write: %+v", first)
	}
	second := UpsertFencedBlock(first.Next, CodexFence, prismBlockBody)
	if second.Kind != "written" || second.Changed {
		t.Fatalf("expected no change on reapply: %+v", second)
	}
}

func TestUpsertFencedBlockRewritesOwnRegion(t *testing.T) {
	first := UpsertFencedBlock(userTOML, CodexFence, prismBlockBody)
	edited := strings.Replace(first.Next, CodexFence.Begin, CodexFence.Begin+"\nuser_edit = true", 1)
	got := UpsertFencedBlock(edited, CodexFence, prismBlockBody)
	if got.Kind != "written" || !got.Changed {
		t.Fatalf("expected rewrite-in-place, got %+v", got)
	}
	if !strings.Contains(got.Next, userTOML) || strings.Contains(got.Next, "user_edit") {
		t.Fatalf("rewrite lost user bytes or kept foreign edit:\n%s", got.Next)
	}
	again := UpsertFencedBlock(got.Next, CodexFence, prismBlockBody)
	if again.Changed {
		t.Fatal("rewrite is not idempotent")
	}
}

func TestUpsertFencedBlockRefusesOrphaned(t *testing.T) {
	damaged := userTOML + CodexFence.Begin + "\n[model_providers.stale]\n"
	got := UpsertFencedBlock(damaged, CodexFence, prismBlockBody)
	if got.Kind != "refused" || !strings.Contains(got.Reason, "damaged") {
		t.Fatalf("expected damaged refusal, got %+v", got)
	}
}

func TestRemoveFencedBlockRoundTrip(t *testing.T) {
	upsert := UpsertFencedBlock(userTOML, CodexFence, prismBlockBody)
	if upsert.Kind != "written" {
		t.Fatalf("expected written, got %+v", upsert)
	}
	next, changed := RemoveFencedBlock(upsert.Next, CodexFence)
	if !changed {
		t.Fatalf("expected changed=true on rollback")
	}
	if next != userTOML {
		t.Fatalf("round trip not byte-identical:\nwant: %q\ngot:  %q", userTOML, next)
	}
}

func TestRemoveFencedBlockMissingIsNoop(t *testing.T) {
	next, changed := RemoveFencedBlock(userTOML, CodexFence)
	if changed || next != userTOML {
		t.Fatalf("expected noop, got changed=%v next=%q", changed, next)
	}
}
