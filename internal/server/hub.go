package server

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"jailops.net/jailops/internal/auth"
	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

// DefaultRemotePort is added to a remote address that has none.
const DefaultRemotePort = "8443"

type hubTransport struct {
	tr      *http.Transport
	created time.Time
}

// remoteHostPort turns a registry address ("host", "host:8443",
// "https://host:8443") into host:port.
func remoteHostPort(addr string) (string, error) {
	a := strings.TrimPrefix(strings.TrimPrefix(addr, "https://"), "http://")
	a = strings.TrimRight(a, "/")
	if a == "" || strings.ContainsAny(a, "/?# ") {
		return "", api.Errorf(api.CodeInvalid, "invalid remote address %q", addr)
	}
	u, err := url.Parse("//" + a)
	if err != nil || u.Hostname() == "" {
		return "", api.Errorf(api.CodeInvalid, "invalid remote address %q", addr)
	}
	if u.Port() == "" {
		if strings.Contains(u.Hostname(), ":") {
			return "[" + u.Hostname() + "]:" + DefaultRemotePort, nil
		}
		return u.Hostname() + ":" + DefaultRemotePort, nil
	}
	return a, nil
}

func (s *Server) transportFor(ctx context.Context, rem api.Remote) (http.RoundTripper, error) {
	if s.cfg.Remotes != nil {
		return s.cfg.Remotes(ctx, rem)
	}
	addr, err := remoteHostPort(rem.Spec.Address)
	if err != nil {
		return nil, err
	}
	key := addr + "|" + rem.Spec.Fingerprint
	s.hubMu.Lock()
	if ht, ok := s.hubTr[key]; ok && time.Since(ht.created) < 10*time.Minute {
		s.hubMu.Unlock()
		return ht.tr, nil
	}
	s.hubMu.Unlock()
	tc, err := auth.ClientConfigPinned(ctx, s.cfg.ClientCert, addr, rem.Spec.Fingerprint)
	if err != nil {
		return nil, api.Errorf(api.CodeBusy, "cannot reach remote %q: %v", rem.Metadata.Name, err)
	}
	tr := &http.Transport{TLSClientConfig: tc, TLSHandshakeTimeout: 10 * time.Second, IdleConnTimeout: 90 * time.Second,
		ResponseHeaderTimeout: 5 * time.Minute, MaxIdleConnsPerHost: 4}
	s.hubMu.Lock()
	if old, ok := s.hubTr[key]; ok {
		old.tr.CloseIdleConnections()
	}
	s.hubTr[key] = &hubTransport{tr: tr, created: time.Now()}
	s.hubMu.Unlock()
	return tr, nil
}

func (s *Server) dropTransport(rem api.Remote) {
	addr, err := remoteHostPort(rem.Spec.Address)
	if err != nil {
		return
	}
	s.hubMu.Lock()
	defer s.hubMu.Unlock()
	if ht, ok := s.hubTr[addr+"|"+rem.Spec.Fingerprint]; ok {
		ht.tr.CloseIdleConnections()
		delete(s.hubTr, addr+"|"+rem.Spec.Fingerprint)
	}
}

// forward handles ?host=. It reports true when it answered the request; false
// means the request is for this daemon and goes on locally (the parameter is removed).
func (s *Server) forward(c *rc, host string) bool {
	info := s.svc.Info("")
	q := c.r.URL.Query()
	q.Del("host")
	q.Del("access_token") // a credential for this daemon, not for the remote
	if host == info.HostUUID || host == info.Hostname {
		c.r.URL.RawQuery = q.Encode()
		return false
	}
	if !s.svc.Hub() {
		c.fail(api.Errorf(api.CodeUnsupported, "this daemon is not a hub").WithHint("?host= works only on a hub"))
		return true
	}
	rem, err := s.svc.ResolveRemote(host)
	if err != nil {
		c.fail(err)
		return true
	}
	ctx := c.r.Context()
	if !c.rt.long {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
	}
	tr, err := s.transportFor(ctx, rem)
	if err != nil {
		c.statusOverride = http.StatusBadGateway
		c.fail(err)
		return true
	}
	addr, err := remoteHostPort(rem.Spec.Address)
	if err != nil {
		c.fail(err)
		return true
	}
	target := &url.URL{Scheme: "https", Host: addr}
	claimHdr, err := s.signClaim(c, rem)
	if err != nil {
		c.fail(err)
		return true
	}
	proxy := &httputil.ReverseProxy{
		Transport:     tr,
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.RawQuery = q.Encode()
			pr.Out.Host = ""
			// The caller's credentials were checked here; the remote sees the hub's certificate.
			for _, h := range []string{"Authorization", "Cookie", "X-Csrf-Token", "Origin", "Referer"} {
				pr.Out.Header.Del(h)
			}
			pr.Out.Header.Set("X-Request-ID", c.rid)
			// Whatever claim the caller sent is dropped; the host gets the hub's own.
			pr.Out.Header.Del(auth.RequestorHeader)
			pr.Out.Header.Set(auth.RequestorHeader, claimHdr)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			s.dropTransport(rem)
			s.log.Warn("forwarding failed", "rid", c.rid, "remote", rem.Metadata.Name, "err", err)
			c.statusOverride = http.StatusBadGateway
			c.fail(api.Errorf(api.CodeBusy, "remote %q did not answer", rem.Metadata.Name).WithHint("the hub forwards over pinned mTLS; check the remote and its pin"))
		},
	}
	proxy.ServeHTTP(c.w, c.r.WithContext(ctx))
	return true
}

// claimLifetime is how long a forwarded requestor claim is valid; a claim is
// signed per request and bound to its method and path.
const claimLifetime = 30 * time.Second

// signClaim signs the requestor claim the hub sends with a forwarded request:
// who the caller is, their role and scope, for this host and this request.
func (s *Server) signClaim(c *rc, rem api.Remote) (string, error) {
	if s.cfg.ClientCert == nil {
		return "", api.Errorf(api.CodeInternal, "this hub has no certificate to sign requestor claims with")
	}
	if rem.Spec.HostUUID == "" {
		return "", api.Errorf(api.CodeBusy, "remote %s has not been probed yet: its identity is unknown, so a claim cannot be addressed to it", rem.Metadata.Name)
	}
	now := s.svc.Clock()
	cl := auth.RequestorClaim{Version: 1, Name: c.actor.Name, Kind: c.actor.Kind, Role: c.actor.Perm,
		Projects: c.actor.Projects, Global: c.actor.Global, IssuedAt: now.Unix(), Expires: now.Add(claimLifetime).Unix(),
		Audience: rem.Spec.HostUUID, Method: c.r.Method, Path: c.r.URL.Path}
	h, err := auth.SignRequestor(*s.cfg.ClientCert, cl)
	if err != nil {
		return "", api.Errorf(api.CodeInternal, "signing the requestor claim: %v", err)
	}
	return h, nil
}

// SDKProber probes remotes with the SDK client over pinned mTLS.
type SDKProber struct {
	// Cert is the hub's client certificate.
	Cert *tls.Certificate
	mu   sync.Mutex
}

// Probe implements service.RemoteProber.
func (p *SDKProber) Probe(ctx context.Context, r api.Remote) (api.ServerInfo, error) {
	c, err := p.Dial(ctx, r)
	if err != nil {
		return api.ServerInfo{}, err
	}
	defer c.Close()
	return c.Info(ctx)
}

// Dial opens an SDK client to a remote over pinned mTLS with the hub's
// certificate; the hub applies fleet projects through it.
func (p *SDKProber) Dial(ctx context.Context, r api.Remote) (*sdk.Client, error) {
	addr, err := remoteHostPort(r.Spec.Address)
	if err != nil {
		return nil, err
	}
	opts := []sdk.Option{sdk.WithPinnedFingerprint(r.Spec.Fingerprint), sdk.WithRetries(0), sdk.WithTimeout(15 * time.Second)}
	if p.Cert != nil {
		opts = append(opts, sdk.WithClientCert(*p.Cert))
	}
	return sdk.Connect(ctx, sdk.Remote("https://"+addr, opts...))
}
