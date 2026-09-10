package webui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeBundle(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func TestServesIndexAndAssets(t *testing.T) {
	dir := writeBundle(t, map[string]string{
		"index.html": "<html>prism</html>",
		"styles.css": "body{}",
		"index.js":   "console.log(1)",
	})
	h := http.StripPrefix("/ui", New(dir))

	for path, wantBody := range map[string]string{
		"/ui":            "<html>prism</html>",
		"/ui/":           "<html>prism</html>",
		"/ui/styles.css": "body{}",
		"/ui/index.js":   "console.log(1)",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200", path, rec.Code)
		}
		if body := rec.Body.String(); body != wantBody {
			t.Fatalf("GET %s body = %q, want %q", path, body, wantBody)
		}
	}
}

func TestContentTypes(t *testing.T) {
	dir := writeBundle(t, map[string]string{
		"index.html": "x",
		"styles.css": "x",
		"index.js":   "x",
	})
	h := http.StripPrefix("/ui", New(dir))

	for path, wantType := range map[string]string{
		"/ui/index.html": "text/html; charset=utf-8",
		"/ui/styles.css": "text/css; charset=utf-8",
		"/ui/index.js":   "text/javascript; charset=utf-8",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if got := rec.Header().Get("Content-Type"); got != wantType {
			t.Fatalf("GET %s content-type = %q, want %q", path, got, wantType)
		}
	}
}

func TestUnknownPathFallsBackToIndex(t *testing.T) {
	dir := writeBundle(t, map[string]string{"index.html": "<html>prism</html>"})
	h := http.StripPrefix("/ui", New(dir))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/whatever", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "<html>prism</html>" {
		t.Fatalf("fallback status=%d body=%q, want 200 index.html", rec.Code, rec.Body.String())
	}
}

func TestTraversalRejected(t *testing.T) {
	dir := writeBundle(t, map[string]string{"index.html": "x", "secret.txt": "topsecret"})
	h := http.StripPrefix("/ui", New(dir))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/../secret.txt", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("traversal status = %d, want 400", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "topsecret") {
		t.Fatalf("traversal leaked file content")
	}
}

func TestPostRejected(t *testing.T) {
	dir := writeBundle(t, map[string]string{"index.html": "x"})
	h := http.StripPrefix("/ui", New(dir))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/ui/index.html", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", rec.Code)
	}
}

func TestMissingBundleIs404(t *testing.T) {
	h := http.StripPrefix("/ui", New(t.TempDir()))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("empty bundle status = %d, want 404", rec.Code)
	}
}
