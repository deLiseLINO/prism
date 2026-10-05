package agentinstall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deLiseLINO/prism/internal/integrations"
)

func TestMaintenanceDiagnosticsAndInstallEnvironment(t *testing.T) {
	f := maintenanceSandbox(t)
	f.global(f.prefix, "@earendil-works/pi-coding-agent", "pi", "echo 1.2.3")
	f.env["PRISM_TOKEN"] = "private-value"
	f.env["TERM"] = "xterm"
	f.env["PACKAGE_AUTH"] = "user-auth"
	observed := filepath.Join(f.home, "environment")
	f.npm("printf '%s\\n' \"${PRISM_TOKEN-unset}\" \"$CI\" \"$NONINTERACTIVE\" \"$TERM\" \"$PACKAGE_AUTH\" > " + shellQuote(observed) + "\n" +
		"read ignored && exit 9\nprintf 'Bearer '; printf 'fixture-one\\n'\nprintf 'api_key=' >&2; printf 'fixture-two' >&2\n" +
		"i=0; while [ $i -lt 5000 ]; do printf x; i=$((i+1)); done; printf ' sk-fixture-overflow\\n'\nprintf 'Basic fixture-final'\nexit 1")
	m := f.manager()
	if _, err := m.Install(integrations.Pi, true); err != nil {
		t.Fatal(err)
	}
	job := requireJob(t, m, integrations.Pi, StateFailed)
	data, err := os.ReadFile(observed)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "unset\n1\n1\ndumb\nuser-auth\n" {
		t.Fatalf("install environment %q", data)
	}
	for _, secret := range []string{"fixture-one", "fixture-two", "fixture-overflow", "fixture-final"} {
		if strings.Contains(job.Output+job.Error, secret) {
			t.Fatalf("public diagnostic leaked %s: %+v", secret, job)
		}
	}
	if !strings.Contains(job.Output, "[REDACTED]") || len(job.Output) > outputTailCap {
		t.Fatalf("unsafe or unbounded diagnostics: %+v", job)
	}
	if f.env["PRISM_TOKEN"] != "private-value" || f.env["TERM"] != "xterm" {
		t.Fatal("caller environment mutated")
	}
}
