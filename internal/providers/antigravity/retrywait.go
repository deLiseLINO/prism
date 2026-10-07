package antigravity

import (
	"context"
	"time"

	"github.com/deLiseLINO/prism/internal/provider"
)

func (r *Runner) waitRetry(ctx context.Context, delay time.Duration, failure provider.RunError) error {
	if ctx.Err() != nil {
		return transportError(ctx.Err())
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= delay {
		return failure
	}
	if r.sleep != nil {
		r.sleep(delay)
		if ctx.Err() != nil {
			return transportError(ctx.Err())
		}
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return transportError(ctx.Err())
	case <-timer.C:
		return nil
	}
}
