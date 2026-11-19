package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"jailops.net/jailops/internal/auth"
	"jailops.net/jailops/internal/service"
	"jailops.net/jailops/sdk/api"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// sync writes a sync envelope.
func (c *rc) sync(status int, meta any, etag string) {
	raw, err := json.Marshal(meta)
	if err != nil {
		c.fail(api.Errorf(api.CodeInternal, "encode reply: %v", err))
		return
	}
	if etag != "" {
		c.w.Header().Set("ETag", etag)
	}
	c.status = status
	writeJSON(c.w, status, api.Response{Type: api.ResponseSync, Status: "Success", StatusCode: status, Metadata: raw})
}

// async writes the 202 envelope of an operation.
func (c *rc) async(op api.Operation) {
	raw, _ := json.Marshal(op)
	loc := "/api/v1/operations/" + op.ID
	c.w.Header().Set("Location", loc)
	c.status = http.StatusAccepted
	writeJSON(c.w, http.StatusAccepted, api.Response{Type: api.ResponseAsync, Status: "Operation created", StatusCode: 202, Operation: loc, Metadata: raw})
}

// fail writes the error envelope for err.
func (c *rc) fail(err error) {
	var ae *api.Error
	var locked *auth.LockedError
	switch {
	case errors.As(err, &locked):
		c.w.Header().Set("Retry-After", locked.RetryAfterSeconds())
		ae = api.Errorf(api.CodeRateLimited, "too many failed attempts").
			WithHint("try again in "+locked.RetryAfterSeconds()+" seconds").WithDetail("retry_after", locked.RetryAfterSeconds())
	case errors.As(err, &ae):
		if ae.Code == api.CodeInternal {
			// what the service wrapped (a store, helper or command error) can carry host paths and command output: it goes to
			// the daemon log under the request id, and the client gets the id
			c.s.log.Error("internal error", "rid", c.rid, "route", c.r.Method+" "+c.r.URL.Path, "err", ae.Message)
			ae = api.Errorf(api.CodeInternal, "internal error").WithHint("see the daemon log for request " + c.rid)
		}
	default:
		var mb *http.MaxBytesError
		if errors.As(err, &mb) {
			ae = api.Errorf(api.CodeInvalid, "request body is larger than %d bytes", mb.Limit)
		} else {
			c.s.log.Error("internal error", "rid", c.rid, "route", c.r.Method+" "+c.r.URL.Path, "err", err)
			ae = api.Errorf(api.CodeInternal, "internal error").WithHint("see the daemon log for request " + c.rid)
		}
	}
	st := ae.Code.HTTPStatus()
	if c.statusOverride != 0 {
		st = c.statusOverride
	}
	c.status, c.code = st, ae.Code
	writeJSON(c.w, st, api.Response{Type: api.ResponseError, Status: "Failure", StatusCode: st, Error: ae})
}

// decode reads one JSON value from the body, strictly (strictjson.go).
func (c *rc) decode(v any) error {
	if err := c.jsonBody(); err != nil {
		return err
	}
	return c.decodeBody(v, false)
}

// decodeBody reads the body (bounded by the request's MaxBytesReader) and decodes it into v after the strict check.
// An empty body is io.EOF when optional is set.
func (c *rc) decodeBody(v any, optional bool) error {
	data, err := io.ReadAll(c.r.Body)
	if err != nil {
		var mb *http.MaxBytesError
		if errors.As(err, &mb) {
			return err
		}
		return api.Errorf(api.CodeInvalid, "invalid request body: %v", err)
	}
	if optional && len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := checkStrict(data, v); err != nil {
		return api.Errorf(api.CodeInvalid, "invalid request body: %v", err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return api.Errorf(api.CodeInvalid, "invalid request body: %v", err)
	}
	return nil
}

// decodePatch reads a merge patch (RFC 7386) for an object of the type of typ. Its members are checked against that
// type like the members of a typed body (an unknown or duplicated member is refused: a misspelled member must not
// answer 200 and change nothing), and numbers keep their text, so an integer beyond 2^53 reaches the object as it was
// sent.
func (c *rc) decodePatch(typ any) (map[string]any, error) {
	data, err := io.ReadAll(c.r.Body)
	if err != nil {
		var mb *http.MaxBytesError
		if errors.As(err, &mb) {
			return nil, err
		}
		return nil, api.Errorf(api.CodeInvalid, "invalid request body: %v", err)
	}
	if err := checkStrict(data, typ); err != nil {
		return nil, api.Errorf(api.CodeInvalid, "invalid request body: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var p map[string]any
	if err := dec.Decode(&p); err != nil {
		return nil, api.Errorf(api.CodeInvalid, "invalid request body: %v", err)
	}
	return p, nil
}

// expect parses If-Match into the generation the request expects: AnyGeneration for no header and for "*", the
// generation of the strong entity tag otherwise. The UUID of the tag goes into the request context: a change or delete
// of an object that was created again since (same name, another UUID) is refused like a stale generation. A header
// whose tags cannot match (weak tags, tags this server did not write) fails the precondition at once, as the strong
// comparison of RFC 9110 says; one with several different tags is refused, because the check runs against one.
func (c *rc) expect() (int64, error) {
	m, err := api.ParseIfMatch(c.r.Header.Get("If-Match"))
	if err != nil {
		return 0, err
	}
	if m.Any {
		return service.AnyGeneration, nil
	}
	if len(m.Tags) > 1 {
		return 0, api.Errorf(api.CodeInvalid, "If-Match %q names several entity tags: send the one you read", c.r.Header.Get("If-Match"))
	}
	var uuid string
	var gen int64
	var ok bool
	if len(m.Tags) == 1 {
		uuid, gen, ok = api.ParseETag(m.Tags[0])
	}
	if !ok {
		return 0, api.Errorf(api.CodePreconditionFailed, "no entity tag of If-Match can match: the tags of an object are strong and come from the server").WithHint("read the object again and send the ETag it gives")
	}
	c.r = c.r.WithContext(service.WithExpectedUUID(c.r.Context(), uuid))
	return gen, nil
}

// labels parses repeated ?label=k=v.
func (c *rc) labels() (map[string]string, error) {
	var out map[string]string
	for _, l := range c.r.URL.Query()["label"] {
		k, v, ok := strings.Cut(l, "=")
		if !ok || k == "" {
			return nil, api.Errorf(api.CodeInvalid, "invalid label selector %q (use key=value)", l)
		}
		if out == nil {
			out = map[string]string{}
		}
		out[k] = v
	}
	return out, nil
}

func (c *rc) pathArg(name string) string { return c.r.PathValue(name) }

var _ = fmt.Sprint

// jsonBody refuses a body that is not declared as JSON. A browser form can send text/plain or
// application/x-www-form-urlencoded across origins without a preflight; requiring application/json keeps a
// forged cross-site request out of every handler that reads a body.
func (c *rc) jsonBody() error {
	ct := c.r.Header.Get("Content-Type")
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil || (mt != "application/json" && !strings.HasSuffix(mt, "+json")) {
		return api.Errorf(api.CodeInvalid, "the request body must be application/json (got %q)", ct)
	}
	return nil
}
