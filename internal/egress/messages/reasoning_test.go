package messages

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"prism/internal/canon"
	"prism/internal/reasonenv"
)

type flushRecorder struct {
	bytes.Buffer
	flushes int
}

func TestIdlePingKeepsWireWarm(t *testing.T) {
	oldEvery, oldIdle := pingEvery, pingIdleAfter
	pingEvery, pingIdleAfter = 20*time.Millisecond, 40*time.Millisecond
	t.Cleanup(func() { pingEvery, pingIdleAfter = oldEvery, oldIdle })
	rec := &flushRecorder{}
	eg := New(rec, true)
	enc := eg.(*streamEncoder)
	if err := eg.Begin(ResponseHeader{ID: "msg_1", Model: "claude-prism-codex--gpt-5"}); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	written := func() string {
		enc.writeMu.Lock()
		defer enc.writeMu.Unlock()
		return rec.String()
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(written(), "event: ping") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(written(), "event: ping") {
		t.Fatalf("no ping observed on idle wire: %s", written())
	}
	if err := eg.Frame(canon.TurnFinished{Status: canon.Completed()}); err != nil {
		t.Fatalf("Frame: %v", err)
	}
	if err := eg.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
}

func (f *flushRecorder) Flush() { f.flushes++ }

func TestPerFrameFlush(t *testing.T) {
	rec := &flushRecorder{}
	eg := New(rec, true)
	if err := eg.Begin(ResponseHeader{ID: "msg_1", Model: "claude-prism-codex--gpt-5"}); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	afterStart := rec.flushes
	if afterStart == 0 {
		t.Fatalf("message_start was not flushed")
	}
	if err := eg.Frame(canon.ItemStarted{Item: canon.Message{ID: "t1", Role: canon.RoleAssistant}}); err != nil {
		t.Fatalf("Frame: %v", err)
	}
	if rec.flushes == afterStart {
		t.Fatalf("content_block_start was not flushed")
	}
	if err := eg.Frame(canon.ItemFinished{Item: canon.Message{ID: "t1", Role: canon.RoleAssistant}}); err != nil {
		t.Fatalf("Frame: %v", err)
	}
	if err := eg.Frame(canon.TurnFinished{Status: canon.Completed()}); err != nil {
		t.Fatalf("Frame: %v", err)
	}
	if err := eg.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
}

func TestRedactedThinkingBlockEmitted(t *testing.T) {
	sig := reasonenv.EncodeRedacted([]string{"opaque-blob"})
	out, _ := runStream(t,
		ResponseHeader{ID: "msg_1", Model: "claude-prism-codex--gpt-5"},
		[]canon.Event{
			canon.ItemStarted{Item: canon.ReasoningItem{ID: "r1", Signature: sig}},
			canon.ItemFinished{Item: canon.ReasoningItem{ID: "r1", Signature: sig}},
			canon.TurnFinished{Status: canon.Completed()},
		})
	if !strings.Contains(out, `"redacted_thinking"`) || !strings.Contains(out, "opaque-blob") {
		t.Fatalf("redacted block not emitted: %s", out)
	}
	if strings.Contains(out, "signature_delta") {
		t.Fatalf("redacted block must not carry a signature_delta: %s", out)
	}
}
