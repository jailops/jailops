package s3

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	algoV4        = "AWS4-HMAC-SHA256"
	unsignedBody  = "UNSIGNED-PAYLOAD"
	emptyPayload  = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	amzDateFormat = "20060102T150405Z"
)

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
// %XX (upper-case hex); '/' stays when keepSlash.
func uriEncode(s string, keepSlash bool) string {
	const hexd = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && keepSlash:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexd[c>>4])
			b.WriteByte(hexd[c&15])
		}
	}
	return b.String()
}

// canonicalQuery encodes and sorts query parameters. The same string is sent
// on the wire, so what is signed is what is received.
func canonicalQuery(params map[string]string) string {
	if len(params) == 0 {
		return ""
	}
	type kv struct{ k, v string }
	kvs := make([]kv, 0, len(params))
	for k, v := range params {
		kvs = append(kvs, kv{uriEncode(k, false), uriEncode(v, false)})
	}
	sort.Slice(kvs, func(i, j int) bool { return kvs[i].k < kvs[j].k })
	parts := make([]string, len(kvs))
	for i, e := range kvs {
		parts[i] = e.k + "=" + e.v
	}
	return strings.Join(parts, "&")
}

// signedHeaderNames are the optional headers that are signed when present.
// Every x-amz-* header is always signed, and host.
var signedHeaderNames = []string{"content-md5", "content-type", "if-none-match", "range"}

// signV4 signs req in place: it sets X-Amz-Date, X-Amz-Content-Sha256, the
// session token when there is one, and Authorization. payloadHash is a hex
// SHA-256 or UNSIGNED-PAYLOAD.
func signV4(req *http.Request, c Credentials, region string, payloadHash string, now time.Time) {
	now = now.UTC()
	amzDate := now.Format(amzDateFormat)
	day := now.Format("20060102")
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if c.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", c.SessionToken)
	}
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	names := []string{"host"}
	for _, n := range signedHeaderNames {
		if req.Header.Get(n) != "" {
			names = append(names, n)
		}
	}
	for k := range req.Header {
		if l := strings.ToLower(k); strings.HasPrefix(l, "x-amz-") {
			names = append(names, l)
		}
	}
	sort.Strings(names)
	var ch strings.Builder
	for _, n := range names {
		v := host
		if n != "host" {
			v = strings.Join(strings.Fields(strings.Join(req.Header.Values(n), ",")), " ")
		}
		ch.WriteString(n + ":" + v + "\n")
	}
	signed := strings.Join(names, ";")
	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	canonical := strings.Join([]string{req.Method, path, req.URL.RawQuery, ch.String(), signed, payloadHash}, "\n")
	scope := day + "/" + region + "/s3/aws4_request"
	sts := strings.Join([]string{algoV4, amzDate, scope, sha256Hex([]byte(canonical))}, "\n")
	sig := hex.EncodeToString(hmacSHA256(signingKey(c.SecretKey, day, region), sts))
	req.Header.Set("Authorization", algoV4+" Credential="+c.AccessKey+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
}

// encodePath is the escaped form of an object path.
func encodePath(p string) string { return uriEncode(p, true) }

// buildURL assembles a request URL whose escaped path and query are exactly
// what is signed.
func buildURL(scheme, host, rawPath string, params map[string]string) *url.URL {
	dec, err := url.PathUnescape(rawPath)
	if err != nil {
		dec = rawPath
	}
	return &url.URL{Scheme: scheme, Host: host, Path: dec, RawPath: rawPath, RawQuery: canonicalQuery(params)}
}

// SignRequest signs req with SigV4 in place (X-Amz-Date, X-Amz-Content-Sha256,
// Authorization). payloadHash is the hex SHA-256 of the body or
// "UNSIGNED-PAYLOAD"; an empty value means the empty body. It exists for tests
// and for the conformance suite, which issues hand-built requests.
func SignRequest(req *http.Request, c Credentials, region, payloadHash string, now time.Time) {
	if payloadHash == "" {
		payloadHash = emptyPayload
	}
	signV4(req, c, region, payloadHash, now)
}

// EncodePath is the S3 escaping of an object path, for hand-built requests.
func EncodePath(p string) string { return encodePath(p) }

// CanonicalQuery encodes and sorts query parameters the way they are signed.
func CanonicalQuery(params map[string]string) string { return canonicalQuery(params) }
