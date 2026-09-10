package webui

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type Handler struct {
	root string
}

func New(dir string) *Handler {
	return &Handler{root: filepath.Clean(dir)}
}

const indexPage = "index.html"

var contentTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".js":   "text/javascript; charset=utf-8",
	".css":  "text/css; charset=utf-8",
	".png":  "image/png",
	".svg":  "image/svg+xml",
	".ico":  "image/x-icon",
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rel := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if rel == "" || rel == "." {
		rel = indexPage
	}
	if strings.Contains(rel, "..") {
		http.Error(w, "prism webui: invalid path", http.StatusBadRequest)
		return
	}
	if !h.serve(w, r, filepath.Join(h.root, filepath.FromSlash(rel))) {
		if !h.serve(w, r, filepath.Join(h.root, indexPage)) {
			http.Error(w, "prism webui: bundle missing", http.StatusNotFound)
		}
	}
}

func (h *Handler) serve(w http.ResponseWriter, r *http.Request, full string) bool {
	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		return false
	}
	if ctype := contentTypes[filepath.Ext(full)]; ctype != "" {
		w.Header().Set("Content-Type", ctype)
	}
	http.ServeFile(w, r, full)
	return true
}
