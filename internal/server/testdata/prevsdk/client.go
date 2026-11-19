package sdk

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"jailops.net/jailops/sdk/api"
)

const (
	// DefaultSocket is the unix socket used by Local.
	DefaultSocket = "/var/run/jailops/jailops.sock"
	// EnvSocket overrides DefaultSocket.
	EnvSocket = "JAILOPS_SOCKET"
	// Version is the SDK version, sent in the User-Agent.
	Version = "0.1.0"

	defaultMaxBody = 32 << 20
)

// Option configures a connection.
type Option func(*config)

type config struct {
	socket     string
	token      string
	pin        string
	cert       *tls.Certificate
	rootCAs    *x509.CertPool
	userAgent  string
	maxBody    int64
	retries    int
	backoff    time.Duration
	backoffMax time.Duration
	timeout    time.Duration
	project    string
}

// WithToken authenticates with an API token (Authorization: Bearer).
func WithToken(token string) Option { return func(c *config) { c.token = token } }

// WithPinnedFingerprint pins the server's leaf certificate by the hex SHA-256
// of its DER encoding (see Fingerprint). Colons and a "sha256:" prefix are
// accepted. With a pin and no WithRootCAs the pin replaces CA verification,
// which is how a host with a self-signed certificate is trusted.
func WithPinnedFingerprint(fp string) Option { return func(c *config) { c.pin = fp } }

// WithClientCert presents a client certificate (mTLS).
func WithClientCert(cert tls.Certificate) Option { return func(c *config) { c.cert = &cert } }

// WithRootCAs verifies the server certificate against pool. Combined with a
// pin, both checks must pass.
func WithRootCAs(pool *x509.CertPool) Option { return func(c *config) { c.rootCAs = pool } }

// WithProject scopes every project-level request to a project: the client
// appends ?project=<name> unless a request sets one itself.
func WithProject(name string) Option { return func(c *config) { c.project = name } }

// WithSocket sets the unix socket path used by Local.
func WithSocket(path string) Option { return func(c *config) { c.socket = path } }

// WithUserAgent appends a product token to the User-Agent.
func WithUserAgent(ua string) Option { return func(c *config) { c.userAgent = ua } }

// WithMaxResponseSize bounds the size of one API response body (default 32 MiB).
func WithMaxResponseSize(n int64) Option { return func(c *config) { c.maxBody = n } }

// WithRetries sets the number of retries for idempotent requests (default 3).
func WithRetries(n int) Option { return func(c *config) { c.retries = n } }

// WithBackoff sets the retry backoff base and cap (default 100ms, 2s).
func WithBackoff(base, max time.Duration) Option {
	return func(c *config) { c.backoff, c.backoffMax = base, max }
}

// WithTimeout sets the per-request timeout (default 60s). Event streams are
// not subject to it.
func WithTimeout(d time.Duration) Option { return func(c *config) { c.timeout = d } }

// Target says where to connect.
type Target struct {
	local bool
	url   string
	opts  []Option
}

// Local targets the daemon's unix socket: WithSocket, else $JAILOPS_SOCKET,
// else DefaultSocket.
func Local(opts ...Option) Target { return Target{local: true, opts: opts} }

// Remote targets a daemon over https. Plain http is accepted only for
// loopback addresses.
func Remote(rawURL string, opts ...Option) Target { return Target{url: rawURL, opts: opts} }

// Client talks to one daemon (or, after Host, to one host through a hub).
type Client struct {
	t    *transport
	host string
	// project overrides the connection's project when non-empty.
	project string
	child   bool
	info    atomic.Pointer[api.ServerInfo]
}

type transport struct {
	cfg     config
	base    string
	hc      *http.Client // requests
	sc      *http.Client // streams, no timeout
	tr      *http.Transport
	ctx     context.Context
	cancel  context.CancelFunc
	closeMu sync.Once

	streamOnce sync.Once
	streamHC   *http.Client
}

// Connect opens a client and fetches GET /api/v1 to learn the server's extensions.
func Connect(ctx context.Context, target Target, opts ...Option) (*Client, error) {
	cfg := config{
		maxBody: defaultMaxBody, retries: 3, backoff: 100 * time.Millisecond,
		backoffMax: 2 * time.Second, timeout: 60 * time.Second,
	}
	for _, o := range slices.Concat(target.opts, opts) {
		o(&cfg)
	}
	t := &transport{cfg: cfg}
	t.ctx, t.cancel = context.WithCancel(context.Background())
	if err := t.init(target); err != nil {
		t.cancel()
		return nil, err
	}
	c := &Client{t: t}
	if _, err := c.Info(ctx); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

func (t *transport) init(target Target) error {
	cfg := &t.cfg
	tr := &http.Transport{
		TLSHandshakeTimeout: 10 * time.Second,
		IdleConnTimeout:     90 * time.Second,
		MaxIdleConnsPerHost: 4,
	}
	if target.local {
		sock := cfg.socket
		if sock == "" {
			sock = os.Getenv(EnvSocket)
		}
		if sock == "" {
			sock = DefaultSocket
		}
		tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}
		t.base = "http://jailops.local"
	} else {
		u, err := url.Parse(target.url)
		if err != nil || u.Host == "" {
			return fmt.Errorf("jailops: invalid remote URL %q", target.url)
		}
		switch u.Scheme {
		case "https":
			tc, err := cfg.tlsConfig()
			if err != nil {
				return err
			}
			tr.TLSClientConfig = tc
			tr.ForceAttemptHTTP2 = true
		case "http":
			if !isLoopback(u.Hostname()) {
				return fmt.Errorf("jailops: refusing plain http to non-loopback host %q; use https", u.Host)
			}
		default:
			return fmt.Errorf("jailops: unsupported URL scheme %q", u.Scheme)
		}
		tr.Proxy = http.ProxyFromEnvironment
		t.base = strings.TrimRight(u.Scheme+"://"+u.Host+u.Path, "/")
	}
	t.tr = tr
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	t.hc = &http.Client{Transport: tr, CheckRedirect: noRedirect}
	t.sc = &http.Client{Transport: tr, CheckRedirect: noRedirect}
	return nil
}

func isLoopback(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

var errPinMismatch = errors.New("jailops: server certificate does not match the pinned fingerprint")

// Fingerprint returns the pin form of a certificate: hex SHA-256 of its DER.
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

func normalizeFP(s string) string {
	s = strings.ToLower(strings.TrimPrefix(strings.ToLower(s), "sha256:"))
	return strings.ReplaceAll(s, ":", "")
}

func (cfg *config) tlsConfig() (*tls.Config, error) {
	tc := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: cfg.rootCAs}
	if cfg.cert != nil {
		tc.Certificates = []tls.Certificate{*cfg.cert}
	}
	if cfg.pin == "" {
		return tc, nil
	}
	pin := normalizeFP(cfg.pin)
	if raw, err := hex.DecodeString(pin); err != nil || len(raw) != sha256.Size {
		return nil, fmt.Errorf("jailops: pinned fingerprint must be a hex SHA-256")
	}
	check := func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 || Fingerprint(cs.PeerCertificates[0]) != pin {
			return errPinMismatch
		}
		return nil
	}
	if cfg.rootCAs == nil {
		// crypto/tls offers no other way to trust a self-signed certificate
		// by pin. The skip is never set without VerifyConnection enforcing
		// the pin and the certificate's validity period, and it is not
		// reachable from the public API without a pin.
		tc.InsecureSkipVerify = true
		tc.VerifyConnection = func(cs tls.ConnectionState) error {
			if err := check(cs); err != nil {
				return err
			}
			now := time.Now()
			leaf := cs.PeerCertificates[0]
			if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
				return fmt.Errorf("jailops: pinned certificate is outside its validity period")
			}
			return nil
		}
	} else {
		tc.VerifyConnection = check
	}
	return tc, nil
}

// Host returns a client scoped to one host managed by this hub. Every request
// carries ?host=<uuid-or-name> and the hub forwards it. The scoped client has
// the same interface; call Info to load that host's extensions.
func (c *Client) Host(nameOrUUID string) *Client {
	return &Client{t: c.t, host: nameOrUUID, project: c.project, child: true}
}

// Project returns a client scoped to one project: every request carries
// ?project=<name>. It shares the connection; Close is a no-op on it.
func (c *Client) Project(name string) *Client {
	return &Client{t: c.t, host: c.host, project: name, child: true}
}

// ProjectName returns the project requests are scoped to, "" when none was
// chosen (the server then refuses project-scoped requests: there is no
// default project).
func (c *Client) ProjectName() string {
	if c.project != "" {
		return c.project
	}
	return c.t.cfg.project
}

// HostName returns the host this client is scoped to, "" for the daemon itself.
func (c *Client) HostName() string { return c.host }

// Close releases idle connections and cancels in-flight requests and event
// streams. Clients made by Host share the connection and Close is a no-op on
// them. Close is idempotent.
func (c *Client) Close() error {
	if c.child {
		return nil
	}
	c.t.closeMu.Do(func() {
		c.t.cancel()
		c.t.tr.CloseIdleConnections()
	})
	return nil
}

// Info fetches GET /api/v1 and caches the result for Has.
func (c *Client) Info(ctx context.Context) (api.ServerInfo, error) {
	info, _, err := get[api.ServerInfo](ctx, c, "/api/v1", nil)
	if err != nil {
		return info, err
	}
	c.info.Store(&info)
	return info, nil
}

// Has reports whether the server advertised the extension. It uses the
// information loaded by Connect or Info and is false before that.
func (c *Client) Has(extension string) bool {
	i := c.info.Load()
	return i != nil && slices.Contains(i.APIExtensions, extension)
}

// Require returns an error satisfying errors.Is(err, ErrUnsupported) when the
// server lacks the extension, loading Info first if needed.
func (c *Client) Require(ctx context.Context, extension string) error {
	if c.info.Load() == nil {
		if _, err := c.Info(ctx); err != nil {
			return err
		}
	}
	return c.need(extension)
}

// need fails fast when Info is known and lacks the extension. When Info is
// unknown the request is sent and the server answers.
func (c *Client) need(extension string) error {
	if i := c.info.Load(); i != nil && !slices.Contains(i.APIExtensions, extension) {
		return api.Errorf(api.CodeUnsupported, "server does not support %q", extension).
			WithHint("upgrade the server").WithDetail("extension", extension)
	}
	return nil
}
