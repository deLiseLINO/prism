//go:build darwin || linux

package agentinstall

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deLiseLINO/prism/internal/integrations"
	"golang.org/x/sys/unix"
)

func TestMaintenanceStandaloneArchiveTransaction(t *testing.T) {
	for _, kind := range []string{"success", "digest", "size", "target", "traversal", "link", "staged-version", "locked", "existing-invalid", "direct-entry", "cancel-before-activation", "reuse", "consecutive", "external-current", "postactivation-failure"} {
		t.Run(kind, func(t *testing.T) {
			f := maintenanceSandbox(t)
			root := filepath.Join(f.home, ".codex", "packages", "standalone")
			old := filepath.Join(root, "releases", "1.0.0-fixture-target")
			manifest := codexManifest{LayoutVersion: 1, Version: "1.0.0", Target: "fixture-target", Variant: "codex", Entrypoint: "bin/codex", ResourcesDir: "codex-resources", PathDir: "codex-path"}
			data, _ := json.Marshal(manifest)
			f.executable(filepath.Join(old, "bin", "codex"), "echo 1.0.0")
			for _, dir := range []string{"codex-resources", "codex-path"} {
				if err := os.Mkdir(filepath.Join(old, dir), 0755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(old, "codex-package.json"), data, 0644); err != nil {
				t.Fatal(err)
			}
			f.link(filepath.Join(root, "current"), old)
			entry := filepath.Join(f.home, ".local", "bin", "codex")
			link := filepath.Join(root, "current", "bin", "codex")
			if kind == "direct-entry" {
				link = filepath.Join(old, "bin", "codex")
			}
			f.link(entry, link)
			manifest.Version = "2.0.0"
			if kind == "target" {
				manifest.Target = "other-target"
			}
			data, _ = json.Marshal(manifest)
			var archive bytes.Buffer
			gz := gzip.NewWriter(&archive)
			tw := tar.NewWriter(gz)
			for _, dir := range []string{"bin", "codex-resources", "codex-path"} {
				tw.WriteHeader(&tar.Header{Name: dir, Typeflag: tar.TypeDir, Mode: 0755})
			}
			version := "2.0.0"
			if kind == "staged-version" {
				version = "1.0.0"
			}
			clientBody := "#!/bin/sh\necho " + version + "\n"
			if kind == "postactivation-failure" {
				clientBody = "#!/bin/sh\nif [ \"$0\" = " + shellQuote(entry) + " ]; then printf held > \"$HOME/probe-held\"; while [ ! -f \"$HOME/release-probe\" ]; do :; done; exit 7; fi\necho 2.0.0\n"
			}
			files := map[string][]byte{"bin/codex": []byte(clientBody), "codex-package.json": data}
			if kind == "traversal" {
				files["../escape"] = []byte("bad")
			}
			for name, content := range files {
				tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0755, Size: int64(len(content))})
				tw.Write(content)
			}
			if kind == "link" {
				tw.WriteHeader(&tar.Header{Name: "codex-path/escape", Typeflag: tar.TypeSymlink, Linkname: "/tmp/escape"})
			}
			tw.Close()
			gz.Close()
			hash := sha256.Sum256(archive.Bytes())
			asset := archiveRelease{Name: "codex-package-fixture-target.tar.gz", URL: "https://github.com/openai/codex/releases/download/rust-v2.0.0/codex-package-fixture-target.tar.gz", Digest: "sha256:" + hex.EncodeToString(hash[:]), Size: int64(archive.Len())}
			if kind == "digest" {
				asset.Digest = "sha256:" + strings.Repeat("0", 64)
			}
			if kind == "size" {
				asset.Size++
			}
			downloadStarted := make(chan struct{})
			downloadRelease := make(chan struct{})
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/latest"):
					json.NewEncoder(w).Encode(map[string]string{"tag_name": "rust-v2.0.0"})
				case strings.Contains(r.URL.Path, "/tags/"):
					json.NewEncoder(w).Encode(map[string]any{"tag_name": "rust-v2.0.0", "assets": []archiveRelease{asset}})
				case strings.HasSuffix(r.URL.Path, ".tar.gz"):
					if kind == "cancel-before-activation" || kind == "external-current" {
						close(downloadStarted)
						select {
						case <-downloadRelease:
						case <-r.Context().Done():
							return
						}
					}
					w.Write(archive.Bytes())
				default:
					t.Errorf("unmapped request %s", r.URL.Path)
					http.Error(w, "unmapped", 404)
				}
			}))
			defer server.Close()
			m := f.manager()
			client := server.Client()
			client.Transport = fixtureTransport{transport: client.Transport, server: server.URL}
			m.http = client
			if kind == "locked" {
				lock, err := os.OpenFile(filepath.Join(root, "install.lock"), os.O_CREATE|os.O_RDWR, 0600)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
				if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
					t.Fatal(err)
				}
				defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
			}
			destination := filepath.Join(root, "releases", "2.0.0-fixture-target")
			var existingInfo os.FileInfo
			if kind == "reuse" {
				f.executable(filepath.Join(destination, "bin", "codex"), "echo 2.0.0")
				for _, dir := range []string{"codex-resources", "codex-path"} {
					if err := os.Mkdir(filepath.Join(destination, dir), 0755); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filepath.Join(destination, "codex-package.json"), data, 0644); err != nil {
					t.Fatal(err)
				}
				existingInfo, _ = os.Stat(filepath.Join(destination, "bin", "codex"))
			}
			if kind == "existing-invalid" {
				os.Mkdir(destination, 0755)
			}
			job, err := m.Update(integrations.Codex)
			if err != nil {
				t.Fatal(err)
			}
			wantActive := old
			if kind == "cancel-before-activation" || kind == "external-current" {
				select {
				case <-downloadStarted:
				case <-time.After(5 * time.Second):
					t.Fatal("download did not start")
				}
				if kind == "cancel-before-activation" {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					if err := m.Stop(ctx); err != nil {
						t.Fatal(err)
					}
				} else {
					wantActive = filepath.Join(root, "releases", "external-fixture-target")
					if err := os.Mkdir(wantActive, 0755); err != nil {
						t.Fatal(err)
					}
					temp := filepath.Join(root, "external-current")
					if err := os.Symlink(wantActive, temp); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(temp, filepath.Join(root, "current")); err != nil {
						t.Fatal(err)
					}
				}
				close(downloadRelease)
			}
			if kind == "postactivation-failure" {
				probe := filepath.Join(f.home, "probe-held")
				until := time.Now().Add(5 * time.Second)
				for {
					if _, err := os.Stat(probe); err == nil {
						break
					}
					if time.Now().After(until) {
						t.Fatal("postactivation probe did not run")
					}
					time.Sleep(10 * time.Millisecond)
				}
				lock, err := os.OpenFile(filepath.Join(root, "install.lock"), os.O_RDWR, 0600)
				if err != nil {
					t.Fatal(err)
				}
				if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
					lock.Close()
					t.Fatal("postactivation probe released filesystem lock")
				}
				lock.Close()
				if _, err := m.Update(integrations.Codex); !errors.Is(err, ErrInstallActive) {
					t.Fatalf("live probe released Manager lease: %v", err)
				}
				if err := os.WriteFile(filepath.Join(f.home, "release-probe"), nil, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "direct-entry" {
				if job.State != StateUnsupported {
					t.Fatalf("direct release authorized: %+v", job)
				}
			} else {
				want := StateFailed
				if kind == "success" || kind == "reuse" || kind == "consecutive" {
					want = StateSucceeded
				}
				if kind == "cancel-before-activation" {
					want = StateInterrupted
				}
				job = requireJob(t, m, integrations.Codex, want)
			}
			active, err := filepath.EvalSymlinks(filepath.Join(root, "current"))
			if err != nil {
				t.Fatal(err)
			}
			if kind == "success" || kind == "reuse" || kind == "consecutive" || kind == "postactivation-failure" {
				wantActive = destination
			}
			if kind == "consecutive" {
				if err := os.Remove(filepath.Join(root, "current")); err != nil {
					t.Fatal(err)
				}
				f.link(filepath.Join(root, "current"), old)
				if _, err := m.Update(integrations.Codex); err != nil {
					t.Fatal(err)
				}
				job = requireJob(t, m, integrations.Codex, StateSucceeded)
				active, err = filepath.EvalSymlinks(filepath.Join(root, "current"))
				if err != nil {
					t.Fatal(err)
				}
			}
			if active != wantActive {
				t.Fatalf("active %s, want %s; job %+v", active, wantActive, job)
			}
			outer, _ := os.Readlink(entry)
			if outer != link {
				t.Fatal("outer launcher changed")
			}
			partials, _ := filepath.Glob(filepath.Join(root, "releases", ".prism-*"))
			if len(partials) != 0 {
				t.Fatalf("attempt leaked %v", partials)
			}
			if kind == "reuse" {
				after, _ := os.Stat(filepath.Join(destination, "bin", "codex"))
				if after == nil || !os.SameFile(existingInfo, after) {
					t.Fatal("valid immutable release was overwritten")
				}
			}
			if kind != "locked" && kind != "direct-entry" {
				lock, err := os.OpenFile(filepath.Join(root, "install.lock"), os.O_RDWR, 0600)
				if err != nil {
					t.Fatal(err)
				}
				if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
					t.Fatalf("terminal job retained lock: %v", err)
				}
				unix.Flock(int(lock.Fd()), unix.LOCK_UN)
				lock.Close()
			}
			links, _ := filepath.Glob(filepath.Join(root, ".current-*"))
			if len(links) != 0 {
				t.Fatalf("activation link leaked %v", links)
			}
			if kind == "postactivation-failure" && !strings.Contains(job.Error, "exit status 7") {
				t.Fatalf("postactivation failure was not reported: %s", fmt.Sprint(job))
			}
			if kind == "success" && (job.Version != "2.0.0" || job.ExpectedVersion != "2.0.0") {
				t.Fatalf("archive proof missing: %+v", job)
			}
		})
	}
}
