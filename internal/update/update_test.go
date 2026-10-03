package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDetectMethodFromInputs(t *testing.T) {
	tests := []struct {
		name                     string
		exe, gobin, gopath, home string
		want                     Method
	}{
		{"default go bin", "/home/u/go/bin/prism", "", "", "/home/u", MethodGo},
		{"gobin wins", "/opt/tools/prism", "/opt/tools", "/home/u/go", "/home/u", MethodGo},
		{"gopath first entry", "/w/one/bin/prism", "", "/w/one:/w/two", "/home/u", MethodGo},
		{"go bin nested is not go", "/home/u/go/bin/sub/prism", "", "", "/home/u", MethodUnknown},
		{"homebrew arm", "/opt/homebrew/Cellar/prism/0.1.0/bin/prism", "", "", "/home/u", MethodBrew},
		{"homebrew intel", "/usr/local/bin/prism", "", "", "/home/u", MethodBrew},
		{"linuxbrew", "/home/linuxbrew/.linuxbrew/bin/prism", "", "", "/home/u", MethodBrew},
		{"prefix lookalike", "/opt/homebrew-other/prism", "", "", "/home/u", MethodUnknown},
		{"desktop staged copy", "/Users/u/Library/Application Support/Prism/cli/prism", "", "", "/Users/u", MethodUnknown},
		{"tmp", "/tmp/prism", "", "", "/home/u", MethodUnknown},
		{"empty", "", "", "", "/home/u", MethodUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectMethodFromInputs(tt.exe, tt.gobin, tt.gopath, tt.home); got != tt.want {
				t.Fatalf("got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestIsNewer(t *testing.T) {
	tests := []struct {
		latest, current string
		want            bool
	}{
		{"0.2.0", "0.1.0", true},
		{"v0.2.0", "0.1.9", true},
		{"0.1.0", "0.1.0-rc.11", true},
		{"0.1.0-rc.11", "0.1.0", false},
		{"0.1.0-rc.12", "0.1.0-rc.11", true},
		{"0.1.0", "0.1.0", false},
		{"0.1.0", "0.2.0", false},
		{"0.1.0", "0.0.0-dev", false},
		{"0.1.0", "garbage", false},
		{"garbage", "0.1.0", false},
		{"", "0.1.0", false},
	}
	for _, tt := range tests {
		if got := IsNewer(tt.latest, tt.current); got != tt.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", tt.latest, tt.current, got, tt.want)
		}
	}
}

func TestShouldPrompt(t *testing.T) {
	state := State{LatestVersion: "0.2.0"}
	tests := []struct {
		name    string
		enabled bool
		state   State
		current string
		method  Method
		want    bool
	}{
		{"newer", true, state, "0.1.0", MethodGo, true},
		{"disabled", false, state, "0.1.0", MethodGo, false},
		{"unknown method", true, state, "0.1.0", MethodUnknown, false},
		{"dismissed", true, State{LatestVersion: "0.2.0", DismissedVersion: "0.2.0"}, "0.1.0", MethodBrew, false},
		{"dismissed older", true, State{LatestVersion: "0.3.0", DismissedVersion: "0.2.0"}, "0.1.0", MethodBrew, true},
		{"up to date", true, state, "0.2.0", MethodGo, false},
		{"empty latest", true, State{}, "0.1.0", MethodGo, false},
		{"dev current", true, state, "0.0.0-dev", MethodGo, false},
		{"invalid current", true, state, "nightly", MethodGo, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ShouldPrompt(tt.enabled, tt.state, tt.current, tt.method)
			if ok != tt.want {
				t.Fatalf("ok = %v, want %v", ok, tt.want)
			}
			if ok && got != "0.2.0" && got != "0.3.0" {
				t.Fatalf("version = %q", got)
			}
		})
	}
}

func TestShouldRefresh(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		checked time.Time
		want    bool
	}{
		{"never", time.Time{}, true},
		{"fresh", now.Add(-RefreshInterval + time.Second), false},
		{"stale", now.Add(-RefreshInterval - time.Second), true},
	}
	for _, tt := range tests {
		if got := ShouldRefresh(State{LastCheckedAt: tt.checked}, now); got != tt.want {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestStateRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested")
	want := State{
		LatestVersion:    "0.2.0",
		LastCheckedAt:    time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC),
		DismissedVersion: "0.1.5",
	}
	if err := SaveState(dir, want); err != nil {
		t.Fatal(err)
	}
	got := LoadState(dir)
	if got.LatestVersion != want.LatestVersion || got.DismissedVersion != want.DismissedVersion || !got.LastCheckedAt.Equal(want.LastCheckedAt) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	info, err := os.Stat(filepath.Join(dir, stateFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}
}

func TestLoadStateToleratesMissingAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	if got := LoadState(dir); got != (State{}) {
		t.Fatalf("missing file: %+v", got)
	}
	if err := os.WriteFile(filepath.Join(dir, stateFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := LoadState(dir); got != (State{}) {
		t.Fatalf("corrupt file: %+v", got)
	}
}

func TestDismissVersionKeepsLatest(t *testing.T) {
	dir := t.TempDir()
	if err := SaveState(dir, State{LatestVersion: "0.2.0"}); err != nil {
		t.Fatal(err)
	}
	if err := DismissVersion(dir, "0.2.0"); err != nil {
		t.Fatal(err)
	}
	got := LoadState(dir)
	if got.LatestVersion != "0.2.0" || got.DismissedVersion != "0.2.0" {
		t.Fatalf("got %+v", got)
	}
}

func newSource(t *testing.T, formula, release http.HandlerFunc) Source {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/formula", formula)
	mux.HandleFunc("/release", release)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return Source{HTTP: srv.Client(), FormulaURL: srv.URL + "/formula", ReleaseURL: srv.URL + "/release"}
}

func TestFetchLatest(t *testing.T) {
	formula := func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("class Prism < Formula\n  version \"0.4.0\"\nend\n"))
	}
	release := func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"tag_name":"v0.3.0-rc.1"}]`))
	}
	src := newSource(t, formula, release)
	ctx := context.Background()

	if got, err := src.FetchLatest(ctx, MethodBrew); err != nil || got != "0.4.0" {
		t.Fatalf("brew: %q %v", got, err)
	}
	if got, err := src.FetchLatest(ctx, MethodGo); err != nil || got != "0.3.0-rc.1" {
		t.Fatalf("go: %q %v", got, err)
	}

	broken := newSource(t, func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", http.StatusNotFound) }, release)
	if got, err := broken.FetchLatest(ctx, MethodBrew); err != nil || got != "0.3.0-rc.1" {
		t.Fatalf("brew fallback: %q %v", got, err)
	}

	failing := newSource(t, formula, func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", http.StatusInternalServerError) })
	if _, err := failing.FetchLatest(ctx, MethodGo); err == nil {
		t.Fatal("expected error")
	}

	empty := newSource(t, formula, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`[]`)) })
	if _, err := empty.FetchLatest(ctx, MethodGo); err == nil {
		t.Fatal("expected error for no releases")
	}
}

func TestRefreshStateKeepsDismissed(t *testing.T) {
	src := newSource(t, http.NotFound, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"tag_name":"v0.5.0"}]`))
	})
	got, err := src.Refresh(context.Background(), State{DismissedVersion: "0.4.0"}, MethodGo)
	if err != nil {
		t.Fatal(err)
	}
	if got.LatestVersion != "0.5.0" || got.DismissedVersion != "0.4.0" || got.LastCheckedAt.IsZero() {
		t.Fatalf("got %+v", got)
	}
}

func TestCommandAndManualInstructions(t *testing.T) {
	if got := CommandString(MethodBrew, "0.2.0"); got != "brew upgrade deLiseLINO/tap/prism" {
		t.Fatalf("brew: %q", got)
	}
	if got := CommandString(MethodGo, "0.2.0"); got != "go install github.com/deLiseLINO/prism/cmd/prism@v0.2.0" {
		t.Fatalf("go: %q", got)
	}
	if got := CommandString(MethodUnknown, "0.2.0"); got != "" {
		t.Fatalf("unknown: %q", got)
	}
	if got := ReleaseNotesURL("0.2.0"); got != "https://github.com/deLiseLINO/prism/releases/tag/v0.2.0" {
		t.Fatalf("notes: %q", got)
	}
	if got := ReleaseNotesURL(""); got != "https://github.com/deLiseLINO/prism/releases/latest" {
		t.Fatalf("notes empty: %q", got)
	}
}
