// Package wiretest pumps canonical event scripts through every client-facing
// egress and reads the bytes back with strict, independent per-protocol
// readers.
package wiretest

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/canon"
	"github.com/deLiseLINO/prism/internal/egress"
	egresschat "github.com/deLiseLINO/prism/internal/egress/chat"
	egressmessages "github.com/deLiseLINO/prism/internal/egress/messages"
	egressresponses "github.com/deLiseLINO/prism/internal/egress/responses"
	"github.com/deLiseLINO/prism/internal/execution"
)

type wire struct {
	name   string
	stream bool
	run    func(t *testing.T, events []canon.Event) ([]byte, error)
	read   func([]byte) (outcome, error)
}

var created = time.Unix(1700000000, 0)

func wires() []wire {
	return []wire{
		{"responses/stream", true, func(t *testing.T, evs []canon.Event) ([]byte, error) {
			var b bytes.Buffer
			e := egressresponses.New(&b, execution.Facts{Client: execution.ClientCodex}, nil)
			defer e.Close()
			return pump(&b, func() error { return e.Begin(egress.ResponseHeader{ID: "resp_1", Model: "m", CreatedAt: created}) }, e.Frame, e.Flush, evs)
		}, readResponsesStream},
		{"responses/json", false, func(t *testing.T, evs []canon.Event) ([]byte, error) {
			var b bytes.Buffer
			e := egressresponses.NewBuffered(&b, execution.Facts{Client: execution.ClientCodex}, nil)
			defer e.Close()
			return pump(&b, func() error { return e.Begin(egress.ResponseHeader{ID: "resp_1", Model: "m", CreatedAt: created}) }, e.Frame, e.Flush, evs)
		}, readResponsesJSON},
		{"chat/stream", true, func(t *testing.T, evs []canon.Event) ([]byte, error) {
			var b bytes.Buffer
			e := egresschat.New(&b, true)
			return pump(&b, func() error {
				return e.Begin(egresschat.ResponseHeader{ID: "chatcmpl_1", Model: "m", CreatedAt: created})
			}, e.Frame, e.Flush, evs)
		}, readChatStream},
		{"chat/json", false, func(t *testing.T, evs []canon.Event) ([]byte, error) {
			var b bytes.Buffer
			e := egresschat.New(&b, false)
			return pump(&b, func() error {
				return e.Begin(egresschat.ResponseHeader{ID: "chatcmpl_1", Model: "m", CreatedAt: created})
			}, e.Frame, e.Flush, evs)
		}, readChatJSON},
		{"messages/stream", true, func(t *testing.T, evs []canon.Event) ([]byte, error) {
			var b bytes.Buffer
			e := egressmessages.New(&b, true)
			defer e.Close()
			return pump(&b, func() error {
				return e.Begin(egressmessages.ResponseHeader{ID: "msg_1", Model: "m", CreatedAt: created})
			}, e.Frame, e.Flush, evs)
		}, readMessagesStream},
		{"messages/json", false, func(t *testing.T, evs []canon.Event) ([]byte, error) {
			var b bytes.Buffer
			e := egressmessages.New(&b, false)
			defer e.Close()
			return pump(&b, func() error {
				return e.Begin(egressmessages.ResponseHeader{ID: "msg_1", Model: "m", CreatedAt: created})
			}, e.Frame, e.Flush, evs)
		}, readMessagesJSON},
	}
}

// pump drives an egress the way the server pipeline does: provider events via
// Frame (errors are the provider's problem and abort the script), then the
// routing terminal and Flush.
func pump(b *bytes.Buffer, begin func() error, frame func(canon.Event) error, flush func() error, evs []canon.Event) ([]byte, error) {
	if err := begin(); err != nil {
		return nil, err
	}
	for _, ev := range evs {
		if err := frame(ev); err != nil {
			return b.Bytes(), err
		}
	}
	if err := flush(); err != nil {
		return b.Bytes(), err
	}
	return b.Bytes(), nil
}

func TestEveryEgressIsWireValidForEveryScenario(t *testing.T) {
	for _, sc := range scenarios() {
		for _, w := range wires() {
			t.Run(sc.Name+"/"+w.name, func(t *testing.T) {
				raw, err := w.run(t, sc.Events)
				if err != nil {
					t.Fatalf("egress rejected a valid canonical script: %v", err)
				}
				got, err := w.read(raw)
				if err != nil {
					t.Fatalf("strict reader: %v\n--- wire ---\n%s", err, clip(raw))
				}
				assertOutcome(t, sc, w, got, raw)
			})
		}
	}
}

func clip(b []byte) string {
	if len(b) > 3000 {
		return string(b[:1500]) + "\n…\n" + string(b[len(b)-1500:])
	}
	return string(b)
}

func assertOutcome(t *testing.T, sc scenario, w wire, got outcome, raw []byte) {
	t.Helper()
	fail := func(format string, a ...any) {
		t.Helper()
		t.Errorf(format+"\n--- wire ---\n%s", append(a, clip(raw))...)
	}
	wantFailed := sc.Want.Failed
	if sc.Name == "upstream-stall-mid-text" && (strings.HasPrefix(w.name, "chat") || strings.HasPrefix(w.name, "messages")) {
		wantFailed = true
	}
	if got.Failed != wantFailed {
		fail("failed = %v, want %v", got.Failed, sc.Want.Failed)
		return
	}
	if wantFailed {
		return
	}
	if got.Text != sc.Want.Text {
		fail("text = %q, want %q", trunc(got.Text), trunc(sc.Want.Text))
	}
	if got.Reasoning != sc.Want.Reasoning {
		fail("reasoning = %q, want %q", got.Reasoning, sc.Want.Reasoning)
	}
	if len(got.Tools) != len(sc.Want.Tools) {
		fail("tools = %+v, want %+v", got.Tools, sc.Want.Tools)
	} else {
		for i := range got.Tools {
			if got.Tools[i] != sc.Want.Tools[i] {
				fail("tool %d = %+v, want %+v", i, got.Tools[i], sc.Want.Tools[i])
			}
		}
	}
	wantStop := sc.Want.Stop
	if wantStop == "stall" {
		wantStop = "" // no wire has a dedicated class; only wire validity is asserted
	}
	if wantStop == "filter" && strings.HasPrefix(w.name, "messages") {
		wantStop = "end" // the Messages wire has no content-filter stop reason the SDKs all accept
	}
	if wantStop != "" && got.Stop != wantStop {
		fail("stop = %q, want %q", got.Stop, wantStop)
	}
	u := sc.Want.Usage
	if u == (canon.Usage{}) {
		return
	}
	if !got.Usage.Present && !strings.HasPrefix(w.name, "chat/stream") {
		fail("usage missing")
		return
	}
	if got.Usage.Out != u.OutputTokens {
		fail("output tokens = %d, want %d", got.Usage.Out, u.OutputTokens)
	}
	if strings.HasPrefix(w.name, "messages") {
		// Messages reports input_tokens without the cached part; clients sum
		// input_tokens + cache_read to get the prompt size.
		if got.Usage.Cached != u.CachedInputTokens || got.Usage.In+got.Usage.Cached != u.InputTokens {
			fail("input_tokens %d + cache_read %d, want prompt size %d", got.Usage.In, got.Usage.Cached, u.InputTokens)
		}
		return
	}
	if got.Usage.In != u.InputTokens || got.Usage.Cached != u.CachedInputTokens || got.Usage.Reason != u.ReasoningTokens || got.Usage.Tot != u.TotalTokens {
		fail("usage = %+v, want %+v", got.Usage, u)
	}
}
