package server_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk/api"
)

// The wire tests record the bodies a client and the server really exchange: the request line and body, the status,
// the headers a client reads (ETag, Location, Retry-After, Content-Type, Cache-Control) and the JSON body, for the
// main routes. The transcripts are golden files in testdata/wire. They are normalized (times, ids, uuids, durations)
// and the JSON members are sorted, so the diff of a golden file is the diff of the wire. A change that is meant is
// made with `just freeze`, like the other frozen surfaces (docs/api.md, "What is frozen").

// wire drives the server over HTTP as an administrator bound to the project the environment seeded, and writes
// what it did into a transcript.
type wire struct {
	t     *testing.T
	e     *dtest.Env
	token string
	out   strings.Builder
	ops   map[string]string // the operation ids seen, by the placeholder they were given
}

func newWire(t *testing.T, e *dtest.Env) *wire {
	return &wire{t: t, e: e, token: e.Token(api.PermAdmin), ops: map[string]string{}}
}

// reply is one answer of the server: the status, the headers a client reads and the decoded JSON.
type reply struct {
	Status int
	Header http.Header
	Raw    []byte
	JSON   map[string]any
}

// metadata returns the `metadata` member of a reply as a map (a missing or non-object member is nil).
func (r reply) metadata() map[string]any {
	m, _ := r.JSON["metadata"].(map[string]any)
	return m
}

func (r reply) etag() string { return r.Header.Get("ETag") }

// opID returns the id of the operation an asynchronous reply carries.
func (r reply) opID() string {
	m := r.metadata()
	id, _ := m["id"].(string)
	return id
}

// headers a client reads; the others (Date, Content-Length, X-Request-ID, ...) are not part of the contract.
var wireHeaders = []string{"Content-Type", "Cache-Control", "ETag", "Location", "Retry-After"}

// do sends a request and records it. A body is JSON text; hdr adds request headers (If-Match, ...). The project
// of the environment is added to the query unless the path names one.
func (w *wire) do(method, path, body string, hdr ...string) reply {
	w.t.Helper()
	u, err := url.Parse(path)
	if err != nil {
		w.t.Fatal(err)
	}
	q := u.Query()
	if q.Get("project") == "" && w.e.Admin != nil && !strings.Contains(path, "project=") && !strings.HasPrefix(u.Path, "/api/v1/projects") &&
		!strings.HasPrefix(u.Path, "/api/v1/networks") && !strings.HasPrefix(u.Path, "/api/v1/storage") && !strings.HasPrefix(u.Path, "/api/v1/images") {
		q.Set("project", dtest.SeedProject)
	}
	u.RawQuery = q.Encode()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, w.e.HTTP.URL+u.RequestURI(), rd)
	if err != nil {
		w.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+w.token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	var sent []string
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
		sent = append(sent, hdr[i]+": "+hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	r := reply{Status: resp.StatusCode, Header: resp.Header, Raw: raw}
	_ = json.Unmarshal(raw, &r.JSON)

	fmt.Fprintf(&w.out, "> %s %s\n", method, w.norm(strings.TrimPrefix(u.String(), w.e.HTTP.URL)))
	for _, h := range sent {
		fmt.Fprintf(&w.out, "> %s\n", h)
	}
	if body != "" {
		fmt.Fprintf(&w.out, "> %s\n", compactJSON(body))
	}
	fmt.Fprintf(&w.out, "< %d %s\n", resp.StatusCode, http.StatusText(resp.StatusCode))
	for _, h := range wireHeaders {
		if v := resp.Header.Get(h); v != "" {
			fmt.Fprintf(&w.out, "< %s: %s\n", h, w.norm(v))
		}
	}
	if len(bytes.TrimSpace(raw)) > 0 {
		fmt.Fprintf(&w.out, "%s\n", w.normJSON(raw, resp.StatusCode == http.StatusAccepted))
	}
	w.out.WriteString("\n")
	return r
}

// note writes a comment line into the transcript: what the following requests show.
func (w *wire) note(format string, a ...any) {
	fmt.Fprintf(&w.out, "# "+format+"\n", a...)
}

// wait waits for an operation with the wait route (a final record answers at once) and records it.
func (w *wire) wait(id string) reply {
	w.t.Helper()
	if id == "" {
		w.t.Fatalf("the request was not accepted as an operation:\n%s", w.out.String())
	}
	var r reply
	for i := 0; i < 20; i++ {
		r = w.do("GET", "/api/v1/operations/"+id+"/wait?timeout=5", "")
		if m := r.metadata(); m != nil {
			if s, _ := m["status"].(string); api.OperationStatus(s).Final() {
				return r
			}
		}
	}
	w.t.Fatalf("operation %s did not finish: %s", id, r.Raw)
	return r
}

var (
	reTime     = regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?(Z|[+-]\d\d:\d\d)`)
	reUUID     = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	reOpPath   = regexp.MustCompile(`/operations/([0-9a-f-]{36})`)
	reHostPort = regexp.MustCompile(`127\.0\.0\.1:\d+`)
	reLink     = regexp.MustCompile(`\bjo[0-9a-f]{12}[0-9a-z]\b`)
	reLabel    = regexp.MustCompile(`jo-[0-9a-f]{12}`)
	reAge      = regexp.MustCompile(`age1[0-9a-z]{50,}`)
)

// norm replaces what differs from run to run in a line: times, uuids, the operation ids and the address of the
// test server. Operation ids get a number in the order they appear.
func (w *wire) norm(s string) string {
	s = reOpPath.ReplaceAllStringFunc(s, func(m string) string {
		return "/operations/" + w.opName(reOpPath.FindStringSubmatch(m)[1])
	})
	s = reTime.ReplaceAllString(s, "<time>")
	s = reUUID.ReplaceAllString(s, "<uuid>")
	s = reHostPort.ReplaceAllString(s, "<server>")
	s = reLink.ReplaceAllString(s, "<link>")
	s = reLabel.ReplaceAllString(s, "jo-<label>")
	s = reAge.ReplaceAllString(s, "<age-recipient>")
	return s
}

func (w *wire) opName(id string) string {
	if n, ok := w.ops[id]; ok {
		return n
	}
	n := fmt.Sprintf("<op%d>", len(w.ops)+1)
	w.ops[id] = n
	return n
}

// volatileOp are the members of an operation that change while it runs: the answer that accepts a request records
// them as a placeholder, and the final record of the operation (wait) shows them.
var volatileOp = map[string]bool{"status": true, "may_cancel": true, "metadata": true, "steps": true, "progress": true, "updated_at": true, "error": true}

// normJSON pretty-prints a JSON body with sorted members and normalized values. mask replaces the volatile members
// of the operation record in the metadata of an asynchronous answer.
func (w *wire) normJSON(raw []byte, mask bool) string {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return w.norm(strings.TrimSpace(string(raw)))
	}
	if m, ok := v.(map[string]any); ok && mask {
		if md, ok := m["metadata"].(map[string]any); ok {
			for k := range md {
				if volatileOp[k] {
					md[k] = "<volatile>"
				}
			}
		}
	}
	v = w.normValue("", v)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		w.t.Fatal(err)
	}
	return strings.TrimRight(buf.String(), "\n")
}

func (w *wire) normValue(key string, v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			switch {
			case k == "duration_ms":
				out[k] = "<ms>"
			case k == "rules_id":
				out[k] = "<hash>"
			case k == "api_extensions":
				out[k] = "<the list of extensions.golden>"
			case k == "steps" && isOperationMap(x):
				// the names of the steps are the operation's business; the shape of a step is the contract
				out[k] = []any{map[string]any{"name": "<step>", "duration_ms": "<ms>"}}
			case k == "id" && isOperationMap(x):
				out[k] = w.opName(fmt.Sprint(e))
			default:
				out[k] = w.normValue(k, e)
			}
		}
		// What the daemon's own loops read into the status of an object (conditions, drift: the background setup of
		// a shell, the reconcile pass, the state of the firewall) is in an answer or not depending on the moment, so
		// a transcript leaves it out. The shape of a condition is frozen with the other envelopes
		// (testdata/freeze/envelopes.golden).
		delete(out, "conditions")
		delete(out, "drift")
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = w.normValue(key, e)
		}
		return out
	case string:
		return w.norm(x)
	default:
		return v
	}
}

// isOperationMap reports whether a JSON object is an operation record (it has the members only operations have).
func isOperationMap(m map[string]any) bool {
	_, a := m["may_cancel"]
	_, b := m["description"]
	return a && b
}

func compactJSON(s string) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(s)); err != nil {
		return s
	}
	return buf.String()
}

// check compares the transcript with its golden file.
func (w *wire) check(name string) {
	w.t.Helper()
	wireCheck(w.t, name, w.out.String())
}
