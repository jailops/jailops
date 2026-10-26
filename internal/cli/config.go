package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"jailops.net/jailops/sdk"
)

// FileConfig is the client configuration file. Two files are read: the
// host-wide one (/usr/local/etc/jailops/client.yaml) and the user's
// (~/.config/jailops/config.yaml). A remote of the user's file replaces a
// remote of the same name in the host-wide file as a whole.
type FileConfig struct {
	Current string `yaml:"current,omitempty"`
	// Projects remembers the current project of each remote, by remote
	// name ("local" for the unix socket). 'jailops project switch' writes it.
	Projects map[string]string       `yaml:"projects,omitempty"`
	Remotes  map[string]RemoteConfig `yaml:"remotes,omitempty"`
}

// RemoteConfig is one named daemon.
type RemoteConfig struct {
	URL         string `yaml:"url,omitempty"`
	Socket      string `yaml:"socket,omitempty"`
	Token       string `yaml:"token,omitempty"`
	TokenFile   string `yaml:"token_file,omitempty"`
	Fingerprint string `yaml:"fingerprint,omitempty"`
	Cert        string `yaml:"cert,omitempty"`
	Key         string `yaml:"key,omitempty"`
	CA          string `yaml:"ca,omitempty"`
	// Host scopes every request to one host behind a hub.
	Host string `yaml:"host,omitempty"`
	// Project is the project used when the user has not switched.
	Project string `yaml:"project,omitempty"`
}

// Connection is the resolved way to reach a daemon.
type Connection struct {
	Name        string // remote name, "" or "local" for the unix socket
	URL         string
	Socket      string
	Token       string
	Fingerprint string
	Cert, Key   string
	CA          string
	Host        string
	// Project is the selected project, "" for the server's default.
	Project string
	// Sources says where each value came from, for 'jailops doctor'.
	Sources map[string]string
}

func (a *app) userConfigPath() string {
	if a.configPath != "" {
		return a.configPath
	}
	if p := a.env.Getenv("JAILOPS_CONFIG"); p != "" {
		return p
	}
	if x := a.env.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "jailops", "config.yaml")
	}
	return filepath.Join(a.env.HomeDir(), ".config", "jailops", "config.yaml")
}

func loadConfig(path string) (FileConfig, error) {
	var c FileConfig
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

func saveConfig(path string, c FileConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// loadAll reads both files; the second result says which file defines a remote.
func (a *app) loadAll() (user, system FileConfig, err error) {
	if system, err = loadConfig(a.env.SystemConfig); err != nil {
		return
	}
	user, err = loadConfig(a.userConfigPath())
	return
}

// resolveConnection applies, from strongest to weakest: flags (--remote,
// --host), environment (JAILOPS_*), the user's file, the host-wide file, and
// last the unix socket default.
func (a *app) resolveConnection() (*Connection, error) {
	user, system, err := a.loadAll()
	if err != nil {
		return nil, err
	}
	src := map[string]string{}
	name := a.remote
	if name != "" {
		src["remote"] = "flag --remote"
	} else if v := a.env.Getenv("JAILOPS_REMOTE"); v != "" {
		name, src["remote"] = v, "env JAILOPS_REMOTE"
	} else if user.Current != "" {
		name, src["remote"] = user.Current, "user config"
	} else if system.Current != "" {
		name, src["remote"] = system.Current, "system config"
	}
	c := &Connection{Sources: src}
	var rc RemoteConfig
	switch {
	case strings.Contains(name, "://"):
		rc.URL = name
		src["url"] = src["remote"]
		name = ""
	case name == "" || name == "local":
		name = ""
	default:
		if r, ok := user.Remotes[name]; ok {
			rc, src["entry"] = r, "user config"
		} else if r, ok := system.Remotes[name]; ok {
			rc, src["entry"] = r, "system config"
		} else {
			return nil, usagef("unknown remote %q", name)
		}
	}
	c.Name = name
	override := func(key, envName string, dst *string, from string) {
		if v := a.env.Getenv(envName); v != "" {
			*dst, src[key] = v, "env "+envName
		} else if *dst != "" {
			src[key] = from
		}
	}
	entry := src["entry"]
	if entry == "" {
		entry = src["url"]
	}
	c.URL, c.Socket, c.Token, c.Fingerprint = rc.URL, rc.Socket, rc.Token, rc.Fingerprint
	c.Cert, c.Key, c.CA, c.Host = rc.Cert, rc.Key, rc.CA, rc.Host
	if rc.TokenFile != "" && c.Token == "" {
		b, err := os.ReadFile(rc.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("token_file: %w", err)
		}
		c.Token = strings.TrimSpace(string(b))
	}
	if name == "" && c.URL == "" {
		if v := a.env.Getenv("JAILOPS_URL"); v != "" {
			c.URL, src["url"] = v, "env JAILOPS_URL"
		}
	}
	override("token", "JAILOPS_TOKEN", &c.Token, entry)
	override("fingerprint", "JAILOPS_FINGERPRINT", &c.Fingerprint, entry)
	override("cert", "JAILOPS_CERT", &c.Cert, entry)
	override("key", "JAILOPS_KEY", &c.Key, entry)
	override("ca", "JAILOPS_CA", &c.CA, entry)
	override("socket", "JAILOPS_SOCKET", &c.Socket, entry)
	override("host", "JAILOPS_HOST", &c.Host, entry)
	if a.host != "" {
		c.Host, src["host"] = a.host, "flag --host"
	}
	c.Project = rc.Project
	if v := user.Projects[keyOf(name)]; v != "" {
		c.Project, src["project"] = v, "user config"
	}
	if v := a.env.Getenv("JAILOPS_PROJECT"); v != "" {
		c.Project, src["project"] = v, "env JAILOPS_PROJECT"
	}
	if a.project != "" {
		c.Project, src["project"] = a.project, "flag --project"
	}
	if c.URL == "" && c.Socket == "" {
		c.Socket, src["socket"] = sdk.DefaultSocket, "default"
	}
	return c, nil
}

func keyOf(name string) string {
	if name == "" {
		return "local"
	}
	return name
}

// CurrentProject is the project requests go to. It is empty when none was chosen: there is no default project.
func (c *Connection) CurrentProject() string {
	if c == nil {
		return ""
	}
	return c.Project
}

func (c *Connection) describe() string {
	if c.URL != "" {
		return c.URL
	}
	return "unix:" + c.Socket
}

// connect opens the SDK client for the resolved connection.
func (a *app) connect(ctx context.Context) (*sdk.Client, error) {
	if a.cl != nil {
		return a.cl, nil
	}
	conn, err := a.resolveConnection()
	if err != nil {
		return nil, err
	}
	a.conn = conn
	opts, err := conn.sdkOptions()
	if err != nil {
		return nil, err
	}
	if conn.Project != "" {
		opts = append(opts, sdk.WithProject(conn.Project))
	}
	opts = append(opts, a.env.SDKOptions...)
	target := sdk.Local(sdk.WithSocket(conn.Socket))
	if conn.URL != "" {
		target = sdk.Remote(conn.URL)
	}
	cl, err := sdk.Connect(ctx, target, opts...)
	if err != nil {
		return nil, err
	}
	a.cl = cl
	if conn.Host != "" {
		h := cl.Host(conn.Host)
		if _, err := h.Info(ctx); err != nil {
			return nil, err
		}
		a.cl = h
		// The parent connection stays open until process exit; Close on the
		// scoped client is a no-op, so keep the parent for Execute to close.
		a.parent = cl
	}
	return a.cl, nil
}

func (c *Connection) sdkOptions() ([]sdk.Option, error) {
	var opts []sdk.Option
	if c.Token != "" {
		opts = append(opts, sdk.WithToken(c.Token))
	}
	if c.Fingerprint != "" {
		opts = append(opts, sdk.WithPinnedFingerprint(c.Fingerprint))
	}
	if (c.Cert == "") != (c.Key == "") {
		return nil, usagef("a client certificate needs both cert and key")
	}
	if c.Cert != "" {
		pair, err := tls.LoadX509KeyPair(c.Cert, c.Key)
		if err != nil {
			return nil, fmt.Errorf("client certificate: %w", err)
		}
		opts = append(opts, sdk.WithClientCert(pair))
	}
	if c.CA != "" {
		pem, err := os.ReadFile(c.CA)
		if err != nil {
			return nil, fmt.Errorf("ca: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, usagef("ca %s holds no certificate", c.CA)
		}
		opts = append(opts, sdk.WithRootCAs(pool))
	}
	return opts, nil
}

// CertProbe is what a server presented during the TLS handshake.
type CertProbe struct {
	Cert *x509.Certificate
	// Trusted is true when the system roots verify the certificate.
	Trusted bool
}

func probeCert(ctx context.Context, hostport string) (*CertProbe, error) {
	d := &net.Dialer{Timeout: 10 * time.Second}
	host, _, _ := net.SplitHostPort(hostport)
	conn, err := (&tls.Dialer{NetDialer: d, Config: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}}).DialContext(ctx, "tcp", hostport)
	if err == nil {
		defer conn.Close()
		return &CertProbe{Cert: conn.(*tls.Conn).ConnectionState().PeerCertificates[0], Trusted: true}, nil
	}
	// Verification failed. Read the certificate without trusting it, only to
	// show its fingerprint to the user, who then decides whether to pin it.
	// Nothing is sent over this connection.
	cfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}
	conn, err2 := (&tls.Dialer{NetDialer: d, Config: cfg}).DialContext(ctx, "tcp", hostport)
	if err2 != nil {
		return nil, err2
	}
	defer conn.Close()
	return &CertProbe{Cert: conn.(*tls.Conn).ConnectionState().PeerCertificates[0]}, nil
}

func colonFP(hexfp string) string {
	var parts []string
	for i := 0; i+2 <= len(hexfp); i += 2 {
		parts = append(parts, hexfp[i:i+2])
	}
	return strings.Join(parts, ":")
}

func hostPort(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "", usagef("invalid URL %q", rawURL)
	}
	if u.Port() == "" {
		return net.JoinHostPort(u.Hostname(), "443"), nil
	}
	return u.Host, nil
}

func sortedKeys[V any](m map[string]V) []string {
	k := make([]string, 0, len(m))
	for n := range m {
		k = append(k, n)
	}
	sort.Strings(k)
	return k
}
