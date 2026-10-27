// Package backupui embeds the web UI of the backup node (cmd/jailops-backupd) and
// serves it as a single page application. The UI is built into dist/ by the
// web build; until it is built only dist/.gitkeep is embedded and the handler
// answers 404 for every page.
package backupui

import (
	"embed"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// Handler serves the embedded UI.
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // dist is embedded: its absence is a build error
	}
	return handlerFS(sub)
}

var types = map[string]string{
	".js":    "text/javascript; charset=utf-8",
	".mjs":   "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".html":  "text/html; charset=utf-8",
	".svg":   "image/svg+xml",
	".woff2": "font/woff2",
	".woff":  "font/woff",
	".wasm":  "application/wasm",
	".json":  "application/json",
	".png":   "image/png",
	".ico":   "image/x-icon",
}

// isAssetExt reports whether a path ending in ext asks for a file rather than for a page
// (a page of the UI may well end in ".com": a jail name).
func isAssetExt(ext string) bool {
	switch ext {
	case ".js", ".mjs", ".css", ".html", ".svg", ".woff2", ".woff", ".wasm", ".json", ".png", ".ico", ".map", ".txt", ".jpg", ".webp", ".gif", ".ttf", ".otf":
		return true
	}
	return false
}

// handlerFS serves files from files. A path that names a file is served as that
// file; a path that does not name an asset (by its extension) that is neither under /assets/ nor under
// /api/v1 gets index.html so that the UI's router can handle it. A missing file
// (an asset extension, or anything under /assets/) is a 404, never the
// app shell, and /api/v1 is never answered with HTML.
func handlerFS(files fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := path.Clean("/" + r.URL.Path)
		if p == "/api" || strings.HasPrefix(p, "/api/") {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(p, "/")
		if name == "" {
			name = "index.html"
		}
		st, err := fs.Stat(files, name)
		if err != nil || st.IsDir() {
			if name == "assets" || strings.HasPrefix(name, "assets/") || isAssetExt(path.Ext(name)) {
				http.NotFound(w, r)
				return
			}
			name = "index.html"
			if st, err = fs.Stat(files, name); err != nil {
				http.NotFound(w, r)
				return
			}
		}
		f, err := files.Open(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		ct := types[path.Ext(name)]
		if ct == "" {
			ct = mime.TypeByExtension(path.Ext(name))
		}
		if ct == "" {
			ct = "application/octet-stream"
		}
		h := w.Header()
		h.Set("Content-Type", ct)
		h.Set("X-Content-Type-Options", "nosniff")
		if strings.HasPrefix(name, "assets/") {
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			h.Set("Cache-Control", "no-cache")
		}
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = io.Copy(w, f)
		}
	})
}
