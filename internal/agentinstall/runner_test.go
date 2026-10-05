package agentinstall

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunnerSettlesDescendantsOnEveryReturn(t *testing.T) {
	for _, kind := range []string{"success", "error", "inherited-pipes", "timeout", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			f := maintenanceSandbox(t)
			marker := filepath.Join(f.home, "late-mutation")
			ready := filepath.Join(f.home, "ready")
			redirect := ">/dev/null 2>&1"
			if kind == "inherited-pipes" {
				redirect = ""
			}
			body := "(/bin/sleep 2; printf late > " + shellQuote(marker) + ") " + redirect + " &\nprintf ready > " + shellQuote(ready) + "\n"
			switch kind {
			case "error":
				body += "exit 7"
			case "timeout", "cancel":
				body += "while :; do /bin/sleep 1; done"
			default:
				body += "exit 0"
			}
			entry := filepath.Join(f.tools, "mutator")
			f.executable(entry, body)
			budget := 5 * time.Second
			if kind == "timeout" {
				budget = 700 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			if kind == "cancel" {
				go func() {
					for ctx.Err() == nil {
						if _, err := os.Stat(ready); err == nil {
							cancel()
							return
						}
						time.Sleep(time.Millisecond)
					}
				}()
			}
			var out bytes.Buffer
			err := (ExecRunner{}).Run(ctx, f.env, []string{entry}, &out, &out)
			if (kind == "error" || kind == "timeout" || kind == "cancel") != (err != nil) {
				t.Fatalf("unexpected return for %s: %v", kind, err)
			}
			time.Sleep(2200 * time.Millisecond)
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("descendant mutated after Runner returned: %v", err)
			}
		})
	}
}

func TestFetchScriptRejectsNonHTTPS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, raw := range []string{"http://example.com/install.sh", "ftp://example.com/install.sh", "example.com/install.sh", ""} {
		if _, err := FetchScript(ctx, raw); err == nil {
			t.Errorf("FetchScript(%q) succeeded, want HTTPS-only refusal", raw)
		}
	}
}
