package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/account"
)

// An access token the upstream refused, held without a refresh grant, can never
// be renewed. That must surface as a rejected credential (the account is parked
// for re-login), not as a transient transport failure that keeps the dead token
// in rotation.
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
