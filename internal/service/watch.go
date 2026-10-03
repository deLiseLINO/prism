package service

import (
	"context"
	"time"
)

const WatchInterval = 10 * time.Second

func Watch(ctx context.Context, stateDir, id string, interval time.Duration, onLost func()) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if reg, err := ReadRegistration(stateDir); err != nil || reg.ID != id {
				onLost()
				return
			}
		}
	}
}
