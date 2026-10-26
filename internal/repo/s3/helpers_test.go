package s3

import (
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
)

func pemOf(ts *httptest.Server) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw})
}

func fakeResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}
