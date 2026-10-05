package provider

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/deLiseLINO/prism/internal/canon"
)

type runnerFunc func(ctx context.Context, req RunRequest, sink Sink) error

func (f runnerFunc) Run(ctx context.Context, req RunRequest, sink Sink) error {
	return f(ctx, req, sink)
}

type sinkFunc func(canon.Event) error

func (f sinkFunc) Emit(ev canon.Event) error { return f(ev) }

var (
	nopSink   = sinkFunc(func(canon.Event) error { return nil })
	textDelta = canon.TextDelta{ItemID: "m", Text: "x"}
	done      = canon.TurnFinished{Status: canon.Completed()}
)

func streamReq(p WaitPolicy) RunRequest {
	return RunRequest{Request: canon.Request{Stream: true}, Target: Target{Wire: WireResponses, Wait: p}}
}

func blockUntilDone(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func wantStall(t *testing.T, err error) {
	t.Helper()
	var re RunError
	if !errors.Is(err, ErrUpstreamStall) || !errors.As(err, &re) {
		t.Fatalf("err = %v, want RunError wrapping ErrUpstreamStall", err)
	}
	if re.Kind != UnsafeReplay || re.Class != ClassTransport || !re.Accepted || re.ReplaySafe || re.Kind.FailoverAllowed() {
		t.Fatalf("RunError = %+v, want accepted non-replayable transport", re)
	}
}

func TestWaitFirstBudgetNotUndercutByShorterIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := Waiting(runnerFunc(func(ctx context.Context, _ RunRequest, s Sink) error {
			time.Sleep(50 * time.Second)
			if err := s.Emit(textDelta); err != nil {
				return err
			}
			time.Sleep(time.Second)
			return s.Emit(done)
		}))
		err := r.Run(t.Context(), streamReq(WaitPolicy{FirstProgress: time.Minute, Idle: 2 * time.Second}), nopSink)
		if err != nil {
			t.Fatalf("err = %v, want success", err)
		}
	})
}

func TestWaitExpiryPhases(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy WaitPolicy
		before time.Duration
		want   time.Duration
	}{
		{"first", WaitPolicy{FirstProgress: 3 * time.Second, Idle: 10 * time.Second}, -1, 3 * time.Second},
		{"idle", WaitPolicy{FirstProgress: 10 * time.Second, Idle: 2 * time.Second}, time.Second, 3 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				r := Waiting(runnerFunc(func(ctx context.Context, _ RunRequest, s Sink) error {
					if tc.before >= 0 {
						time.Sleep(tc.before)
						if err := s.Emit(textDelta); err != nil {
							return err
						}
					}
					return blockUntilDone(ctx)
				}))
				err := r.Run(t.Context(), streamReq(tc.policy), nopSink)
				wantStall(t, err)
				if got := time.Since(start); got != tc.want {
					t.Fatalf("stalled after %s, want %s", got, tc.want)
				}
			})
		})
	}
}

func TestWaitBudgetsDisableIndependently(t *testing.T) {
	long := time.Hour
	for _, tc := range []struct {
		name   string
		policy WaitPolicy
		preGap time.Duration
		gap    time.Duration
	}{
		{"first off", WaitPolicy{FirstProgress: WaitOff, Idle: 2 * time.Second}, long, time.Second},
		{"idle off", WaitPolicy{FirstProgress: 2 * time.Second, Idle: WaitOff}, time.Second, long},
		{"both off", WaitPolicy{FirstProgress: WaitOff, Idle: WaitOff}, long, long},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := Waiting(runnerFunc(func(ctx context.Context, _ RunRequest, s Sink) error {
					time.Sleep(tc.preGap)
					if err := s.Emit(textDelta); err != nil {
						return err
					}
					time.Sleep(tc.gap)
					return s.Emit(done)
				}))
				if err := r.Run(t.Context(), streamReq(tc.policy), nopSink); err != nil {
					t.Fatalf("err = %v, want success", err)
				}
			})
		})
	}
}

func TestWaitZeroPolicyUsesDefault(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		r := Waiting(runnerFunc(func(ctx context.Context, _ RunRequest, _ Sink) error { return blockUntilDone(ctx) }))
		err := r.Run(t.Context(), streamReq(WaitPolicy{}), nopSink)
		wantStall(t, err)
		if got := time.Since(start); got != DefaultWait {
			t.Fatalf("stalled after %s, want %s", got, DefaultWait)
		}
	})
}

func TestWaitIgnoresEmptyFrames(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		r := Waiting(runnerFunc(func(ctx context.Context, _ RunRequest, s Sink) error {
			for range 2 {
				time.Sleep(time.Second)
				for _, ev := range []canon.Event{
					canon.TextDelta{ItemID: "m"}, canon.ReasoningDelta{ItemID: "m"},
					canon.ToolArgumentsDelta{ItemID: "m"}, canon.CustomToolInputDelta{ItemID: "m"},
				} {
					if err := s.Emit(ev); err != nil {
						return err
					}
				}
			}
			return blockUntilDone(ctx)
		}))
		err := r.Run(t.Context(), streamReq(WaitPolicy{FirstProgress: 3 * time.Second, Idle: WaitOff}), nopSink)
		wantStall(t, err)
		if got := time.Since(start); got != 3*time.Second {
			t.Fatalf("stalled after %s, want 3s despite empty frames", got)
		}
	})
}

func TestWaitRawProgressSwitchesToIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		r := Waiting(runnerFunc(func(ctx context.Context, req RunRequest, _ Sink) error {
			time.Sleep(time.Second)
			Mark(req.Progress)
			return blockUntilDone(ctx)
		}))
		err := r.Run(t.Context(), streamReq(WaitPolicy{FirstProgress: 2 * time.Second, Idle: 5 * time.Second}), nopSink)
		wantStall(t, err)
		if got := time.Since(start); got != 6*time.Second {
			t.Fatalf("stalled after %s, want 6s", got)
		}
	})
}

func TestWaitBlockedDownstreamPausesBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		slow := false
		sink := sinkFunc(func(canon.Event) error {
			if slow {
				time.Sleep(time.Hour)
			}
			return nil
		})
		r := Waiting(runnerFunc(func(ctx context.Context, _ RunRequest, s Sink) error {
			if err := s.Emit(textDelta); err != nil {
				return err
			}
			time.Sleep(3 * time.Second)
			slow = true
			if err := s.Emit(canon.TextDelta{ItemID: "m"}); err != nil {
				return err
			}
			slow = false
			time.Sleep(time.Second)
			return blockUntilDone(ctx)
		}))
		err := r.Run(t.Context(), streamReq(WaitPolicy{FirstProgress: WaitOff, Idle: 5 * time.Second}), sink)
		wantStall(t, err)
		if got, want := time.Since(start), 3*time.Second+time.Hour+2*time.Second; got != want {
			t.Fatalf("stalled after %s, want %s: downstream time must not count, remaining budget must survive", got, want)
		}
	})
}

func TestWaitTerminalStopsWatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lateErr := errors.New("reader closed")
		sink := sinkFunc(func(ev canon.Event) error {
			if _, ok := ev.(canon.TurnFinished); ok {
				time.Sleep(time.Hour)
			}
			return nil
		})
		r := Waiting(runnerFunc(func(ctx context.Context, _ RunRequest, s Sink) error {
			if err := s.Emit(textDelta); err != nil {
				return err
			}
			if err := s.Emit(done); err != nil {
				return err
			}
			time.Sleep(time.Hour)
			return lateErr
		}))
		err := r.Run(t.Context(), streamReq(WaitPolicy{FirstProgress: time.Second, Idle: time.Second}), sink)
		if !errors.Is(err, lateErr) || errors.Is(err, ErrUpstreamStall) {
			t.Fatalf("err = %v, want late reader error untouched after accepted terminal", err)
		}
	})
}

func TestWaitNilResultIsNotStall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := Waiting(runnerFunc(func(ctx context.Context, _ RunRequest, _ Sink) error {
			<-ctx.Done()
			return nil
		}))
		if err := r.Run(t.Context(), streamReq(WaitPolicy{FirstProgress: time.Second}), nopSink); err != nil {
			t.Fatalf("err = %v, want nil passthrough", err)
		}
	})
}

func TestWaitParentCancelStaysCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		time.AfterFunc(time.Second, cancel)
		r := Waiting(runnerFunc(func(ctx context.Context, _ RunRequest, _ Sink) error { return blockUntilDone(ctx) }))
		err := r.Run(ctx, streamReq(WaitPolicy{FirstProgress: 2 * time.Second}), nopSink)
		if !errors.Is(err, context.Canceled) || errors.Is(err, ErrUpstreamStall) {
			t.Fatalf("err = %v, want parent cancel", err)
		}
	})
}

func TestWaitDownstreamErrorPropagates(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		boom := errors.New("client gone")
		r := Waiting(runnerFunc(func(ctx context.Context, _ RunRequest, s Sink) error { return s.Emit(textDelta) }))
		err := r.Run(t.Context(), streamReq(WaitPolicy{FirstProgress: time.Second}), sinkFunc(func(canon.Event) error { return boom }))
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want downstream error", err)
		}
	})
}

func TestWaitStaleTimerCannotExpireNewBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cancelled := false
		w := newWatch(func(error) { cancelled = true }, 10*time.Second, 10*time.Second)
		stale := w.gen
		w.Mark()
		w.expire(stale)
		if cancelled || w.expired {
			t.Fatal("stale generation expired the current budget")
		}
		w.stop()
	})
}

func TestWaitAppliesOnlyToStreamedAttempts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		wire   Wire
		stream bool
		want   bool
	}{
		{"chat stream", WireChat, true, true},
		{"responses non-stream", WireResponses, false, false},
		{"chat non-stream", WireChat, false, false},
		{"codex non-stream", WireCodex, false, true},
		{"antigravity non-stream", WireAntigravity, false, true},
		{"messages non-stream", WireMessages, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got Progress
			r := Waiting(runnerFunc(func(_ context.Context, req RunRequest, _ Sink) error {
				got = req.Progress
				return nil
			}))
			req := RunRequest{Request: canon.Request{Stream: tc.stream}, Target: Target{Wire: tc.wire}}
			if err := r.Run(t.Context(), req, nopSink); err != nil {
				t.Fatal(err)
			}
			if (got != nil) != tc.want {
				t.Fatalf("bounded = %t, want %t", got != nil, tc.want)
			}
		})
	}
}
