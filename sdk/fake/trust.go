package fake

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"jailops.net/jailops/sdk/api"
)

var b64 = base64.RawURLEncoding

// trustRec is what the server remembers about an issued trust token.
type trustRec struct {
	name     string
	perm     api.Permission
	projects []string
	global   bool
	certExp  time.Time
	exp      time.Time
	used     bool
}

type trustClaims struct {
	V    int    `json:"v"`
	ID   string `json:"id"`
	Name string `json:"name"`
	FP   string `json:"fp"`
	Exp  int64  `json:"exp"`
	Sec  string `json:"sec"`
}

const trustMaxFailures = 5

func (s *Server) trustMAC(payload string) []byte {
	m := hmac.New(sha256.New, s.trustKey)
	m.Write([]byte("jailops-trust:"))
	m.Write([]byte(payload))
	return m.Sum(nil)
}

func validPermission(p api.Permission) bool {
	switch p {
	case api.PermReadonly, api.PermOperator, api.PermAdmin, api.PermBackupPush, api.PermBackupPull, api.PermRestore, api.PermBackupAdmin:
		return true
	}
	return false
}

func (s *Server) createTrustToken(w http.ResponseWriter, r *http.Request) {
	req, e := decode[api.TrustTokenRequest](r)
	if e != nil {
		writeErr(w, e)
		return
	}
	s.mu.Lock()
	rp := func() reply {
		if !validName(req.Name) {
			return reply{err: api.Errorf(api.CodeInvalid, "invalid name %q", req.Name)}
		}
		if !validPermission(req.Permission) {
			return reply{err: api.Errorf(api.CodeInvalid, "a trust token needs a permission")}
		}
		if e := validIdentityScope(req.Projects, req.Global); e != nil {
			return reply{err: e}
		}
		if e := s.checkProjectsExist(req.Projects); e != nil {
			return reply{err: e}
		}
		if req.TTLSeconds < 0 {
			return reply{err: api.Errorf(api.CodeInvalid, "ttl_seconds must not be negative")}
		}
		if s.certs.items[req.Name] != nil {
			return reply{err: api.Errorf(api.CodeAlreadyExists, "certificate %q already exists", req.Name)}
		}
		ttl := time.Duration(req.TTLSeconds) * time.Second
		if ttl == 0 {
			ttl = time.Hour
		}
		exp := s.now().Add(ttl).UTC().Truncate(time.Second)
		id := randHex(12)
		s.trustTokens[id] = &trustRec{name: req.Name, perm: req.Permission, projects: slices.Clone(req.Projects), global: req.Global,
			certExp: req.ExpiresAt, exp: exp}
		payload, _ := json.Marshal(trustClaims{V: 1, ID: id, Name: req.Name, FP: s.serverFP, Exp: exp.Unix(), Sec: randHex(16)})
		p := b64.EncodeToString(payload)
		tok := "joc_" + p + "." + b64.EncodeToString(s.trustMAC(p))
		s.emit("trust-token-created", "/api/v1/trust/tokens/"+req.Name)
		return reply{status: 201, meta: api.TrustToken{Name: req.Name, Token: tok, ExpiresAt: exp}}
	}()
	s.mu.Unlock()
	s.respond(w, rp)
}

func source(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

var errBadTrust = api.Errorf(api.CodeUnauthorized, "invalid trust token")

func (s *Server) redeemTrustToken(w http.ResponseWriter, r *http.Request) {
	req, e := decode[api.TrustClientRequest](r)
	if e != nil {
		writeErr(w, e)
		return
	}
	src := source(r)
	s.mu.Lock()
	rp := func() reply {
		if s.trustFails[src] >= trustMaxFailures {
			return reply{err: api.Errorf(api.CodeRateLimited, "too many failed attempts").WithHint("try again later")}
		}
		fail := func(e *api.Error) reply {
			s.trustFails[src]++
			return reply{err: e}
		}
		rest, ok := strings.CutPrefix(req.Token, "joc_")
		if !ok {
			return fail(errBadTrust)
		}
		p, sig, ok := strings.Cut(rest, ".")
		if !ok {
			return fail(errBadTrust)
		}
		got, err := b64.DecodeString(sig)
		if err != nil || !hmac.Equal(got, s.trustMAC(p)) {
			return fail(errBadTrust)
		}
		raw, err := b64.DecodeString(p)
		var c trustClaims
		if err != nil || json.Unmarshal(raw, &c) != nil || c.V != 1 {
			return fail(errBadTrust)
		}
		rec := s.trustTokens[c.ID]
		if rec == nil || rec.used || !s.now().Before(rec.exp) || c.FP != s.serverFP || c.Name != rec.name {
			return fail(errBadTrust)
		}
		blk, _ := pem.Decode([]byte(req.Certificate))
		if blk == nil || blk.Type != "CERTIFICATE" {
			return reply{err: api.Errorf(api.CodeInvalid, "certificate must be a PEM CERTIFICATE")}
		}
		cert, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			return reply{err: api.Errorf(api.CodeInvalid, "invalid certificate: %v", err)}
		}
		if s.certs.items[rec.name] != nil {
			return reply{err: api.Errorf(api.CodeAlreadyExists, "certificate %q already exists", rec.name)}
		}
		rec.used = true // consumed: it never works again, whatever follows
		delete(s.trustFails, src)
		obj := &api.Certificate{Metadata: api.ObjectMeta{Name: rec.name}, Spec: api.CertificateSpec{
			Fingerprint: fingerprintOf(cert.Raw), Permission: rec.perm, Kind: "client",
			Projects: slices.Clone(rec.projects), Global: rec.global, ExpiresAt: rec.certExp}}
		s.certs.insert(obj, false)
		return reply{status: 201, meta: clone(obj)}
	}()
	s.mu.Unlock()
	s.respond(w, rp)
}

// TrustCertificate stores a trusted certificate directly, for tests.
func (s *Server) TrustCertificate(c api.Certificate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c.Spec.Fingerprint = normFP(c.Spec.Fingerprint)
	if c.Spec.Kind == "" {
		c.Spec.Kind = "client"
	}
	s.certs.insert(&c, true)
}

// SeedSession stores a web session. Its ID is the reference clients use.
func (s *Server) SeedSession(sess api.Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sess.ID] = &sess
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	rc := s.info(r)
	s.mu.Lock()
	ids := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := []api.Session{}
	for _, id := range ids {
		if sess := s.sessions[id]; rc.id.Global || sess.User == rc.id.Name {
			out = append(out, *sess)
		}
	}
	s.mu.Unlock()
	writeSync(w, 200, out)
}

func (s *Server) revokeSession(w http.ResponseWriter, r *http.Request) {
	rc := s.info(r)
	id := r.PathValue("id")
	s.mu.Lock()
	sess := s.sessions[id]
	ok := sess != nil && (rc.id.Global || sess.User == rc.id.Name)
	if ok {
		delete(s.sessions, id)
		s.emit("session-revoked", "/api/v1/sessions/"+id)
	}
	s.mu.Unlock()
	if !ok {
		writeErr(w, notFound("session", id))
		return
	}
	writeSync(w, 200, struct{}{})
}

func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	rc := s.info(r)
	kind := rc.kind
	if kind == "anonymous" {
		kind = "unix"
	}
	writeSync(w, 200, api.Identity{User: rc.id.Name, Kind: kind, Permission: rc.id.Permission,
		Projects: slices.Clone(rc.id.Projects), Global: rc.id.Global})
}
