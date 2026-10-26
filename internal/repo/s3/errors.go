package s3

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Error is an error answered by the service. The message never contains
// credentials.
type Error struct {
	Op        string // "put", "get", "head", "list", ...
	Key       string // the repository key, without the prefix
	Status    int
	Code      string // the S3 error code, for example "NoSuchBucket"
	Message   string
	RequestID string
}

// Error implements error.
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("s3 " + e.Op)
	if e.Key != "" {
		b.WriteString(" " + e.Key)
	}
	fmt.Fprintf(&b, ": HTTP %d", e.Status)
	if e.Code != "" {
		b.WriteString(" " + e.Code)
	}
	if e.Message != "" && e.Message != e.Code {
		b.WriteString(": " + e.Message)
	}
	if e.RequestID != "" {
		b.WriteString(" (request " + e.RequestID + ")")
	}
	return b.String()
}

// Temporary reports whether retrying the request later may succeed.
func (e *Error) Temporary() bool { return retryableStatus(e.Status, e.Code) }

func retryableStatus(status int, code string) bool {
	switch status {
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout,
		http.StatusTooManyRequests, http.StatusRequestTimeout:
		return true
	case http.StatusConflict:
		// a concurrent conditional write on the same key
		return code == "ConditionalRequestConflict" || code == "OperationAborted"
	}
	return code == "SlowDown" || code == "RequestTimeout" || code == "InternalError" || code == "ServiceUnavailable"
}

// IsCode reports whether err is an S3 error with one of the codes.
func IsCode(err error, codes ...string) bool {
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	for _, c := range codes {
		if e.Code == c {
			return true
		}
	}
	return false
}

type xmlErrorBody struct {
	XMLName   xml.Name `xml:"Error"`
	Code      string   `xml:"Code"`
	Message   string   `xml:"Message"`
	RequestID string   `xml:"RequestId"`
}

// parseError reads an error response (the body is consumed and closed).
func parseError(op, key string, resp *http.Response) *Error {
	e := &Error{Op: op, Key: key, Status: resp.StatusCode, RequestID: resp.Header.Get("x-amz-request-id"), Code: resp.Header.Get("x-amz-error-code")}
	if resp.Body != nil {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
		var x xmlErrorBody
		if len(b) > 0 && xml.Unmarshal(b, &x) == nil {
			if x.Code != "" {
				e.Code = x.Code
			}
			e.Message = x.Message
			if x.RequestID != "" {
				e.RequestID = x.RequestID
			}
		}
	}
	return e
}
