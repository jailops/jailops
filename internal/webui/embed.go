//go:build !noweb

package webui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed all:dist
var distFS embed.FS

// Enabled reports whether this binary carries the web console (false when built with -tags noweb).
const Enabled = true

// Handler serves the single-page application: real files as they are (hashed assets cached for a
// year, everything else revalidated), and any other path that is not a static file falls back to
// index.html so client-side routes survive a reload. Mount it below the API: it knows nothing
// about /1.0 and answers 404 for it so a mistyped API path never returns HTML.
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err)
	}
	return handlerFS(sub)
}
