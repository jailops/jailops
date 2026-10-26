package s3

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"jailops.net/jailops/internal/repo/datastore"
)

var _ datastore.Backend = (*Backend)(nil)

// ErrSize is returned by Put when the reader does not hold exactly size bytes.
// It is datastore.ErrSize, so callers match either name.
var ErrSize = datastore.ErrSize

// MaxKeyLength is the longest key S3 accepts, with the prefix.
const MaxKeyLength = 1024

// checkKey applies the repository's key rules to every call, so a key can
// never address anything outside the repository, then the S3 length limit.
func (b *Backend) checkKey(key string) error {
	if err := datastore.ValidKey(key); err != nil {
		return err
	}
	if len(b.prefix)+len(key) > MaxKeyLength {
		return fmt.Errorf("%w: key %q with the repository prefix is longer than %d bytes", datastore.ErrInvalidKey, key, MaxKeyLength)
	}
	return nil
}

// checkPrefix validates a listing prefix: like a key, but it may be empty,
// end in '/' and stop in the middle of a component.
func (b *Backend) checkPrefix(prefix string) error {
	if prefix == "" {
		return nil
	}
	bad := len(prefix) > MaxKeyLength || !utf8.ValidString(prefix) || strings.HasPrefix(prefix, "/") || strings.HasPrefix(prefix, datastore.TempPrefix)
	for _, r := range prefix {
		bad = bad || r < 0x20 || r == 0x7f || r == '\\'
	}
	for _, c := range strings.Split(strings.TrimSuffix(prefix, "/"), "/") {
		bad = bad || c == "" || c == "." || c == ".."
	}
	if bad || len(b.prefix)+len(prefix) > MaxKeyLength {
		return fmt.Errorf("%w: prefix %q", datastore.ErrInvalidKey, prefix)
	}
	return nil
}

func (b *Backend) signPayload() bool {
	switch b.cfg.Payload {
	case PayloadAlways:
		return true
	case PayloadNever:
		return false
	}
	return b.tgt.scheme == "http"
}

// prepared is a body that can be sent again.
type prepared struct {
	open    func() (io.Reader, error)
	payload string
}

func (b *Backend) prepare(r io.Reader, size int64) (*prepared, error) {
	if size < 0 {
		return nil, errors.New("s3: negative size")
	}
	sign := b.signPayload()
	if rs, ok := r.(io.ReadSeeker); ok {
		start, err := rs.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, err
		}
		end, err := rs.Seek(0, io.SeekEnd)
		if err != nil {
			return nil, err
		}
		if end-start != size {
			return nil, fmt.Errorf("%w: the reader holds %d bytes, size is %d", ErrSize, end-start, size)
		}
		p := &prepared{payload: unsignedBody, open: func() (io.Reader, error) {
			if _, err := rs.Seek(start, io.SeekStart); err != nil {
				return nil, err
			}
			return io.LimitReader(rs, size), nil
		}}
		if sign {
			rd, err := p.open()
			if err != nil {
				return nil, err
			}
			h := sha256.New()
			if n, err := io.Copy(h, rd); err != nil || n != size {
				return nil, fmt.Errorf("%w: hashing the body: %v", ErrSize, err)
			}
			p.payload = hex.EncodeToString(h.Sum(nil))
		}
		return p, nil
	}
	if size > b.maxBuf {
		return nil, fmt.Errorf("s3: a %d byte body that cannot seek exceeds the %d byte retry buffer", size, b.maxBuf)
	}
	data := make([]byte, size)
	n, err := io.ReadFull(r, data)
	if err != nil {
		return nil, fmt.Errorf("%w: the reader ended after %d bytes: %v", ErrSize, n, err)
	}
	var one [1]byte
	if m, _ := r.Read(one[:]); m > 0 {
		return nil, fmt.Errorf("%w: the reader holds more than %d bytes", ErrSize, size)
	}
	p := &prepared{payload: unsignedBody, open: func() (io.Reader, error) { return bytes.NewReader(data), nil }}
	if sign {
		p.payload = sha256Hex(data)
	}
	return p, nil
}

// Put implements datastore.Backend. The whole object goes up in one PUT. With
// IfAbsent the request carries If-None-Match: *, so the service itself
// arbitrates concurrent creators and answers 412 to every loser.
func (b *Backend) Put(ctx context.Context, key string, r io.Reader, size int64, opts datastore.PutOptions) error {
	if err := b.checkKey(key); err != nil {
		return err
	}
	p, err := b.prepare(r, size)
	if err != nil {
		return err
	}
	h := http.Header{}
	h.Set("Content-Type", "application/octet-stream")
	if opts.IfAbsent {
		h.Set("If-None-Match", "*")
	}
	sp := &spec{op: "put", method: http.MethodPut, key: key, full: b.fullKey(key), header: h, body: p.open, size: size, payload: p.payload}
	resp, err := b.do(ctx, sp)
	if err == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return resp.Body.Close()
	}
	var e *Error
	if opts.IfAbsent && errors.As(err, &e) && e.Status == http.StatusPreconditionFailed {
		// A retry whose first attempt got through (the answer was lost) meets its own
		// object. If the stored bytes are the ones sent here, the write succeeded.
		if sp.ambiguous && b.sameAsStored(ctx, key, p, size) {
			return nil
		}
		return fmt.Errorf("s3 put %s: %w", key, datastore.ErrExists)
	}
	return err
}

// sameAsStored reports whether the object at key holds exactly the prepared body.
func (b *Backend) sameAsStored(ctx context.Context, key string, p *prepared, size int64) bool {
	info, err := b.Head(ctx, key)
	if err != nil || info.Size != size {
		return false
	}
	rc, err := b.Get(ctx, key)
	if err != nil {
		return false
	}
	defer rc.Close()
	got := sha256.New()
	if n, err := io.Copy(got, rc); err != nil || n != size {
		return false
	}
	rd, err := p.open()
	if err != nil {
		return false
	}
	want := sha256.New()
	if _, err := io.Copy(want, rd); err != nil {
		return false
	}
	return bytes.Equal(got.Sum(nil), want.Sum(nil))
}

// notFound maps a missing OBJECT to datastore.ErrNotFound. A missing bucket is
// a hard error: treating it as "no objects" would make every chunk look
// missing and every repository look empty.
func notFound(err error, op, key string) error {
	var e *Error
	if errors.As(err, &e) && e.Status == http.StatusNotFound && e.Code != "NoSuchBucket" {
		return fmt.Errorf("s3 %s %s: %w", op, key, datastore.ErrNotFound)
	}
	return err
}

// Get implements datastore.Backend.
func (b *Backend) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := b.checkKey(key); err != nil {
		return nil, err
	}
	resp, err := b.do(ctx, &spec{op: "get", method: http.MethodGet, key: key, full: b.fullKey(key)})
	if err != nil {
		return nil, notFound(err, "get", key)
	}
	return resp.Body, nil
}

// GetRange implements datastore.Backend.
func (b *Backend) GetRange(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	if err := b.checkKey(key); err != nil {
		return nil, err
	}
	if offset < 0 {
		return nil, errors.New("s3: negative offset")
	}
	if length == 0 {
		// nothing to read, but a missing key is still an error
		if _, err := b.Head(ctx, key); err != nil {
			return nil, err
		}
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	h := http.Header{}
	rng := "bytes=" + strconv.FormatInt(offset, 10) + "-"
	if length > 0 {
		rng += strconv.FormatInt(offset+length-1, 10)
	}
	h.Set("Range", rng)
	resp, err := b.do(ctx, &spec{op: "get", method: http.MethodGet, key: key, full: b.fullKey(key), header: h})
	if err != nil {
		var e *Error
		if errors.As(err, &e) && e.Status == http.StatusRequestedRangeNotSatisfiable && offset == 0 {
			// a range of the empty object is not satisfiable; the object itself is fine
			if info, herr := b.Head(ctx, key); herr == nil && info.Size == 0 {
				return io.NopCloser(bytes.NewReader(nil)), nil
			}
		}
		return nil, notFound(err, "get", key)
	}
	switch resp.StatusCode {
	case http.StatusPartialContent:
		if start, ok := contentRangeStart(resp.Header.Get("Content-Range")); !ok || start != offset {
			resp.Body.Close()
			return nil, fmt.Errorf("s3 get %s: the service answered range %q for a request starting at %d", key, resp.Header.Get("Content-Range"), offset)
		}
		if length > 0 && resp.ContentLength > length {
			resp.Body.Close()
			return nil, fmt.Errorf("s3 get %s: the service sent %d bytes for a range of %d", key, resp.ContentLength, length)
		}
	case http.StatusOK:
		// A service that ignores Range sends the whole object. Only the whole object is acceptable.
		if offset != 0 || length >= 0 {
			resp.Body.Close()
			return nil, fmt.Errorf("s3 get %s: the service ignored the Range header", key)
		}
	}
	return resp.Body, nil
}

func contentRangeStart(v string) (int64, bool) {
	rest, ok := strings.CutPrefix(v, "bytes ")
	if !ok {
		return 0, false
	}
	a, _, ok := strings.Cut(rest, "-")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(a, 10, 64)
	return n, err == nil
}

// Head implements datastore.Backend.
func (b *Backend) Head(ctx context.Context, key string) (datastore.ObjectInfo, error) {
	if err := b.checkKey(key); err != nil {
		return datastore.ObjectInfo{}, err
	}
	resp, err := b.do(ctx, &spec{op: "head", method: http.MethodHead, key: key, full: b.fullKey(key)})
	if err != nil {
		return datastore.ObjectInfo{}, notFound(err, "head", key)
	}
	defer resp.Body.Close()
	if resp.ContentLength < 0 {
		return datastore.ObjectInfo{}, fmt.Errorf("s3 head %s: the service sent no Content-Length", key)
	}
	mod, err := http.ParseTime(resp.Header.Get("Last-Modified"))
	if err != nil {
		// Garbage collection ages objects by this time. A missing time must never read as "old".
		return datastore.ObjectInfo{}, fmt.Errorf("s3 head %s: no usable Last-Modified: %w", key, err)
	}
	return datastore.ObjectInfo{Key: key, Size: resp.ContentLength, ModTime: mod.UTC()}, nil
}

// Delete implements datastore.Backend. Deleting a missing key succeeds.
func (b *Backend) Delete(ctx context.Context, key string) error {
	if err := b.checkKey(key); err != nil {
		return err
	}
	resp, err := b.do(ctx, &spec{op: "delete", method: http.MethodDelete, key: key, full: b.fullKey(key)})
	if err != nil {
		if errors.Is(notFound(err, "delete", key), datastore.ErrNotFound) {
			return nil
		}
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return resp.Body.Close()
}

// ---- listing ---------------------------------------------------------------

type xmlList struct {
	XMLName               xml.Name `xml:"ListBucketResult"`
	IsTruncated           bool     `xml:"IsTruncated"`
	NextContinuationToken string   `xml:"NextContinuationToken"`
	EncodingType          string   `xml:"EncodingType"`
	KeyCount              int      `xml:"KeyCount"`
	Contents              []struct {
		Key          string `xml:"Key"`
		LastModified string `xml:"LastModified"`
		Size         int64  `xml:"Size"`
	} `xml:"Contents"`
}

// Page is one page of a listing.
type Page struct {
	Objects []datastore.ObjectInfo
	// Next is the continuation token of the next page; "" at the end.
	Next string
}

// ListPage fetches one page of ListObjectsV2. prefix is relative to the
// repository prefix; startAfter (relative too) and token are optional; maxKeys
// 0 means the service default.
func (b *Backend) ListPage(ctx context.Context, prefix, startAfter, token string, maxKeys int) (*Page, error) {
	if err := b.checkPrefix(prefix); err != nil {
		return nil, err
	}
	params := map[string]string{"list-type": "2", "encoding-type": "url", "prefix": b.prefix + prefix}
	if startAfter != "" {
		params["start-after"] = b.prefix + startAfter
	}
	if token != "" {
		params["continuation-token"] = token
	}
	if maxKeys > 0 {
		params["max-keys"] = strconv.Itoa(maxKeys)
	}
	resp, err := b.do(ctx, &spec{op: "list", method: http.MethodGet, key: prefix, params: params})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return decodeList(io.LimitReader(resp.Body, 64<<20), b.prefix, prefix)
}

// decodeList parses a ListObjectsV2 answer. repoPrefix is the repository
// prefix on the service, prefix the one asked for (relative to it).
func decodeList(r io.Reader, repoPrefix, prefix string) (*Page, error) {
	var x xmlList
	if err := xml.NewDecoder(r).Decode(&x); err != nil {
		return nil, fmt.Errorf("s3 list %s: bad response: %w", prefix, err)
	}
	decode := func(s string) (string, error) { return s, nil }
	if strings.EqualFold(x.EncodingType, "url") {
		decode = url.QueryUnescape
	}
	pg := &Page{}
	for _, c := range x.Contents {
		k, err := decode(c.Key)
		if err != nil {
			return nil, fmt.Errorf("s3 list %s: undecodable key: %w", prefix, err)
		}
		rel, ok := strings.CutPrefix(k, repoPrefix)
		if !ok || !strings.HasPrefix(rel, prefix) || rel == "" {
			return nil, fmt.Errorf("s3 list %s: the service returned %q outside the prefix", prefix, k)
		}
		mod, err := time.Parse(time.RFC3339, c.LastModified)
		if err != nil {
			return nil, fmt.Errorf("s3 list %s: no usable LastModified for %q: %w", prefix, rel, err)
		}
		if c.Size < 0 {
			return nil, fmt.Errorf("s3 list %s: negative size for %q", prefix, rel)
		}
		pg.Objects = append(pg.Objects, datastore.ObjectInfo{Key: rel, Size: c.Size, ModTime: mod.UTC()})
	}
	if x.IsTruncated {
		if x.NextContinuationToken == "" {
			return nil, fmt.Errorf("s3 list %s: truncated listing without a continuation token", prefix)
		}
		pg.Next = x.NextContinuationToken
	}
	return pg, nil
}

// List implements datastore.Backend: every object under prefix, in lexical
// order, exactly once. A service that returns keys out of order or twice is
// an error, never silently accepted.
func (b *Backend) List(ctx context.Context, prefix string, fn func(datastore.ObjectInfo) error) error {
	return b.ListFrom(ctx, prefix, "", fn)
}

// ListFrom is List starting after the key startAfter.
func (b *Backend) ListFrom(ctx context.Context, prefix, startAfter string, fn func(datastore.ObjectInfo) error) error {
	token, last, sa := "", startAfter, startAfter
	seen := map[string]bool{}
	for {
		pg, err := b.ListPage(ctx, prefix, sa, token, 0)
		if err != nil {
			return err
		}
		for _, o := range pg.Objects {
			if last != "" && o.Key <= last {
				return fmt.Errorf("s3 list %s: the service returned %q after %q (out of order or repeated)", prefix, o.Key, last)
			}
			last = o.Key
			if err := fn(o); err != nil {
				return err
			}
		}
		if pg.Next == "" {
			return nil
		}
		if seen[pg.Next] {
			return fmt.Errorf("s3 list %s: the service repeated a continuation token", prefix)
		}
		seen[pg.Next] = true
		token, sa = pg.Next, ""
	}
}

// ---- bucket level (setup, probe and tests) ---------------------------------

// HeadBucket reports whether the bucket exists and is reachable with these credentials.
func (b *Backend) HeadBucket(ctx context.Context) error {
	resp, err := b.do(ctx, &spec{op: "head-bucket", method: http.MethodHead})
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// MakeBucket creates the bucket. It is for tests and first-time setup; the
// repository itself never creates buckets.
func (b *Backend) MakeBucket(ctx context.Context) error {
	sp := &spec{op: "make-bucket", method: http.MethodPut}
	if b.cfg.Region != "" && b.cfg.Region != "us-east-1" {
		body := `<CreateBucketConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><LocationConstraint>` + xmlEscape(b.cfg.Region) + `</LocationConstraint></CreateBucketConfiguration>`
		sp.body = func() (io.Reader, error) { return strings.NewReader(body), nil }
		sp.size = int64(len(body))
		sp.payload = sha256Hex([]byte(body))
	}
	resp, err := b.do(ctx, sp)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// RemoveBucket deletes an empty bucket (tests).
func (b *Backend) RemoveBucket(ctx context.Context) error {
	resp, err := b.do(ctx, &spec{op: "remove-bucket", method: http.MethodDelete})
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func xmlEscape(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}
