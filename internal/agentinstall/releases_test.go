package agentinstall

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deLiseLINO/prism/internal/integrations"
)

func TestMaintenanceUpdateFreezesReleaseAndRejectsNoop(t *testing.T) {
	for _, kind := range []string{"noop", "advances", "current", "invalid-feed", "v2", "prerelease"} {
		t.Run(kind, func(t *testing.T) {
			f := maintenanceSandbox(t)
			version := filepath.Join(f.home, "version")
			if err := os.WriteFile(version, nil, 0644); err != nil {
				t.Fatal(err)
			}
			body := "if [ \"$1\" = upgrade ]; then printf update >> \"$MUTATIONS\";"
			if kind == "advances" {
				body += " printf 1.2.4 > " + shellQuote(version) + ";"
			}
			body += " else if [ -s " + shellQuote(version) + " ]; then /bin/cat " + shellQuote(version) + "; else echo 1.2.3; fi; fi"
			if kind == "v2" {
				body = strings.ReplaceAll(body, "1.2.3", "2.0.0-beta.1")
			}
			if kind == "prerelease" {
				body = strings.ReplaceAll(body, "1.2.3", "1.2.4-beta.1")
			}
			f.executable(filepath.Join(f.home, ".opencode", "bin", "opencode"), body)
			m := f.manager()
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				pkg := "opencode-ai"
				if kind == "v2" {
					pkg = "@opencode/cli"
				}
				if !strings.Contains(r.URL.Path, pkg) {
					t.Errorf("wrong generation feed %s", r.URL.Path)
				}
				value := "1.2.4"
				if kind == "v2" {
					value = "2.0.0"
				}
				if kind == "current" {
					value = "1.2.3"
				}
				if kind == "invalid-feed" {
					value = "invalid"
				}
				json.NewEncoder(w).Encode(map[string]string{"version": value})
			}))
			defer server.Close()
			client := server.Client()
			client.Transport = fixtureTransport{transport: client.Transport, server: server.URL}
			m.http = client
			if _, err := m.Update(integrations.Opencode); err != nil {
				t.Fatal(err)
			}
			want := StateSucceeded
			if kind == "noop" || kind == "invalid-feed" || kind == "v2" || kind == "prerelease" {
				want = StateFailed
			}
			job := requireJob(t, m, integrations.Opencode, want)
			if kind == "noop" && (!strings.Contains(job.Error, "older than expected") || job.ExpectedVersion != "1.2.4" || job.Version != "1.2.3") {
				t.Fatalf("no-op proof missing: %+v", job)
			}
			if (kind == "current" || kind == "invalid-feed") && f.mutations() != "" {
				t.Fatal("mutated current or invalid release")
			}
			if calls != 1 {
				t.Fatalf("release not frozen: %d checks", calls)
			}
		})
	}
}
