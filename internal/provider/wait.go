package provider

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/deLiseLINO/prism/internal/canon"
)

const DefaultWait = 300 * time.Second

// WaitOff disables one waiting budget. A zero WaitPolicy field means DefaultWait.
const WaitOff = time.Duration(-1)

// WaitPolicy bounds a streaming attempt. FirstProgress covers silence before
// the first useful upstream frame, including time spent waiting for headers.
// Idle covers silence after that frame and stays disarmed until the frame
// arrives, so a shorter idle budget cannot cut a longer first budget. With
// the first budget off and idle on, no timer runs until the first useful
// frame. Both off means no timer.
type WaitPolicy struct {
	FirstProgress time.Duration
	Idle          time.Duration
}

func (p WaitPolicy) budgets() (first, idle time.Duration) {
	return resolveBudget(p.FirstProgress), resolveBudget(p.Idle)
}

func resolveBudget(d time.Duration) time.Duration {
	switch {
	case d == 0:
		return DefaultWait
	case d < 0:
		return 0
	default:
		return d
	}
}

// Progress reports raw upstream activity that the decoder does not emit as a
// canon event. Canon events are marked by the waiting sink, so adapters must
// not mark those again.
type Progress interface{ Mark() }

func Mark(p Progress) {
	if p != nil {
		p.Mark()
	}
}

// Waiting bounds one attempt. Chat and responses calls are bounded only when
// the client asked for a stream. Codex, antigravity, and messages always read
// an event stream, so they are bounded even for a single JSON body. Expiry
// cancels only this attempt and reports an UnsafeReplay error wrapping
// ErrUpstreamStall.
func Waiting(inner Runner) Runner {
	if inner == nil {
		return nil
	}
	return waitingRunner{inner: inner}
}

type waitingRunner struct{ inner Runner }

func (w waitingRunner) Run(ctx context.Context, req RunRequest, sink Sink) error {
	first, idle := req.Target.Wait.budgets()
	if !streamWait(req) || (first <= 0 && idle <= 0) {
		return w.inner.Run(ctx, req, sink)
	}
	attempt, cancel := context.WithCancelCause(ctx)
	defer cancel(context.Canceled)
	wt := newWatch(cancel, first, idle)
	defer wt.stop()
	req.Progress = wt
	ps := &progressSink{next: sink, watch: wt}
	err := w.inner.Run(attempt, req, ps)
	wt.stop()
	if err == nil || ps.failed || !errors.Is(context.Cause(attempt), ErrUpstreamStall) {
		return err
	}
	phase, limit := wt.expiry()
	return RunError{
		Kind:     UnsafeReplay,
		Class:    ClassTransport,
		Accepted: true,
		Cause:    fmt.Errorf("%w: no %s within %s", ErrUpstreamStall, phase, limit),
	}
}

func streamWait(req RunRequest) bool {
	switch req.Target.Wire {
	case WireCodex, WireAntigravity, WireMessages:
		return true
	default:
		return req.Request.Stream
	}
}

type watch struct {
	mu       sync.Mutex
	cancel   context.CancelCauseFunc
	first    time.Duration
	idle     time.Duration
	timer    *time.Timer
	gen      uint64
	seen     bool
	active   bool
	deadline time.Time
	left     time.Duration
	paused   int
	done     bool
	expired  bool
	phase    string
	limit    time.Duration
}

func newWatch(cancel context.CancelCauseFunc, first, idle time.Duration) *watch {
	w := &watch{cancel: cancel, first: first, idle: idle}
	if first > 0 {
		w.mu.Lock()
		w.active, w.left = true, first
		w.arm(first)
		w.mu.Unlock()
	}
	return w
}

func (w *watch) arm(d time.Duration) {
	w.disarm()
	g := w.gen
	w.deadline = time.Now().Add(d)
	w.timer = time.AfterFunc(d, func() { w.expire(g) })
}

func (w *watch) disarm() {
	w.gen++
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
}

func (w *watch) expire(g uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if g != w.gen || w.done || w.expired {
		return
	}
	w.expired, w.timer = true, nil
	w.phase, w.limit = "first progress", w.first
	if w.seen {
		w.phase, w.limit = "progress", w.idle
	}
	w.cancel(ErrUpstreamStall)
}

// Mark records useful upstream progress and moves from the first budget to idle.
func (w *watch) Mark() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done || w.expired {
		return
	}
	w.seen = true
	if w.idle <= 0 {
		w.active = false
		w.disarm()
		return
	}
	w.active, w.left = true, w.idle
	if w.paused == 0 {
		w.arm(w.idle)
	}
}

func (w *watch) tryPause() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.expired {
		return false
	}
	w.paused++
	if w.paused == 1 && w.active && !w.done {
		w.left = max(time.Until(w.deadline), 0)
		w.disarm()
	}
	return true
}

func (w *watch) resume() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.paused > 0 {
		w.paused--
	}
	if w.paused == 0 && w.active && !w.done && !w.expired {
		w.arm(w.left)
	}
}

func (w *watch) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.expired {
		w.done, w.active = true, false
	}
	w.disarm()
}

func (w *watch) expiry() (string, time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.phase, w.limit
}

type progressSink struct {
	next   Sink
	watch  *watch
	failed bool
}

func (s *progressSink) Emit(ev canon.Event) error {
	if !s.watch.tryPause() {
		return ErrUpstreamStall
	}
	defer s.watch.resume()
	if err := s.next.Emit(ev); err != nil {
		s.failed = true
		return err
	}
	switch {
	case terminalEvent(ev):
		s.watch.stop()
	case usefulProgress(ev):
		s.watch.Mark()
	}
	return nil
}

func usefulProgress(ev canon.Event) bool {
	switch e := ev.(type) {
	case canon.ItemStarted, canon.ItemFinished, canon.ItemStateAvailable:
		return true
	case canon.TextDelta:
		return e.Text != ""
	case canon.ReasoningDelta:
		return e.Text != ""
	case canon.ToolArgumentsDelta:
		return len(e.Bytes) > 0
	case canon.CustomToolInputDelta:
		return e.Text != ""
	default:
		return false
	}
}

func terminalEvent(ev canon.Event) bool {
	switch ev.(type) {
	case canon.TurnFinished, canon.TurnFailed:
		return true
	default:
		return false
	}
}
