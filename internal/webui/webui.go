// Package webui embeds the compiled management UI (Vue 3 + Vite, built from /web into dist/) and
// serves it. Only dist/.gitkeep is tracked, so the package compiles before the UI is built.
package webui

import (
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

func init() {
	// Do not depend on the host's mime database for the types the build emits.
	for ext, typ := range map[string]string{
		".js": "text/javascript; charset=utf-8", ".mjs": "text/javascript; charset=utf-8",
		".css": "text/css; charset=utf-8", ".svg": "image/svg+xml", ".woff2": "font/woff2",
		".woff": "font/woff", ".wasm": "application/wasm", ".json": "application/json",
		".webmanifest": "application/manifest+json",
	} {
		_ = mime.AddExtensionType(ext, typ)
	}
}

func handlerFS(sub fs.FS) http.Handler {
	files := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if p == "1.0" || strings.HasPrefix(p, "1.0/") {
			http.NotFound(w, r)
			return
		}
		st, err := fs.Stat(sub, p)
		if err != nil || st.IsDir() || p == "index.html" {
			if isAsset(p) && p != "index.html" { // a missing asset is a 404, never the app under a .js name
				http.NotFound(w, r)
				return
			}
			if _, err := fs.Stat(sub, "index.html"); err != nil {
				http.Error(w, "web UI not built", http.StatusNotFound)
				return
			}
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			w.Header().Set("Cache-Control", "no-cache")
			files.ServeHTTP(w, r2)
			return
		}
		if strings.HasSuffix(p, ".wasm") { // WebAssembly.compileStreaming needs exactly this type
			w.Header().Set("Content-Type", "application/wasm")
		}
		if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}

// staticExt are the file types the build emits. A missing file of one of these types, or anything
// under assets/, is a 404. Client routes may end in dotted names (/jails/web.example) and get the app.
var staticExt = map[string]bool{
	".js": true, ".mjs": true, ".css": true, ".map": true, ".png": true, ".svg": true, ".ico": true,
	".woff": true, ".woff2": true, ".wasm": true, ".json": true, ".txt": true, ".webmanifest": true,
	".jpg": true, ".webp": true,
}

func isAsset(p string) bool {
	return p == "assets" || strings.HasPrefix(p, "assets/") || staticExt[strings.ToLower(path.Ext(p))]
}
