package stream

import (
	"errors"
	"sort"
	"sync"
	"time"

	"prism/internal/canon"
)

const StallThreshold = 300 * time.Second

type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

var (
	ErrTerminalRecorded  = errors.New("stream: terminal event already recorded")
	ErrItemAlreadyOpen   = errors.New("stream: item already started")
	ErrItemNotStarted    = errors.New("stream: item not started")
	ErrItemAlreadyClosed = errors.New("stream: item already finished")
)

type Tracker struct {
	mu           sync.Mutex
	clock        Clock
	open         map[canon.ItemID]struct{}
	closed       map[canon.ItemID]struct{}
	terminal     canon.Event
	hasTerminal  bool
	lastActivity time.Time
}

func NewTracker() *Tracker {
	return NewTrackerWithClock(realClock{})
}

func NewTrackerWithClock(c Clock) *Tracker {
	return &Tracker{
		clock:        c,
		open:         make(map[canon.ItemID]struct{}),
		closed:       make(map[canon.ItemID]struct{}),
		lastActivity: c.Now(),
	}
}

func (t *Tracker) Apply(ev canon.Event) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.hasTerminal {
		return ErrTerminalRecorded
	}
	switch e := ev.(type) {
	case canon.ItemStarted:
		id, ok := itemID(e.Item)
		if !ok {
			return ErrItemNotStarted
		}
		if _, done := t.closed[id]; done {
			return ErrItemAlreadyClosed
		}
		if _, live := t.open[id]; live {
			return ErrItemAlreadyOpen
		}
		t.open[id] = struct{}{}
	case canon.TextDelta:
		return t.delta(e.ItemID)
	case canon.ReasoningDelta:
		return t.delta(e.ItemID)
	case canon.ToolArgumentsDelta:
		return t.delta(e.ItemID)
	case canon.CustomToolInputDelta:
		return t.delta(e.ItemID)
	case canon.ItemStateAvailable:
		return t.delta(e.ItemID)
	case canon.ItemFinished:
		id, ok := itemID(e.Item)
		if !ok {
			return ErrItemNotStarted
		}
		if _, done := t.closed[id]; done {
			return ErrItemAlreadyClosed
		}
		if _, live := t.open[id]; !live {
			return ErrItemNotStarted
		}
		delete(t.open, id)
		t.closed[id] = struct{}{}
	case canon.TurnFinished, canon.TurnFailed:
		t.terminal = ev
		t.hasTerminal = true
	default:
	}
	t.lastActivity = t.clock.Now()
	return nil
}

func (t *Tracker) delta(id canon.ItemID) error {
	if _, done := t.closed[id]; done {
		return ErrItemAlreadyClosed
	}
	if _, live := t.open[id]; !live {
		return ErrItemNotStarted
	}
	t.lastActivity = t.clock.Now()
	return nil
}

func (t *Tracker) Terminal() (canon.Event, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.terminal, t.hasTerminal
}

func (t *Tracker) ActiveItems() []canon.ItemID {
	t.mu.Lock()
	defer t.mu.Unlock()
	ids := make([]canon.ItemID, 0, len(t.open))
	for id := range t.open {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func (t *Tracker) OnClientDisconnect() (canon.Event, bool) {
	return t.synthesize(canon.TurnFinished{
		Status: canon.Incomplete(canon.IncompleteClientDisconnected),
	})
}

func (t *Tracker) OnUpstreamEOF() (canon.Event, bool) {
	return t.synthesize(canon.TurnFinished{
		Status: canon.Incomplete(canon.IncompleteAdapterEOF),
	})
}

func (t *Tracker) OnStall() (canon.Event, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.hasTerminal {
		return canon.Event(nil), false
	}
	if t.clock.Now().Sub(t.lastActivity) < StallThreshold {
		return canon.Event(nil), false
	}
	return t.synthesizeLocked(canon.TurnFinished{
		Status: canon.Incomplete(canon.IncompleteUpstreamStall),
	})
}

func (t *Tracker) synthesize(ev canon.Event) (canon.Event, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.hasTerminal {
		return canon.Event(nil), false
	}
	return t.synthesizeLocked(ev)
}

func (t *Tracker) synthesizeLocked(ev canon.Event) (canon.Event, bool) {
	t.terminal = ev
	t.hasTerminal = true
	return ev, true
}

func itemID(it canon.Item) (canon.ItemID, bool) {
	switch v := it.(type) {
	case canon.Message:
		return v.ID, true
	case canon.ReasoningItem:
		return v.ID, true
	case canon.FunctionCall:
		return v.ID, true
	case canon.FunctionOutput:
		return v.ID, true
	case canon.CustomToolCall:
		return v.ID, true
	case canon.CustomToolOutput:
		return v.ID, true
	case canon.LocalShellCall:
		return v.ID, true
	case canon.LocalShellOutput:
		return v.ID, true
	case canon.ToolSearchCall:
		return v.ID, true
	case canon.ToolSearchOutput:
		return v.ID, true
	case canon.CompactionMarker:
		return v.ID, true
	default:
		return "", false
	}
}
