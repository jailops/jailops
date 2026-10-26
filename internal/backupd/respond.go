package backupd

import (
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"os"

	"jailops.net/jailops/internal/repo/datastore"
	"jailops.net/jailops/sdk/api"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	var meta json.RawMessage
	if v != nil {
		b, err := json.Marshal(v)
		if err != nil {
			writeError(w, api.Errorf(api.CodeInternal, "encoding the answer: %v", err))
			return
		}
		meta = b
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(api.Response{Type: api.ResponseSync, Status: http.StatusText(status), StatusCode: status, Metadata: meta})
}

func writeError(w http.ResponseWriter, err error) {
	ae, ok := err.(*api.Error)
	if !ok {
		ae = api.Errorf(api.CodeInternal, "%v", err)
	}
	status := ae.Code.HTTPStatus()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(api.Response{Type: api.ResponseError, Status: http.StatusText(status), StatusCode: status, Error: ae})
}

func parseMode(s string) (datastore.Mode, bool) {
	if s == "" {
		return datastore.ModeOnline, false
	}
	return datastore.ParseMode(s)
}

func osHostname() (string, error) { return os.Hostname() }

var randReader io.Reader = rand.Reader
