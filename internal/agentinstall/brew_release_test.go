package agentinstall

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deLiseLINO/prism/internal/integrations"
)

func TestMaintenanceBrewReleaseAttestsRequestedToken(t *testing.T) {
	for _, kind := range []string{"local-later", "local-mismatch", "api-mismatch", "api-fallback", "cask-later"} {
		t.Run(kind, func(t *testing.T) {
			f := maintenanceSandbox(t)
			prefix := filepath.Join(f.home, "brew")
			cask := kind == "cask-later"
			token, tree, field, id := "opencode", "Cellar", "formulae", integrations.Opencode
			if cask {
				token, tree, field, id = "claude-code@latest", "Caskroom", "casks", integrations.Claude
			}
			binary := string(id)
			body := "if [ -f \"$MUTATIONS\" ]; then echo 1.2.4; else echo 1.2.3; fi"
			target := filepath.Join(prefix, tree, token, "1.2.3", "bin", binary)
			f.executable(target, body)
			f.link(filepath.Join(prefix, "bin", binary), target)
			record := func(name, version string) string {
				if cask {
					return fmt.Sprintf(`{"token":%q,"version":%q}`, name, version)
				}
				return fmt.Sprintf(`{"name":%q,"versions":{"stable":%q}}`, name, version)
			}
			local := fmt.Sprintf(`{%q:[%s,%s]}`, field, record("unrelated", "9.0.0"), record(token, "1.2.4"))
			if kind == "local-mismatch" || kind == "api-fallback" {
				local = fmt.Sprintf(`{%q:[%s]}`, field, record("unrelated", "9.0.0"))
			}
			api := record(token, "1.2.4")
			if kind == "api-mismatch" {
				api = record("unrelated", "9.0.0")
				local = "invalid"
			}
			if kind == "local-later" || kind == "local-mismatch" || cask {
				api = "invalid"
			}
			f.executable(filepath.Join(prefix, "bin", "brew"), "case \"$1\" in --prefix) echo "+shellQuote(prefix)+";; info) printf '%s\\n' "+shellQuote(local)+";; upgrade) printf update > \"$MUTATIONS\";; *) exit 3;; esac")
			f.env["PATH"] = filepath.Join(prefix, "bin") + ":" + f.env["PATH"]
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.Contains(r.URL.Path, "/api/") {
					http.Error(w, "unmapped", 404)
					return
				}
				fmt.Fprint(w, api)
			}))
			defer server.Close()
			m := f.manager()
			client := server.Client()
			client.Transport = fixtureTransport{transport: client.Transport, server: server.URL}
			m.http = client
			if _, err := m.Update(id); err != nil {
				t.Fatal(err)
			}
			want := StateSucceeded
			if kind == "local-mismatch" || kind == "api-mismatch" {
				want = StateFailed
			}
			job := requireJob(t, m, id, want)
			if want == StateSucceeded && (job.ExpectedVersion != "1.2.4" || job.Version != "1.2.4") {
				t.Fatalf("wrong token release proof: %+v", job)
			}
			if want == StateFailed && f.mutations() != "" {
				t.Fatal("mismatched release mutated selected client")
			}
		})
	}
}
