package fake

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
)

func readAll(r *http.Request) []byte {
	b, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	return b
}

func readCloser(b []byte) io.ReadCloser { return io.NopCloser(bytes.NewReader(b)) }

func itoa(n int) string { return strconv.Itoa(n) }

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
