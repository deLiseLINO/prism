package provider

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"time"
)

const StreamIdleTimeout = 300 * time.Second

var ErrStreamIdle = errors.New("provider: upstream stream idle")

type idleBody struct {
	body  io.ReadCloser
	timer *time.Timer
	idle  atomic.Bool
	d     time.Duration
}

// IdleBody cancels the request and fails reads with ErrStreamIdle when the
// upstream stays silent for d. Progress keeps the stream alive, so total
// duration is unbounded.
func IdleBody(body io.ReadCloser, d time.Duration, cancel context.CancelFunc) io.ReadCloser {
	b := &idleBody{body: body, d: d}
	b.timer = time.AfterFunc(d, func() {
		b.idle.Store(true)
		cancel()
	})
	return b
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	if b.idle.Load() {
		return n, ErrStreamIdle
	}
	if n > 0 {
		b.timer.Reset(b.d)
	}
	return n, err
}

func (b *idleBody) Close() error {
	b.timer.Stop()
	return b.body.Close()
}
