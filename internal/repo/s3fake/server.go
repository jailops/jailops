package s3fake

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const serverName = "jailops-s3fake"

// Defaults.
const (
	DefaultAccess = "testaccess"
	DefaultSecret = "testsecret-12345"
	DefaultRegion = "us-east-1"
)

// Quirk deliberately breaks one behaviour of the fake. Quirks exist so that the
// conformance suite can be shown to FAIL against a wrong server: a suite that
// cannot tell a broken server from a good one proves nothing.
type Quirk string

// The quirks.
const (
	QuirkIgnoreIfNoneMatch  Quirk = "ignore-if-none-match" // If-None-Match: * overwrites
	QuirkNonAtomicPut       Quirk = "non-atomic-put"       // the exists check and the write are not one step
	QuirkAcceptAnySignature Quirk = "accept-any-signature" // nobody is ever refused
	QuirkNoContinuation     Quirk = "no-continuation"      // pages are cut at max-keys without IsTruncated
	QuirkUnsortedListing    Quirk = "unsorted-listing"     // keys come back in reverse order within a page
	QuirkIgnoreRange        Quirk = "ignore-range"         // Range is answered with the whole object
	QuirkRangeOffByOne      Quirk = "range-off-by-one"     // a range is one byte too long
	QuirkTornPutVisible     Quirk = "torn-put-visible"     // a torn upload leaves the partial object behind
	QuirkDeleteMissing404   Quirk = "delete-missing-404"   // deleting a missing key is an error
	QuirkDeleteIgnored      Quirk = "delete-ignored"       // delete answers 204 and removes nothing
	QuirkIgnorePrefix       Quirk = "ignore-prefix"        // listing ignores the prefix
	QuirkIgnoreStartAfter   Quirk = "ignore-start-after"   // listing ignores start-after
	QuirkRepeatPageEdge     Quirk = "repeat-page-edge"     // the last key of a page opens the next one again
	QuirkMissingKeyIs200    Quirk = "missing-key-is-empty" // GET of a missing key returns an empty object
	QuirkHeadMissing200     Quirk = "head-missing-200"     // HEAD of a missing key answers 200
	QuirkLosePutBody        Quirk = "lose-put-body"        // PUT answers 200 and stores a truncated object
	QuirkListHidesRecent    Quirk = "list-hides-recent"    // the newest object is not listed (eventual consistency)
	QuirkIgnoreSkew         Quirk = "ignore-skew"          // clock skew is not checked
	QuirkAcceptWrongPayload Quirk = "accept-wrong-payload" // the payload hash is not checked
)

// Options configure a Server. The zero value is a usable in-memory server on 127.0.0.1.
type Options struct {
	Addr   string // listen address, default 127.0.0.1:0
	Region string // default us-east-1
	Access string // root access key, default DefaultAccess
	Secret string // root secret key, default DefaultSecret
	// Domain enables virtual-host style addressing: a request whose Host is
	// <bucket>.<Domain> addresses that bucket. Path style keeps working.
	Domain  string
	Quirks  []Quirk
	Latency time.Duration // added to every request
}

// Request is one entry of the request log.
type Request struct {
	Seq      int64
	Time     time.Time
	Method   string
	Host     string
	Path     string
	RawQuery string
	Op       string // S3 operation name, e.g. "PutObject", "ListObjectsV2"
	Bucket   string
	Key      string
	Header   http.Header
	Status   int           // HTTP status sent; 0 when the connection was dropped
	Bytes    int64         // payload bytes received
	Fault    string        // description of the injected fault that hit this request, if any
	Duration time.Duration // handling time
}

// Server is a running fake S3 service.
type Server struct {
	opt Options
	st  *store
	ln  net.Listener
	srv *http.Server

	mu       sync.Mutex
	users    map[string]string
	rules    []*Rule
	log      []Request
	counts   map[string]int
	quirks   map[Quirk]bool
	latency  time.Duration
	inflight int
	peak     int

	seq    atomic.Int64
	closed chan struct{}
	once   sync.Once
}

// New starts a server and returns once it listens.
func New(opt Options) (*Server, error) {
	if opt.Addr == "" {
		opt.Addr = "127.0.0.1:0"
	}
	if opt.Region == "" {
		opt.Region = DefaultRegion
	}
	if opt.Access == "" {
		opt.Access = DefaultAccess
	}
	if opt.Secret == "" {
		opt.Secret = DefaultSecret
	}
	ln, err := net.Listen("tcp", opt.Addr)
	if err != nil {
		return nil, err
	}
	s := &Server{opt: opt, st: newStore(), ln: ln, users: map[string]string{opt.Access: opt.Secret}, counts: map[string]int{},
		quirks: map[Quirk]bool{}, latency: opt.Latency, closed: make(chan struct{})}
	for _, q := range opt.Quirks {
		s.quirks[q] = true
	}
	s.srv = &http.Server{Handler: s, ReadHeaderTimeout: 30 * time.Second}
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

// Endpoint is host:port (plain HTTP).
func (s *Server) Endpoint() string { return s.ln.Addr().String() }

// AccessKey and SecretKey are the root credentials.
func (s *Server) AccessKey() string { return s.opt.Access }

// SecretKey is the root secret.
func (s *Server) SecretKey() string { return s.opt.Secret }

// Region is the region the server answers for.
func (s *Server) Region() string { return s.opt.Region }

// Close stops the server and drops its connections.
func (s *Server) Close() {
	s.once.Do(func() {
		close(s.closed)
		_ = s.srv.Close() // not Shutdown: a test server never waits for its clients
	})
}

// AddUser adds a credential; RemoveUser makes it unknown (a revoked key).
func (s *Server) AddUser(access, secret string) {
	s.mu.Lock()
	s.users[access] = secret
	s.mu.Unlock()
}

// RemoveUser makes a credential unknown.
func (s *Server) RemoveUser(access string) {
	s.mu.Lock()
	delete(s.users, access)
	s.mu.Unlock()
}

func (s *Server) secretFor(access string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sec, ok := s.users[access]
	return sec, ok
}

// SetQuirk switches a deliberate defect on or off.
func (s *Server) SetQuirk(q Quirk, on bool) {
	s.mu.Lock()
	s.quirks[q] = on
	s.mu.Unlock()
}

func (s *Server) quirk(q Quirk) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.quirks[q]
}

// SetLatency delays every request by d.
func (s *Server) SetLatency(d time.Duration) {
	s.mu.Lock()
	s.latency = d
	s.mu.Unlock()
}

// ---- request log and counters ---------------------------------------------

// Requests returns a copy of the request log.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.log...)
}

// Count is the number of requests of operation op seen since the last ResetLog ("" = all).
func (s *Server) Count(op string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if op == "" {
		return len(s.log)
	}
	return s.counts[op]
}

// ResetLog clears the log and the counters (not the rules).
func (s *Server) ResetLog() {
	s.mu.Lock()
	s.log, s.counts, s.peak = nil, map[string]int{}, s.inflight
	s.mu.Unlock()
}

// PeakInflight is the highest number of requests handled at the same time since the last ResetLog.
func (s *Server) PeakInflight() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peak
}

// ---- state inspection (tests) ---------------------------------------------

// Object returns the current body of bucket/key.
func (s *Server) Object(bucket, key string) (data []byte, ok bool) {
	b := s.st.bucket(bucket)
	if b == nil {
		return nil, false
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	o := b.objs[key]
	if o == nil {
		return nil, false
	}
	return append([]byte(nil), o.data...), true
}

// PutDirect stores an object without any request (test setup, for example to
// plant a corrupt object); it overwrites.
func (s *Server) PutDirect(bucket, key string, data []byte, mod time.Time) {
	b := s.st.bucket(bucket)
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.put(key, append([]byte(nil), data...), mod)
}

// Keys lists the keys of a bucket, sorted.
func (s *Server) Keys(bucket string) []string {
	b := s.st.bucket(bucket)
	if b == nil {
		return nil
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return append([]string(nil), b.keys...)
}

// EnsureBucket creates the bucket if it does not exist.
func (s *Server) EnsureBucket(name string) error {
	if !validBucketName(name) {
		return fmt.Errorf("s3fake: invalid bucket name %q", name)
	}
	s.st.create(name)
	return nil
}

// Buckets lists the bucket names.
func (s *Server) Buckets() []string { return s.st.names() }

// ---- fault injection -------------------------------------------------------

// Rule injects a fault into the requests it matches. A matching request is
// counted; the first Skip matches pass untouched, the next Times matches (0 =
// all of them) get the Action.
type Rule struct {
	// Match: empty fields match everything. Key is a prefix of the object key.
	// Func, if set, decides too.
	Op, Method, Bucket, Key string
	Func                    func(*Request) bool

	Skip  int
	Times int

	Action Action

	s     *Server
	seen  int
	fired int
}

// Action is what a Rule does to a request.
type Action struct {
	// Delay before anything else happens (interrupted by Close).
	Delay time.Duration
	// Status and Code answer with that S3 error instead of doing the operation,
	// for example 503 SlowDown or 500 InternalError (retryable) or 403 AccessDenied.
	Status int
	Code   string
	// Drop resets the TCP connection before the request is looked at.
	Drop bool
	// DropAfterBytes (> 0) resets the connection once that many body bytes
	// arrived: a torn upload. The object is not stored (unless QuirkTornPutVisible).
	DropAfterBytes int64
	// DropResponse does the whole operation and then resets the connection
	// instead of answering: the client does not know whether its request took effect.
	DropResponse bool
	// Hang never answers (until the client gives up or the server closes).
	Hang bool
}

// AddRule installs a rule and returns it; it stays active until Remove.
func (s *Server) AddRule(r Rule) *Rule {
	r.s = s
	s.mu.Lock()
	defer s.mu.Unlock()
	p := &r
	s.rules = append(s.rules, p)
	return p
}

// Hits is how many requests the rule acted on.
func (r *Rule) Hits() int {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	return r.fired
}

// Seen is how many requests matched the rule (acted on or not).
func (r *Rule) Seen() int {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	return r.seen
}

// Remove uninstalls the rule.
func (r *Rule) Remove() {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	for i, x := range r.s.rules {
		if x == r {
			r.s.rules = append(r.s.rules[:i], r.s.rules[i+1:]...)
			return
		}
	}
}

// ClearRules removes every rule.
func (s *Server) ClearRules() {
	s.mu.Lock()
	s.rules = nil
	s.mu.Unlock()
}

// FailNth makes the nth request of operation op ("" = any) fail with an S3 error.
func (s *Server) FailNth(op string, n int, status int, code string) *Rule {
	return s.AddRule(Rule{Op: op, Skip: n - 1, Times: 1, Action: Action{Status: status, Code: code}})
}

// FailNext fails the next `times` requests of operation op with an S3 error.
func (s *Server) FailNext(op string, times int, status int, code string) *Rule {
	return s.AddRule(Rule{Op: op, Times: times, Action: Action{Status: status, Code: code}})
}

// SlowDownBurst answers the next n requests (any operation) with 503 SlowDown.
func (s *Server) SlowDownBurst(n int) *Rule { return s.FailNext("", n, 503, "SlowDown") }

// DropNth resets the connection of the nth request of operation op.
func (s *Server) DropNth(op string, n int) *Rule {
	return s.AddRule(Rule{Op: op, Skip: n - 1, Times: 1, Action: Action{Drop: true}})
}

// TearNextUpload resets the connection of the next PutObject after `bytes` body bytes.
func (s *Server) TearNextUpload(bytes int64) *Rule {
	return s.AddRule(Rule{Op: "PutObject", Times: 1, Action: Action{DropAfterBytes: bytes}})
}

// LoseNextResponse performs the next request of op fully and then drops the connection.
func (s *Server) LoseNextResponse(op string) *Rule {
	return s.AddRule(Rule{Op: op, Times: 1, Action: Action{DropResponse: true}})
}

// DelayNext delays the next `times` requests of op (any when "") by d.
func (s *Server) DelayNext(op string, times int, d time.Duration) *Rule {
	return s.AddRule(Rule{Op: op, Times: times, Action: Action{Delay: d}})
}

func (s *Server) matchRules(rq *Request) (Action, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rules {
		if r.Op != "" && r.Op != rq.Op || r.Method != "" && r.Method != rq.Method || r.Bucket != "" && r.Bucket != rq.Bucket ||
			r.Key != "" && !strings.HasPrefix(rq.Key, r.Key) || r.Func != nil && !r.Func(rq) {
			continue
		}
		r.seen++
		if r.seen <= r.Skip || r.Times > 0 && r.fired >= r.Times {
			continue
		}
		r.fired++
		return r.Action, true
	}
	return Action{}, false
}

// ---- HTTP ------------------------------------------------------------------

type reqCtx struct {
	id     string
	bucket string
	key    string
	op     string
	rec    *Request
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(c int) {
	if w.status == 0 {
		w.status = c
	}
	w.ResponseWriter.WriteHeader(c)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return w.ResponseWriter.Write(p)
}

// mute swallows the answer of a request whose connection is dropped afterwards.
type mute struct {
	h      http.Header
	status int
}

func (m *mute) Header() http.Header { return m.h }
func (m *mute) Write(p []byte) (int, error) {
	if m.status == 0 {
		m.status = 200
	}
	return len(p), nil
}
func (m *mute) WriteHeader(c int) {
	if m.status == 0 {
		m.status = c
	}
}

var errDropped = errors.New("s3fake: connection dropped by fault injection")

// abortConn resets the TCP connection (RST) without answering.
func abortConn(w http.ResponseWriter) {
	if sw, ok := w.(*statusWriter); ok {
		w = sw.ResponseWriter
	}
	if hj, ok := w.(http.Hijacker); ok {
		if c, _, err := hj.Hijack(); err == nil {
			if tc, ok := c.(*net.TCPConn); ok {
				_ = tc.SetLinger(0)
			}
			_ = c.Close()
			return
		}
	}
	panic(http.ErrAbortHandler)
}

// bucketAndKey resolves the addressing style of a request.
func (s *Server) bucketAndKey(r *http.Request) (bucket, key string) {
	if d := s.opt.Domain; d != "" {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		dd := d
		if h, _, err := net.SplitHostPort(dd); err == nil {
			dd = h
		}
		if b, ok := strings.CutSuffix(host, "."+dd); ok && b != "" {
			return b, strings.TrimPrefix(r.URL.Path, "/")
		}
	}
	p := strings.TrimPrefix(r.URL.Path, "/")
	b, k, _ := strings.Cut(p, "/")
	return b, k
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w0 http.ResponseWriter, r *http.Request) {
	start := time.Now()
	seq := s.seq.Add(1)
	rec := &Request{Seq: seq, Time: start, Method: r.Method, Host: r.Host, Path: r.URL.Path, RawQuery: r.URL.RawQuery, Header: r.Header.Clone()}
	rc := &reqCtx{id: fmt.Sprintf("%016X", seq), rec: rec}
	rc.bucket, rc.key = s.bucketAndKey(r)
	rec.Bucket, rec.Key = rc.bucket, rc.key
	rc.op = classify(r, rc)
	rec.Op = rc.op
	sw := &statusWriter{ResponseWriter: w0}
	var w http.ResponseWriter = sw

	s.mu.Lock()
	s.inflight++
	if s.inflight > s.peak {
		s.peak = s.inflight
	}
	lat := s.latency
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.inflight--
		rec.Status = sw.status
		rec.Duration = time.Since(start)
		s.log = append(s.log, *rec)
		s.counts[rec.Op]++
		s.mu.Unlock()
	}()

	if lat > 0 && !s.sleep(r, lat) {
		return
	}
	act, hit := s.matchRules(rec)
	var dropAfter int64
	if hit {
		rec.Fault = fmt.Sprintf("%+v", act)
		if act.Delay > 0 && !s.sleep(r, act.Delay) {
			return
		}
		switch {
		case act.Drop:
			abortConn(w)
			return
		case act.Hang:
			select {
			case <-r.Context().Done():
			case <-s.closed:
			}
			abortConn(w)
			return
		case act.Status != 0 || act.Code != "":
			e := apiErr(act.Code)
			if act.Status != 0 {
				e.Status = act.Status
			}
			writeError(w, r, rc, e)
			return
		}
		dropAfter = act.DropAfterBytes
		if act.DropResponse {
			m := &mute{h: http.Header{}}
			s.dispatch(m, r, rc, dropAfter)
			rec.Status = m.status
			abortConn(w)
			return
		}
	}
	s.dispatch(w, r, rc, dropAfter)
}

// sleep waits d; false if the client left or the server closed meanwhile.
func (s *Server) sleep(r *http.Request, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-r.Context().Done():
		return false
	case <-s.closed:
		return false
	}
}

// subresources that select a different operation than the plain one; none of
// them is implemented.
var unsupportedSub = []string{"acl", "policy", "policyStatus", "tagging", "cors", "encryption", "replication", "notification", "website",
	"logging", "accelerate", "analytics", "inventory", "metrics", "requestPayment", "ownershipControls", "publicAccessBlock",
	"intelligent-tiering", "retention", "legal-hold", "attributes", "restore", "select", "torrent", "uploads", "uploadId", "partNumber",
	"versioning", "versions", "versionId", "lifecycle", "object-lock", "location", "delete"}

func classify(r *http.Request, rc *reqCtx) string {
	q := r.URL.Query()
	has := func(k string) bool { _, ok := q[k]; return ok }
	for _, k := range unsupportedSub {
		if has(k) && rc.bucket != "" {
			return "Unsupported:" + k
		}
	}
	switch {
	case rc.bucket == "":
		return "Unsupported:service"
	case rc.key == "":
		switch r.Method {
		case http.MethodGet:
			if q.Get("list-type") == "2" {
				return "ListObjectsV2"
			}
			return "Unsupported:ListObjectsV1"
		case http.MethodPut:
			return "CreateBucket"
		case http.MethodDelete:
			return "DeleteBucket"
		case http.MethodHead:
			return "HeadBucket"
		}
		return "Unsupported:bucket-" + r.Method
	}
	switch r.Method {
	case http.MethodGet:
		return "GetObject"
	case http.MethodHead:
		return "HeadObject"
	case http.MethodPut:
		if r.Header.Get("X-Amz-Copy-Source") != "" {
			return "Unsupported:CopyObject"
		}
		return "PutObject"
	case http.MethodDelete:
		return "DeleteObject"
	}
	return "Unsupported:object-" + r.Method
}

func itoa(n int) string { return strconv.Itoa(n) }

// AllQuirks lists every quirk, for tests that require each to be covered.
func AllQuirks() []Quirk {
	return []Quirk{QuirkIgnoreIfNoneMatch, QuirkNonAtomicPut, QuirkAcceptAnySignature, QuirkNoContinuation, QuirkUnsortedListing,
		QuirkIgnoreRange, QuirkRangeOffByOne, QuirkTornPutVisible, QuirkDeleteMissing404, QuirkDeleteIgnored, QuirkIgnorePrefix,
		QuirkIgnoreStartAfter, QuirkRepeatPageEdge, QuirkMissingKeyIs200, QuirkHeadMissing200, QuirkLosePutBody, QuirkListHidesRecent,
		QuirkIgnoreSkew, QuirkAcceptWrongPayload}
}
