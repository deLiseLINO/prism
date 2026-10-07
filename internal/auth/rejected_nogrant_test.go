package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/account"
)

func TestRefreshRejectedWithoutGrantNeedsReauth(t *testing.T) {
	h := newRefreshHarness(t, "codex")
	cred := freshCodex()
	cred.Refresh = ""
	cred.ExpiresAt = testNow().Add(time.Hour)
	h.seed(cred)
	_, err := h.ref.RefreshRejected(context.Background(), h.lease, cred.Access)
	if !errors.Is(err, account.ErrNeedsReauth) {
		t.Fatalf("err = %v, want ErrNeedsReauth", err)
	}
}
