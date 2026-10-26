package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"jailops.net/jailops/sdk/api"
)

var urlEnc = base64.RawURLEncoding

// NewSessionID returns a random session id (cookie value) and its SHA-256,
// which is what the database stores.
func NewSessionID() (id string, hash []byte, err error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", nil, fmt.Errorf("auth: entropy: %w", err)
	}
	id = urlEnc.EncodeToString(b[:])
	return id, HashSessionID(id), nil
}

// HashSessionID is the SHA-256 of the cookie value.
func HashSessionID(id string) []byte {
	h := sha256.Sum256([]byte("jailops-session:" + id))
	return h[:]
}

// NewCSRFToken returns a random CSRF token bound to one session.
func NewCSRFToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("auth: entropy: %w", err)
	}
	return urlEnc.EncodeToString(b[:]), nil
}

// CSRFValid compares the session's token with the one the client sent (the
// X-CSRF-Token header) in constant time.
func CSRFValid(sessionToken, presented string) bool {
	if sessionToken == "" || len(sessionToken) != len(presented) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(sessionToken), []byte(presented)) == 1
}

// Cookie names. With TLS the __Host- prefix makes browsers refuse a cookie
// set from another origin or path.
const (
	SessionCookieName       = "jailops_session"
	SessionCookieNameSecure = "__Host-jailops_session"
)

// SessionCookie builds the session cookie: HttpOnly, SameSite=Strict, Secure
// over TLS, scoped to the whole site.
func SessionCookie(id string, secure bool, maxAge time.Duration) *http.Cookie {
	name := SessionCookieName
	if secure {
		name = SessionCookieNameSecure
	}
	return &http.Cookie{Name: name, Value: id, Path: "/", HttpOnly: true, Secure: secure,
		SameSite: http.SameSiteStrictMode, MaxAge: int(maxAge / time.Second)}
}

// ClearSessionCookie expires the cookie.
func ClearSessionCookie(secure bool) *http.Cookie {
	c := SessionCookie("", secure, 0)
	c.MaxAge = -1
	return c
}

// SessionIDFromRequest reads the session cookie. Over TLS only the __Host- cookie counts and over plain HTTP only
// the plain one: a cookie the other transport could have planted (a plain-HTTP response can set a cookie for the
// whole site, but never one with the __Host- prefix) is not a session.
func SessionIDFromRequest(r *http.Request) (string, bool) {
	n := SessionCookieName
	if r.TLS != nil {
		n = SessionCookieNameSecure
	}
	if c, err := r.Cookie(n); err == nil && c.Value != "" {
		return c.Value, true
	}
	return "", false
}

// CheckOrigin is the second CSRF layer: a state-changing browser request must
// carry an Origin (or Referer) whose host equals the request host. Requests
// with neither header are refused for session-authenticated callers.
func CheckOrigin(r *http.Request) error {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return nil
	}
	src := r.Header.Get("Origin")
	if src == "" || src == "null" {
		src = r.Header.Get("Referer")
	}
	if src == "" {
		return api.Errorf(api.CodeForbidden, "missing Origin header")
	}
	u, err := url.Parse(src)
	if err != nil || u.Host == "" || u.Host != r.Host {
		return api.Errorf(api.CodeForbidden, "cross-origin request refused")
	}
	return nil
}
