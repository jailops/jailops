package s3fake

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	algoV4       = "AWS4-HMAC-SHA256"
	unsignedBody = "UNSIGNED-PAYLOAD"
	maxSkew      = 15 * time.Minute
)

// authInfo is what a verified request carries on.
type authInfo struct {
	access      string
	payloadHash string // the x-amz-content-sha256 header
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func signingKey(secret, day, region string) []byte {
	k := hmacSHA256([]byte("AWS4"+secret), day)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, "s3")
	return hmacSHA256(k, "aws4_request")
}

func sha256Hex(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// uriEncode is the AWS canonical encoding: everything but A-Za-z0-9-_.~ is
// %XX; '/' stays when keepSlash.
func uriEncode(s string, keepSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && keepSlash:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func canonicalQuery(raw string) string {
	if raw == "" {
		return ""
	}
	type kv struct{ k, v string }
	var kvs []kv
	for _, part := range strings.Split(raw, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		uk, err1 := url.QueryUnescape(k)
		uv, err2 := url.QueryUnescape(v)
		if err1 != nil || err2 != nil {
			uk, uv = k, v
		}
		kvs = append(kvs, kv{uriEncode(uk, false), uriEncode(uv, false)})
	}
	sort.Slice(kvs, func(i, j int) bool {
		if kvs[i].k != kvs[j].k {
			return kvs[i].k < kvs[j].k
		}
		return kvs[i].v < kvs[j].v
	})
	out := make([]string, len(kvs))
	for i, e := range kvs {
		out[i] = e.k + "=" + e.v
	}
	return strings.Join(out, "&")
}

func trimSpaces(s string) string { return strings.Join(strings.Fields(s), " ") }

// verifySigV4 checks the Authorization header of r against the server's users.
// Everything is recomputed from the AWS specification (decoded path re-encoded,
// query re-sorted, signed headers as named), never copied from a client.
func (s *Server) verifySigV4(r *http.Request) (*authInfo, *apiError) {
	if s.quirk(QuirkAcceptAnySignature) {
		return &authInfo{access: s.opt.Access, payloadHash: r.Header.Get("x-amz-content-sha256")}, nil
	}
	q := r.URL.Query()
	if q.Has("X-Amz-Algorithm") || q.Has("X-Amz-Signature") {
		return nil, apiErrf("NotImplemented", "presigned URLs are not implemented by the test server")
	}
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return nil, apiErrf("AccessDenied", "Access Denied.")
	}
	rest, ok := strings.CutPrefix(auth, algoV4+" ")
	if !ok {
		return nil, apiErrf("AuthorizationHeaderMalformed", "only AWS4-HMAC-SHA256 is supported")
	}
	fields := map[string]string{}
	for _, p := range strings.Split(rest, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
		if !ok {
			return nil, apiErr("AuthorizationHeaderMalformed")
		}
		fields[k] = v
	}
	cred, signedList, sig := fields["Credential"], fields["SignedHeaders"], fields["Signature"]
	if cred == "" || signedList == "" || sig == "" {
		return nil, apiErr("AuthorizationHeaderMalformed")
	}
	cp := strings.Split(cred, "/")
	if len(cp) != 5 || cp[4] != "aws4_request" || cp[3] != "s3" {
		return nil, apiErrf("AuthorizationHeaderMalformed", "the credential scope is malformed")
	}
	access, day, region := cp[0], cp[1], cp[2]
	secret, known := s.secretFor(access)
	if !known {
		return nil, apiErr("InvalidAccessKeyId")
	}
	if region != s.opt.Region {
		return nil, apiErrf("AuthorizationHeaderMalformed", fmt.Sprintf("the region '%s' is wrong; expecting '%s'", region, s.opt.Region))
	}
	amzDate := r.Header.Get("X-Amz-Date")
	if amzDate == "" {
		amzDate = r.Header.Get("Date")
	}
	t, err := time.Parse("20060102T150405Z", amzDate)
	if err != nil {
		if t, err = http.ParseTime(amzDate); err != nil {
			return nil, apiErrf("AccessDenied", "missing or malformed request date")
		}
	}
	if t.UTC().Format("20060102") != day {
		return nil, apiErrf("AuthorizationHeaderMalformed", "the credential date does not match the request date")
	}
	if d := time.Since(t); (d > maxSkew || d < -maxSkew) && !s.quirk(QuirkIgnoreSkew) {
		return nil, apiErr("RequestTimeTooSkewed")
	}
	signed := strings.Split(signedList, ";")
	isSigned := map[string]bool{}
	for _, h := range signed {
		isSigned[h] = true
	}
	if !isSigned["host"] {
		return nil, apiErrf("AccessDenied", "the host header must be signed")
	}
	payload := r.Header.Get("X-Amz-Content-Sha256")
	if payload == "" {
		return nil, apiErrf("InvalidRequest", "missing required header x-amz-content-sha256")
	}
	if !isSigned["x-amz-content-sha256"] {
		return nil, apiErrf("AccessDenied", "x-amz-content-sha256 must be signed")
	}
	// like S3: every x-amz-* header that is present must be signed (a header added in flight is rejected)
	for name := range r.Header {
		l := strings.ToLower(name)
		if strings.HasPrefix(l, "x-amz-") && !isSigned[l] {
			return nil, apiErrf("AccessDenied", "there were headers present in the request which were not signed: "+l)
		}
	}
	var ch strings.Builder
	for _, h := range signed {
		var v string
		switch h {
		case "host":
			v = r.Host
		case "content-length":
			v = strconv.FormatInt(r.ContentLength, 10)
		default:
			vals, present := r.Header[http.CanonicalHeaderKey(h)]
			if !present {
				return nil, apiErrf("SignatureDoesNotMatch", "a signed header is missing: "+h)
			}
			parts := make([]string, len(vals))
			for i, x := range vals {
				parts[i] = trimSpaces(x)
			}
			v = strings.Join(parts, ",")
		}
		ch.WriteString(h + ":" + v + "\n")
	}
	path := r.URL.Path
	if path == "" {
		path = "/"
	}
	canonical := strings.Join([]string{r.Method, uriEncode(path, true), canonicalQuery(r.URL.RawQuery), ch.String(), signedList, payload}, "\n")
	scope := strings.Join(cp[1:], "/")
	sts := strings.Join([]string{algoV4, t.UTC().Format("20060102T150405Z"), scope, sha256Hex([]byte(canonical))}, "\n")
	want := hex.EncodeToString(hmacSHA256(signingKey(secret, day, region), sts))
	if !hmac.Equal([]byte(want), []byte(strings.ToLower(sig))) {
		return nil, apiErr("SignatureDoesNotMatch")
	}
	return &authInfo{access: access, payloadHash: payload}, nil
}
