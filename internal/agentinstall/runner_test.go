package agentinstall

import (
	"context"
	"testing"
	"time"
)

// TestFetchScriptRejectsNonHTTPS: the script runner must refuse anything but
// absolute HTTPS before any network happens (matching Agent Orchestrator's
// installer contract).
func TestFetchScriptRejectsNonHTTPS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, raw := range []string{"http://example.com/install.sh", "ftp://example.com/install.sh", "example.com/install.sh", ""} {
		if _, err := FetchScript(ctx, raw); err == nil {
			t.Errorf("FetchScript(%q) succeeded, want HTTPS-only refusal", raw)
		}
	}
}
