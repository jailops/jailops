package s3fake

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"hash"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	xmlns         = "http://s3.amazonaws.com/doc/2006-03-01/"
	maxObjectSize = 5 << 30
	maxKeyLen     = 1024
)

func writeXML(w http.ResponseWriter, status int, v any) {
	b, _ := xml.Marshal(v)
	out := append([]byte(xml.Header), b...)
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Content-Length", itoa(len(out)))
	w.WriteHeader(status)
	_, _ = w.Write(out)
}

func isoTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func quoteETag(e string) string { return `"` + e + `"` }

// dispatch authenticates and routes one request. w may be a mute writer.
func (s *Server) dispatch(w http.ResponseWriter, r *http.Request, rc *reqCtx, dropAfter int64) {
	w.Header().Set("Server", serverName)
	w.Header().Set("x-amz-request-id", rc.id)
	w.Header().Set("Accept-Ranges", "bytes")
	a, aerr := s.verifySigV4(r)
	if aerr != nil {
		writeError(w, r, rc, aerr)
		return
	}
	if strings.HasPrefix(rc.op, "Unsupported:") {
		writeError(w, r, rc, apiErrf("NotImplemented", rc.op+" is not implemented by the test server (the code under test must not use it)"))
		return
	}
	for _, h := range []string{"If-Modified-Since", "If-Unmodified-Since", "If-Match", "X-Amz-Copy-Source", "X-Amz-Tagging", "X-Amz-Server-Side-Encryption",
		"X-Amz-Storage-Class", "X-Amz-Acl", "X-Amz-Checksum-Algorithm", "X-Amz-Trailer"} {
		if r.Header.Get(h) != "" {
			writeError(w, r, rc, apiErrf("NotImplemented", h+" is not implemented by the test server"))
			return
		}
	}
	if len(rc.key) > maxKeyLen {
		writeError(w, r, rc, apiErr("KeyTooLongError"))
		return
	}
	var err *apiError
	switch rc.op {
	case "CreateBucket":
		err = s.createBucket(w, r, rc, a)
	case "DeleteBucket":
		err = s.deleteBucket(w, rc)
	case "HeadBucket":
		err = s.headBucket(w, rc)
	case "ListObjectsV2":
		err = s.listV2(w, r, rc)
	case "PutObject":
		err = s.putObject(w, r, rc, a, dropAfter)
	case "GetObject", "HeadObject":
		err = s.getObject(w, r, rc)
	case "DeleteObject":
		err = s.deleteObject(w, rc)
	default:
		err = apiErr("MethodNotAllowed")
	}
	if err != nil {
		if err.Code == "dropped" {
			abortConn(w)
			return
		}
		writeError(w, r, rc, err)
	}
}

func (s *Server) lookup(rc *reqCtx) (*bucket, *apiError) {
	b := s.st.bucket(rc.bucket)
	if b == nil {
		return nil, apiErr("NoSuchBucket")
	}
	return b, nil
}

// ---- buckets ---------------------------------------------------------------

func (s *Server) createBucket(w http.ResponseWriter, r *http.Request, rc *reqCtx, a *authInfo) *apiError {
	if !validBucketName(rc.bucket) {
		return apiErr("InvalidBucketName")
	}
	body, aerr := readSmall(r, a, s)
	if aerr != nil {
		return aerr
	}
	loc := "us-east-1"
	if len(strings.TrimSpace(string(body))) > 0 {
		var cfg struct {
			XMLName    xml.Name `xml:"CreateBucketConfiguration"`
			Constraint string   `xml:"LocationConstraint"`
		}
		if err := xml.Unmarshal(body, &cfg); err != nil {
			return apiErrf("InvalidRequest", "malformed CreateBucketConfiguration")
		}
		if cfg.Constraint != "" {
			loc = cfg.Constraint
		}
	}
	if loc != s.opt.Region {
		return apiErrf("InvalidRequest", "the location constraint is incompatible with the region of this endpoint")
	}
	if _, created := s.st.create(rc.bucket); !created {
		return apiErr("BucketAlreadyOwnedByYou")
	}
	w.Header().Set("Location", "/"+rc.bucket)
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(200)
	return nil
}

func (s *Server) deleteBucket(w http.ResponseWriter, rc *reqCtx) *apiError {
	b, aerr := s.lookup(rc)
	if aerr != nil {
		return aerr
	}
	b.mu.RLock()
	empty := len(b.objs) == 0
	b.mu.RUnlock()
	if !empty {
		return apiErr("BucketNotEmpty")
	}
	s.st.remove(rc.bucket)
	w.WriteHeader(204)
	return nil
}

func (s *Server) headBucket(w http.ResponseWriter, rc *reqCtx) *apiError {
	if _, aerr := s.lookup(rc); aerr != nil {
		return aerr
	}
	w.Header().Set("x-amz-bucket-region", s.opt.Region)
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(200)
	return nil
}

// readSmall reads a small request body (XML), verifying the payload hash.
func readSmall(r *http.Request, a *authInfo, s *Server) ([]byte, *apiError) {
	verify, aerr := s.payloadCheck(a)
	if aerr != nil {
		return nil, aerr
	}
	h := verify.hasher()
	data, err := io.ReadAll(io.LimitReader(io.TeeReader(r.Body, h), 1<<20+1))
	if err != nil {
		return nil, apiErrf("IncompleteBody", err.Error())
	}
	if len(data) > 1<<20 {
		return nil, apiErr("EntityTooLarge")
	}
	return data, verify.check(h)
}

// payloadVerifier checks x-amz-content-sha256 against what arrived.
type payloadVerifier struct {
	want string // lower-case hex, or "" for UNSIGNED-PAYLOAD
}

func (s *Server) payloadCheck(a *authInfo) (payloadVerifier, *apiError) {
	switch {
	case a.payloadHash == unsignedBody:
		return payloadVerifier{}, nil
	case strings.HasPrefix(a.payloadHash, "STREAMING-"):
		return payloadVerifier{}, apiErrf("NotImplemented", "streaming (aws-chunked) payloads are not implemented by the test server")
	case len(a.payloadHash) == 64:
		if _, err := hex.DecodeString(a.payloadHash); err != nil {
			return payloadVerifier{}, apiErrf("InvalidArgument", "x-amz-content-sha256 must be UNSIGNED-PAYLOAD or a hex SHA-256")
		}
		if s.quirk(QuirkAcceptWrongPayload) {
			return payloadVerifier{}, nil
		}
		return payloadVerifier{want: strings.ToLower(a.payloadHash)}, nil
	}
	return payloadVerifier{}, apiErrf("InvalidArgument", "x-amz-content-sha256 must be UNSIGNED-PAYLOAD or a hex SHA-256")
}

func (v payloadVerifier) hasher() hash.Hash { return sha256.New() }

func (v payloadVerifier) check(h hash.Hash) *apiError {
	if v.want != "" && hex.EncodeToString(h.Sum(nil)) != v.want {
		return apiErr("XAmzContentSHA256Mismatch")
	}
	return nil
}

// ---- listing ---------------------------------------------------------------

type xmlContents struct {
	Key          string `xml:"Key"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	Size         int64  `xml:"Size"`
	StorageClass string `xml:"StorageClass"`
}
type xmlListV2 struct {
	XMLName               xml.Name      `xml:"ListBucketResult"`
	Xmlns                 string        `xml:"xmlns,attr"`
	Name                  string        `xml:"Name"`
	Prefix                string        `xml:"Prefix"`
	MaxKeys               int           `xml:"MaxKeys"`
	EncodingType          string        `xml:"EncodingType,omitempty"`
	KeyCount              int           `xml:"KeyCount"`
	IsTruncated           bool          `xml:"IsTruncated"`
	ContinuationToken     string        `xml:"ContinuationToken,omitempty"`
	NextContinuationToken string        `xml:"NextContinuationToken,omitempty"`
	StartAfter            string        `xml:"StartAfter,omitempty"`
	Contents              []xmlContents `xml:"Contents"`
}

func (s *Server) listV2(w http.ResponseWriter, r *http.Request, rc *reqCtx) *apiError {
	b, aerr := s.lookup(rc)
	if aerr != nil {
		return aerr
	}
	q := r.URL.Query()
	for k := range q {
		switch k {
		case "list-type", "prefix", "continuation-token", "start-after", "max-keys", "encoding-type":
		default:
			return apiErrf("NotImplemented", "list parameter "+k+" is not implemented by the test server")
		}
	}
	prefix := q.Get("prefix")
	if s.quirk(QuirkIgnorePrefix) {
		prefix = ""
	}
	maxKeys := 1000
	if v := q.Get("max-keys"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return apiErrf("InvalidArgument", "Provided max-keys not an integer or within integer range")
		}
		maxKeys = min(n, 1000)
	}
	enc := func(s string) string { return s }
	switch q.Get("encoding-type") {
	case "":
	case "url":
		enc = url.QueryEscape
	default:
		return apiErrf("InvalidArgument", "Invalid Encoding Method specified in Request")
	}
	startAfter, token := q.Get("start-after"), q.Get("continuation-token")
	if s.quirk(QuirkIgnoreStartAfter) {
		startAfter = ""
	}
	marker := ""
	switch {
	case token != "":
		raw, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil {
			return apiErrf("InvalidArgument", "The continuation token provided is incorrect")
		}
		marker = string(raw)
	case startAfter != "":
		marker = startAfter
	}

	b.mu.RLock()
	defer b.mu.RUnlock()
	keys := b.keys
	i := sort.SearchStrings(keys, prefix)
	if marker != "" {
		if j := sort.SearchStrings(keys, marker+"\x00"); j > i {
			i = j
		}
		if s.quirk(QuirkRepeatPageEdge) && token != "" && i > 0 {
			i-- // the last key of the previous page comes again
		}
	}
	out := xmlListV2{Xmlns: xmlns, Name: rc.bucket, Prefix: enc(prefix), MaxKeys: maxKeys, EncodingType: q.Get("encoding-type"),
		ContinuationToken: token, StartAfter: enc(startAfter)}
	limit := len(keys)
	count, truncated := 0, false
	var last string
	for i < limit && strings.HasPrefix(keys[i], prefix) {
		k := keys[i]
		o := b.objs[k]
		if s.quirk(QuirkListHidesRecent) && time.Since(o.mod) < 2*time.Second {
			i++
			continue
		}
		if count >= maxKeys {
			truncated = maxKeys > 0
			break
		}
		out.Contents = append(out.Contents, xmlContents{Key: enc(k), LastModified: isoTime(o.mod), ETag: quoteETag(o.etag), Size: int64(len(o.data)), StorageClass: "STANDARD"})
		last = k
		count++
		i++
	}
	out.KeyCount = count
	if truncated && !s.quirk(QuirkNoContinuation) {
		out.IsTruncated = true
		out.NextContinuationToken = base64.RawURLEncoding.EncodeToString([]byte(last))
	}
	if s.quirk(QuirkUnsortedListing) {
		for x, y := 0, len(out.Contents)-1; x < y; x, y = x+1, y-1 {
			out.Contents[x], out.Contents[y] = out.Contents[y], out.Contents[x]
		}
	}
	writeXML(w, 200, out)
	return nil
}

// ---- objects ---------------------------------------------------------------

type dropReader struct {
	r    io.Reader
	left int64
}

func (d *dropReader) Read(p []byte) (int, error) {
	if d.left <= 0 {
		return 0, errDropped
	}
	if int64(len(p)) > d.left {
		p = p[:d.left]
	}
	n, err := d.r.Read(p)
	d.left -= int64(n)
	return n, err
}

func (s *Server) putObject(w http.ResponseWriter, r *http.Request, rc *reqCtx, a *authInfo, dropAfter int64) *apiError {
	inm := r.Header.Get("If-None-Match")
	if inm != "" && inm != "*" {
		return apiErrf("NotImplemented", "only If-None-Match: * is implemented on PutObject")
	}
	if r.Header.Get("Expect") != "" && !strings.EqualFold(r.Header.Get("Expect"), "100-continue") {
		return apiErr("InvalidRequest")
	}
	b, aerr := s.lookup(rc)
	if aerr != nil {
		return aerr
	}
	if r.ContentLength < 0 {
		return apiErr("MissingContentLength")
	}
	if r.ContentLength > maxObjectSize {
		return apiErr("EntityTooLarge")
	}
	verify, aerr := s.payloadCheck(a)
	if aerr != nil {
		return aerr
	}
	// Collect the whole body first: nothing is visible to readers until it is
	// complete and verified, so a torn upload leaves no trace.
	hs, md := verify.hasher(), md5.New()
	var src io.Reader = r.Body
	if dropAfter > 0 {
		src = &dropReader{r: src, left: dropAfter}
	}
	var buf bytes.Buffer
	_, cerr := io.Copy(io.MultiWriter(&buf, hs, md), io.LimitReader(src, r.ContentLength+1))
	rc.rec.Bytes = int64(buf.Len())
	if cerr != nil {
		if s.quirk(QuirkTornPutVisible) {
			b.mu.Lock()
			b.put(rc.key, append([]byte(nil), buf.Bytes()...), time.Now())
			b.mu.Unlock()
		}
		if errors.Is(cerr, errDropped) {
			return &apiError{Code: "dropped", Message: errDropped.Error()}
		}
		return apiErrf("IncompleteBody", cerr.Error())
	}
	if int64(buf.Len()) != r.ContentLength {
		return apiErr("IncompleteBody")
	}
	if aerr := verify.check(hs); aerr != nil {
		return aerr
	}
	if m := r.Header.Get("Content-MD5"); m != "" && base64.StdEncoding.EncodeToString(md.Sum(nil)) != m {
		return apiErr("BadDigest")
	}
	data := buf.Bytes()
	if s.quirk(QuirkLosePutBody) && len(data) > 1 {
		data = data[:len(data)-1]
	}
	cond := inm == "*" && !s.quirk(QuirkIgnoreIfNoneMatch)
	if cond && s.quirk(QuirkNonAtomicPut) {
		b.mu.RLock()
		_, exists := b.objs[rc.key]
		b.mu.RUnlock()
		if exists {
			return apiErr("PreconditionFailed")
		}
		time.Sleep(3 * time.Millisecond)
		cond = false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if s.st.bucket(rc.bucket) != b {
		return apiErr("NoSuchBucket")
	}
	if cond {
		if _, exists := b.objs[rc.key]; exists {
			return apiErr("PreconditionFailed")
		}
	}
	b.put(rc.key, data, time.Now())
	w.Header().Set("ETag", quoteETag(b.objs[rc.key].etag))
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(200)
	return nil
}

// parseRange handles one "bytes=a-b" range; ok=false means "no (usable) range: send everything".
func parseRange(h string, size int64) (start, length int64, ok bool, err *apiError) {
	spec, found := strings.CutPrefix(h, "bytes=")
	if !found || strings.Contains(spec, ",") {
		return 0, 0, false, nil
	}
	a, b, found := strings.Cut(spec, "-")
	if !found {
		return 0, 0, false, nil
	}
	if a == "" { // suffix
		n, e := strconv.ParseInt(b, 10, 64)
		if e != nil || n < 0 {
			return 0, 0, false, nil
		}
		if n == 0 || size == 0 {
			return 0, 0, false, apiErr("InvalidRange")
		}
		n = min(n, size)
		return size - n, n, true, nil
	}
	first, e := strconv.ParseInt(a, 10, 64)
	if e != nil || first < 0 {
		return 0, 0, false, nil
	}
	last := size - 1
	if b != "" {
		l, e := strconv.ParseInt(b, 10, 64)
		if e != nil || l < first {
			return 0, 0, false, nil
		}
		last = min(l, size-1)
	}
	if first >= size {
		return 0, 0, false, apiErr("InvalidRange")
	}
	return first, last - first + 1, true, nil
}

func (s *Server) getObject(w http.ResponseWriter, r *http.Request, rc *reqCtx) *apiError {
	b, aerr := s.lookup(rc)
	if aerr != nil {
		return aerr
	}
	if r.Header.Get("If-None-Match") != "" {
		return apiErrf("NotImplemented", "conditional reads are not implemented by the test server")
	}
	b.mu.RLock()
	o := b.objs[rc.key]
	b.mu.RUnlock() // objects are immutable: stream without holding the bucket lock
	if o == nil {
		if r.Method == http.MethodHead && s.quirk(QuirkHeadMissing200) {
			w.Header().Set("Content-Length", "0")
			w.WriteHeader(200)
			return nil
		}
		if r.Method == http.MethodGet && s.quirk(QuirkMissingKeyIs200) {
			w.Header().Set("Content-Length", "0")
			w.WriteHeader(200)
			return nil
		}
		return apiErr("NoSuchKey")
	}
	h := w.Header()
	h.Set("ETag", quoteETag(o.etag))
	h.Set("Last-Modified", o.mod.UTC().Format(http.TimeFormat))
	h.Set("Content-Type", "binary/octet-stream")
	size := int64(len(o.data))
	start, length, ranged := int64(0), size, false
	if rg := r.Header.Get("Range"); rg != "" && !s.quirk(QuirkIgnoreRange) {
		st, ln, ok, err := parseRange(rg, size)
		if err != nil {
			h.Del("ETag")
			h.Del("Last-Modified")
			h.Set("Content-Range", "bytes */"+strconv.FormatInt(size, 10))
			return err
		}
		if ok {
			if s.quirk(QuirkRangeOffByOne) && st+ln < size {
				ln++
			}
			start, length, ranged = st, ln, true
		}
	}
	h.Set("Content-Length", strconv.FormatInt(length, 10))
	if ranged {
		h.Set("Content-Range", "bytes "+strconv.FormatInt(start, 10)+"-"+strconv.FormatInt(start+length-1, 10)+"/"+strconv.FormatInt(size, 10))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	if r.Method == http.MethodHead || length == 0 {
		return nil
	}
	_, _ = w.Write(o.data[start : start+length])
	return nil
}

func (s *Server) deleteObject(w http.ResponseWriter, rc *reqCtx) *apiError {
	b, aerr := s.lookup(rc)
	if aerr != nil {
		return aerr
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.objs[rc.key]; !ok && s.quirk(QuirkDeleteMissing404) {
		return apiErr("NoSuchKey")
	}
	if !s.quirk(QuirkDeleteIgnored) {
		b.remove(rc.key)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
