//go:build darwin

package agentinstall

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/deLiseLINO/prism/internal/integrations"
)

func TestMaintenanceReapedDescendantsDoNotFailCommands(t *testing.T) {
	var wg sync.WaitGroup
	for worker := 0; worker < 6; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for attempt := 0; attempt < 60; attempt++ {
				err := (ExecRunner{}).Run(context.Background(), integrations.Env{"PATH": "/bin:/usr/bin"}, []string{"/bin/sh", "-c", "(:) & exit 0"}, io.Discard, io.Discard)
				if err != nil {
					t.Errorf("successful command with exited descendant failed: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
