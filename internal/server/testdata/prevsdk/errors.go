package sdk

import (
	"bytes"
	"errors"
	"net/http"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// ErrUnsupported matches (errors.Is) any *api.Error with code unsupported:
// a missing API extension, an unimplemented feature, or HTTP 405/501.
var ErrUnsupported error = &api.Error{Code: api.CodeUnsupported, Message: "unsupported"}

// ErrCancelled is returned by Operation.Wait when the operation was cancelled.
var ErrCancelled = errors.New("jailops: operation cancelled")

// ErrClosed is returned after Client.Close.
var ErrClosed = errors.New("jailops: client closed")

// IsUnsupported reports whether err is an unsupported error.
func IsUnsupported(err error) bool { return api.IsCode(err, api.CodeUnsupported) }

// codeForStatus maps an HTTP status to a code for responses without a JSON body.
func codeForStatus(status int) api.Code {
	switch status {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return api.CodeInvalid
	case http.StatusUnauthorized:
		return api.CodeUnauthorized
	case http.StatusForbidden:
		return api.CodeForbidden
	case http.StatusNotFound:
		return api.CodeNotFound
	case http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return api.CodeUnsupported
	case http.StatusConflict:
		return api.CodeAlreadyExists
	case http.StatusPreconditionFailed:
		return api.CodeConflict
	case http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout:
		return api.CodeBusy
	}
	if status >= 400 && status < 500 {
		return api.CodeInvalid
	}
	return api.CodeInternal
}

func errorFromStatus(status int, body []byte) *api.Error {
	msg := strings.TrimSpace(string(bytes.TrimSpace(body)))
	if len(msg) > 200 {
		msg = msg[:200] + "..."
	}
	if msg == "" || strings.HasPrefix(msg, "<") {
		msg = http.StatusText(status)
	}
	e := api.Errorf(codeForStatus(status), "%s", msg)
	return e.WithDetail("http_status", http.StatusText(status))
}
