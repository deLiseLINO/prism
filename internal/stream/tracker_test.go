package stream

import (
	"errors"
	"testing"
	"time"

	"prism/internal/canon"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func messageItem(id canon.ItemID) canon.Message {
	return canon.Message{ID: id, Role: canon.RoleAssistant, Content: []canon.Content{canon.TextContent{Text: "hi"}}}
}

func TestItemStartsBeforeDeltas(t *testing.T) {
	tr := NewTrackerWithClock(&fakeClock{now: time.Unix(0, 0)})
	err := tr.Apply(canon.TextDelta{ItemID: "m1", Text: "a"})
	if !errors.Is(err, ErrItemNotStarted) {
		t.Fatalf("delta before start: got %v, want ErrItemNotStarted", err)
	}
	if err := tr.Apply(canon.ItemStarted{Item: messageItem("m1")}); err != nil {
		t.Fatalf("ItemStarted: %v", err)
	}
	if err := tr.Apply(canon.TextDelta{ItemID: "m1", Text: "a"}); err != nil {
		t.Fatalf("delta after start: %v", err)
	}
}

func TestItemFinishesAtMostOnce(t *testing.T) {
	tr := NewTrackerWithClock(&fakeClock{now: time.Unix(0, 0)})
	if err := tr.Apply(canon.ItemStarted{Item: messageItem("m1")}); err != nil {
		t.Fatalf("ItemStarted: %v", err)
	}
	if err := tr.Apply(canon.ItemFinished{Item: messageItem("m1")}); err != nil {
		t.Fatalf("first ItemFinished: %v", err)
	}
	err := tr.Apply(canon.ItemFinished{Item: messageItem("m1")})
	if !errors.Is(err, ErrItemAlreadyClosed) {
		t.Fatalf("second ItemFinished: got %v, want ErrItemAlreadyClosed", err)
	}
	if got := tr.ActiveItems(); len(got) != 0 {
		t.Fatalf("ActiveItems after finish: got %v, want empty", got)
	}
}

func TestOneTerminalPerTurn(t *testing.T) {
	tr := NewTrackerWithClock(&fakeClock{now: time.Unix(0, 0)})
	fin := canon.TurnFinished{Status: canon.Completed()}
	if err := tr.Apply(fin); err != nil {
		t.Fatalf("TurnFinished: %v", err)
	}
	err := tr.Apply(canon.TurnFailed{Failure: canon.Failure{Reason: canon.FailUnknown}})
	if !errors.Is(err, ErrTerminalRecorded) {
		t.Fatalf("second terminal: got %v, want ErrTerminalRecorded", err)
	}
	got, ok := tr.Terminal()
	if !ok {
		t.Fatal("Terminal: want recorded terminal")
	}
	if _, isTurnFinished := got.(canon.TurnFinished); !isTurnFinished {
		t.Fatalf("Terminal: got %T, want canon.TurnFinished", got)
	}
}

func TestNoEventsAfterTerminal(t *testing.T) {
	tr := NewTrackerWithClock(&fakeClock{now: time.Unix(0, 0)})
	if err := tr.Apply(canon.TurnFinished{Status: canon.Completed()}); err != nil {
		t.Fatalf("TurnFinished: %v", err)
	}
	err := tr.Apply(canon.ItemStarted{Item: messageItem("m1")})
	if !errors.Is(err, ErrTerminalRecorded) {
		t.Fatalf("Apply after terminal: got %v, want ErrTerminalRecorded", err)
	}
	err = tr.Apply(canon.TurnFinished{Status: canon.Completed()})
	if !errors.Is(err, ErrTerminalRecorded) {
		t.Fatalf("second terminal after terminal: got %v, want ErrTerminalRecorded", err)
	}
}

func TestClientDisconnectCancelsTurn(t *testing.T) {
	tr := NewTrackerWithClock(&fakeClock{now: time.Unix(0, 0)})
	if err := tr.Apply(canon.ItemStarted{Item: messageItem("m1")}); err != nil {
		t.Fatalf("ItemStarted: %v", err)
	}
	ev, synthesized := tr.OnClientDisconnect()
	if !synthesized {
		t.Fatal("OnClientDisconnect: want synthesized terminal")
	}
	fin, ok := ev.(canon.TurnFinished)
	if !ok {
		t.Fatalf("OnClientDisconnect: got %T, want canon.TurnFinished", ev)
	}
	reason, hasReason := fin.Status.Reason()
	if !hasReason || reason != canon.IncompleteClientDisconnected {
		t.Fatalf("disconnect status: got reason=%v ok=%v, want IncompleteClientDisconnected", reason, hasReason)
	}
	if ids := tr.ActiveItems(); len(ids) != 1 || ids[0] != "m1" {
		t.Fatalf("ActiveItems after disconnect: got %v, want [m1]", ids)
	}
	err := tr.Apply(canon.TextDelta{ItemID: "m1", Text: "late"})
	if !errors.Is(err, ErrTerminalRecorded) {
		t.Fatalf("Apply after disconnect terminal: got %v, want ErrTerminalRecorded", err)
	}
}

func TestUpstreamEOFYieldsAdapterEOF(t *testing.T) {
	tr := NewTrackerWithClock(&fakeClock{now: time.Unix(0, 0)})
	ev, synthesized := tr.OnUpstreamEOF()
	if !synthesized {
		t.Fatal("OnUpstreamEOF: want synthesized terminal")
	}
	fin, ok := ev.(canon.TurnFinished)
	if !ok {
		t.Fatalf("OnUpstreamEOF: got %T, want canon.TurnFinished", ev)
	}
	reason, hasReason := fin.Status.Reason()
	if !hasReason || reason != canon.IncompleteAdapterEOF {
		t.Fatalf("EOF status: got reason=%v ok=%v, want IncompleteAdapterEOF", reason, hasReason)
	}
	if _, again := tr.OnUpstreamEOF(); again {
		t.Fatal("second OnUpstreamEOF: want synthesized=false")
	}
}

func TestStallAfterThreshold(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	tr := NewTrackerWithClock(clock)
	if err := tr.Apply(canon.ItemStarted{Item: messageItem("m1")}); err != nil {
		t.Fatalf("ItemStarted: %v", err)
	}
	if _, synthesized := tr.OnStall(); synthesized {
		t.Fatal("OnStall before threshold: want synthesized=false")
	}
	clock.now = clock.now.Add(StallThreshold - time.Second)
	if _, synthesized := tr.OnStall(); synthesized {
		t.Fatal("OnStall just before threshold: want synthesized=false")
	}
	clock.now = clock.now.Add(time.Second)
	ev, synthesized := tr.OnStall()
	if !synthesized {
		t.Fatal("OnStall at threshold: want synthesized terminal")
	}
	fin, ok := ev.(canon.TurnFinished)
	if !ok {
		t.Fatalf("OnStall: got %T, want canon.TurnFinished", ev)
	}
	reason, hasReason := fin.Status.Reason()
	if !hasReason || reason != canon.IncompleteUpstreamStall {
		t.Fatalf("stall status: got reason=%v ok=%v, want IncompleteUpstreamStall", reason, hasReason)
	}
}

func TestSynthesisIdempotent(t *testing.T) {
	tr := NewTrackerWithClock(&fakeClock{now: time.Unix(0, 0)})
	if _, synthesized := tr.OnClientDisconnect(); !synthesized {
		t.Fatal("first OnClientDisconnect: want synthesized")
	}
	for name, fn := range map[string]func() (canon.Event, bool){
		"OnClientDisconnect": tr.OnClientDisconnect,
		"OnUpstreamEOF":      tr.OnUpstreamEOF,
		"OnStall":            tr.OnStall,
	} {
		if ev, synthesized := fn(); synthesized || ev != nil {
			t.Fatalf("%s after terminal: got (%v, %v), want (nil, false)", name, ev, synthesized)
		}
	}
	if _, ok := tr.Terminal(); !ok {
		t.Fatal("Terminal after synthesis: want recorded")
	}
}

func TestActiveItemsExposesUnclosedIDs(t *testing.T) {
	tr := NewTrackerWithClock(&fakeClock{now: time.Unix(0, 0)})
	if err := tr.Apply(canon.ItemStarted{Item: messageItem("b")}); err != nil {
		t.Fatalf("ItemStarted b: %v", err)
	}
	if err := tr.Apply(canon.ItemStarted{Item: messageItem("a")}); err != nil {
		t.Fatalf("ItemStarted a: %v", err)
	}
	if err := tr.Apply(canon.ItemFinished{Item: messageItem("b")}); err != nil {
		t.Fatalf("ItemFinished b: %v", err)
	}
	got := tr.ActiveItems()
	if len(got) != 1 || got[0] != "a" {
		t.Fatalf("ActiveItems: got %v, want [a]", got)
	}
}

func TestStallUsesLastActivityNotCreation(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	tr := NewTrackerWithClock(clock)
	clock.now = clock.now.Add(StallThreshold + time.Second)
	if err := tr.Apply(canon.ItemStarted{Item: messageItem("m1")}); err != nil {
		t.Fatalf("ItemStarted: %v", err)
	}
	clock.now = clock.now.Add(StallThreshold - time.Second)
	if _, synthesized := tr.OnStall(); synthesized {
		t.Fatal("OnStall just after fresh activity: want synthesized=false")
	}
	clock.now = clock.now.Add(time.Second)
	if _, synthesized := tr.OnStall(); !synthesized {
		t.Fatal("OnStall 300s after last event: want synthesized terminal")
	}
	err := tr.Apply(canon.TurnFinished{Status: canon.Completed()})
	if !errors.Is(err, ErrTerminalRecorded) {
		t.Fatalf("TurnFinished after stall: got %v, want ErrTerminalRecorded", err)
	}
}
