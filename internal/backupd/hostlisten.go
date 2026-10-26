package backupd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"sync"
	"time"

	"jailops.net/jailops/internal/auth"
	"jailops.net/jailops/internal/node"
	"jailops.net/jailops/internal/node/ctl"
	"jailops.net/jailops/sdk/api"
)

// hostResolver maps a certificate fingerprint to an enrolled, active host. It
// asks the database, with a short cache, so that `hosts revoke` run by another
// process takes effect within seconds.
type hostResolver struct {
	db  *ctl.DB
	ttl time.Duration
	now func() time.Time

	mu    sync.Mutex
	cache map[string]cachedHost
}

type cachedHost struct {
	host ctl.Host
	ok   bool
	at   time.Time
}

func newHostResolver(db *ctl.DB, ttl time.Duration) *hostResolver {
	return &hostResolver{db: db, ttl: ttl, now: time.Now, cache: map[string]cachedHost{}}
}

func (r *hostResolver) lookup(ctx context.Context, fp string) (ctl.Host, bool) {
	r.mu.Lock()
	if c, ok := r.cache[fp]; ok && r.now().Sub(c.at) < r.ttl {
		r.mu.Unlock()
		return c.host, c.ok
	}
	r.mu.Unlock()
	h, err := r.db.HostByFingerprint(ctx, fp)
	ok := err == nil && h.Active()
	if err != nil && !errors.Is(err, ctl.ErrNotFound) {
		return ctl.Host{}, false // a database error never admits anybody
	}
	r.mu.Lock()
	if len(r.cache) > 4096 {
		r.cache = map[string]cachedHost{}
	}
	r.cache[fp] = cachedHost{host: h, ok: ok, at: r.now()}
	r.mu.Unlock()
	return h, ok
}

// forget drops the cache (after an enrolment or a revocation made in this process).
func (r *hostResolver) forget() {
	r.mu.Lock()
	r.cache = map[string]cachedHost{}
	r.mu.Unlock()
}

// hostTLS is the server side of the host listener: TLS 1.3, a client certificate
// is required, its fingerprint must belong to an enrolled, non-revoked host.
func (r *hostResolver) hostTLS(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"h2", "http/1.1"},
		ClientAuth:   tls.RequireAnyClientCert,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("no client certificate")
			}
			leaf, err := x509.ParseCertificate(raw[0])
			if err != nil {
				return err
			}
			now := time.Now()
			if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
				return errors.New("client certificate is outside its validity period")
			}
			if _, ok := r.lookup(context.Background(), auth.Fingerprint(raw[0])); !ok {
				return errors.New("client certificate is not an enrolled host")
			}
			return nil
		},
	}
}

// hostHandler wraps the node's host handler: it resolves the host of the
// connection's certificate for every request (a revoked host stops at once on
// open connections too) and attaches it.
func (s *Server) hostHandler() http.Handler {
	inner := s.Node.Handler()
	mux := http.NewServeMux()
	mux.Handle(node.Base, s.hostAuth(inner))
	mux.Handle(node.Base+"/", s.hostAuth(inner))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, api.Errorf(api.CodeNotFound, "no such endpoint"))
	})
	return mux
}

func (s *Server) hostAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			writeError(w, api.Errorf(api.CodeUnauthorized, "a client certificate is required"))
			return
		}
		h, ok := s.hosts.lookup(r.Context(), auth.CertFingerprint(r.TLS.PeerCertificates[0]))
		if !ok {
			writeError(w, api.Errorf(api.CodeUnauthorized, "the host is not enrolled or was revoked"))
			return
		}
		ctx := node.WithHost(r.Context(), node.Host{ID: h.ID, Name: h.Name, Scopes: h.Scopes})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
