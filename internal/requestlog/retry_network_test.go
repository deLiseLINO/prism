package requestlog

import (
	"strings"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/execution"
)

func TestNetworkAttemptSnapshotIsolationAndBounds(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	j := New(1, func() time.Time { return now })
	turn := j.Open(execution.Facts{}, "model")
	children := make([]NetworkAttemptInfo, MaxNetworkAttemptsPerAttempt+2)
	for i := range children {
		children[i] = NetworkAttemptInfo{StartedAt: now, FinishedAt: now.Add(time.Second), StatusCode: 429, Outcome: AttemptRateLimited, Error: strings.Repeat("x", maxErrorBytes+1)}
	}
	turn.Attempt(AttemptInfo{CredGen: 7, Version: 11, NetworkAttempts: children, NetworkAttemptsDropped: 3})
	children[0].StatusCode = 200
	children[0].Error = "mutated input"
	first := j.Snapshot()[0].Attempts[0]
	if first.CredGen != 7 || first.Version != 11 || len(first.NetworkAttempts) != MaxNetworkAttemptsPerAttempt || first.NetworkAttemptsDropped != 5 {
		t.Fatalf("stored=%+v", first)
	}
	if first.NetworkAttempts[0].StatusCode != 429 || len(first.NetworkAttempts[0].Error) != maxErrorBytes {
		t.Fatalf("input mutation reached journal=%+v", first.NetworkAttempts[0])
	}
	first.NetworkAttempts[0].StatusCode = 503
	first.NetworkAttempts[0].Error = "mutated snapshot"
	second := j.Snapshot()[0].Attempts[0]
	if second.NetworkAttempts[0].StatusCode != 429 || len(second.NetworkAttempts[0].Error) != maxErrorBytes {
		t.Fatalf("snapshot mutation reached journal=%+v", second.NetworkAttempts[0])
	}
}
