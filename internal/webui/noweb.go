//go:build noweb

package webui

import "net/http"

// Enabled reports whether this binary carries the web console (false when built with -tags noweb).
const Enabled = false

// Handler answers 404 to everything: this binary was built with -tags noweb and carries no console.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "this build has no web console", http.StatusNotFound)
	})
}
