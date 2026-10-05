//go:build !darwin && !linux

package agentinstall

import (
	"context"
	"fmt"
)

func recognizeCodexStandalone(entry, real string) (installTarget, bool) {
	return installTarget{}, false
}
func (m *Manager) updateStandalone(ctx context.Context, def Definition, target installTarget, check releaseCheck, job *Job) error {
	return fmt.Errorf("standalone archive maintenance is unsupported on this platform")
}
