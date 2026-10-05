package agentinstall

import (
	"path/filepath"
	"strings"

	"github.com/deLiseLINO/prism/internal/integrations"
)

func maintenanceEnv(base integrations.Env, op, entry string) integrations.Env {
	env := copyEnv(base)
	for key := range env {
		if strings.HasPrefix(key, "PRISM_") {
			delete(env, key)
		}
	}
	for _, key := range []string{"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_SSE_PORT", "CLAUDE_AGENT_SDK_VERSION"} {
		delete(env, key)
	}
	if op == "install" {
		env["CI"], env["NONINTERACTIVE"], env["TERM"] = "1", "1", "dumb"
	} else {
		env["NO_COLOR"] = "1"
	}
	if entry != "" {
		env["PATH"] = filepath.Dir(entry) + string(filepath.ListSeparator) + env["PATH"]
	}
	return env
}
