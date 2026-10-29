package cli

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

const defaultPort = "39110"

func (a *app) trustCmd() *cobra.Command {
	d := certDesc()
	tc := group("trust", "Trust new clients and manage who is trusted",
		`Identities are certificates, tokens and users, each with a role and a project
scope (--projects a,b, or --global). An empty scope grants nothing.

Adding a client takes no trust on first use and no copying of certificates:

  1. A global admin runs 'jailops trust add --name laptop --permission operator --projects web'.
     The server answers with a single-use trust token that carries its own
     certificate fingerprint.
  2. On the client, 'jailops remote add HOST --token TOKEN' pins that fingerprint,
     makes a key pair and presents the token with the certificate.
  3. The server consumes the token and records the certificate with the role
     and projects.

'trust list', 'trust revoke' and 'trust edit' manage the store. A revocation
takes effect on the next request.`,
		`jailops trust add --name laptop --permission operator --projects web
jailops trust list
jailops trust revoke laptop`, "trusted")
	tc.AddCommand(a.trustAdd())
	addList(a, tc, d, "")
	addShow(a, tc, d)
	addEdit(a, tc, d)
	rev := &cobra.Command{
		Use:   "revoke NAME|FINGERPRINT...",
		Short: "Stop trusting a client certificate",
		Long: `Remove a certificate from the trust store, by name or by fingerprint. It takes
effect on the next request of that client, which is then refused. The command
asks for confirmation unless -y is given.`,
		Example:           "jailops trust revoke laptop\njailops trust revoke -y ab:cd:ef:...",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeKind(a, d),
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, id := range args {
				if ok, err := a.dry("revoke trust of "+id, nil); ok || err != nil {
					if err != nil {
						return err
					}
					continue
				}
				if err := a.confirm(fmt.Sprintf("Revoke trust of %q?", id)); err != nil {
					return err
				}
				c, err := a.connect(cmd.Context())
				if err != nil {
					return err
				}
				if err := c.Certificates().Delete(cmd.Context(), id); err != nil {
					return err
				}
				if err := a.finishOp(cmd.Context(), nil, "certificate", id, "revoked", nil); err != nil {
					return err
				}
			}
			return nil
		},
	}
	tc.AddCommand(rev)
	return tc
}

func (a *app) trustAdd() *cobra.Command {
	var name, role string
	var ttl, certExpires time.Duration
	var scope func() ([]string, bool, error)
	cmd := &cobra.Command{
		Use:   "add --name NAME --permission ROLE (--projects a,b | --global)",
		Short: "Create a single-use trust token for a new client",
		Long: `Create a trust token that lets one new client add itself to the trust store.
The token is printed once, on standard output, and a warning on standard error;
it cannot be shown again. It is single use, bound to NAME, and valid for --ttl
(default 1 hour). It carries the server's certificate fingerprint, so the
client pins the server without trusting it on first use.

The identity needs a scope: --projects a,b or --global (global admins only).
--cert-expires limits how long the trust itself lasts once the token is used.`,
		Example: `jailops trust add --name laptop --permission operator --projects web
jailops trust add --name ops --permission admin --global --ttl 15m
jailops trust add --name ci --permission operator --projects web,db --cert-expires 2160h`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if name == "" {
				return usagef("--name is required")
			}
			projects, global, err := scope()
			if err != nil {
				return err
			}
			req := api.TrustTokenRequest{Name: name, Permission: api.Permission(role), Projects: projects, Global: global, TTLSeconds: seconds(ttl)}
			if certExpires > 0 {
				req.ExpiresAt = a.env.Now().Add(certExpires).UTC()
			}
			if ok, err := a.dry("create a trust token for "+name, req); ok || err != nil {
				return err
			}
			c, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			tt, err := c.Trust().CreateToken(cmd.Context(), req)
			if err != nil {
				return err
			}
			fprintf(a.env.Stderr, "%s\n", "warning: this trust token is shown only once and works only once; hand it to the new client now.")
			fprintf(a.env.Stderr, "trust for %s (%s, %s) can be claimed until %s\n", name, role, scopeText(projects, global), tt.ExpiresAt.UTC().Format("2006-01-02 15:04:05 MST"))
			if a.format == fmtTable {
				fprintln(a.env.Stdout, tt.Token)
				return nil
			}
			return a.emit(tt, nil, nil, func() []string { return []string{tt.Name} })
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "name of the new client (required)")
	permFlag(cmd, &role, "readonly")
	cmd.Flags().StringVar(&role, "role", "readonly", "the former name of --permission")
	_ = cmd.Flags().MarkHidden("role")
	cmd.Flags().DurationVar(&ttl, "ttl", 0, "how long the token can be redeemed (default 1h)")
	cmd.Flags().DurationVar(&certExpires, "cert-expires", 0, "how long the client's trust lasts after redemption (default: no expiry)")
	scope = scopeFlags(cmd)
	a.completeFlag(cmd, "projects", listNames(func(ctx context.Context, c *sdk.Client) iterNames { return namesOf(c.Projects().List(ctx), projName) }))
	return cmd
}

// remote add with a trust token ---------------------------------------------

// normalizeAddress turns "host", "host:port" or a URL into an https URL.
func normalizeAddress(addr string) (string, error) {
	if !strings.Contains(addr, "://") {
		addr = "https://" + addr
	}
	u, err := url.Parse(addr)
	if err != nil || u.Host == "" || u.Scheme != "https" {
		return "", usagef("%q is not an https address", addr)
	}
	if u.Port() == "" {
		u.Host = net.JoinHostPort(u.Hostname(), defaultPort)
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func (a *app) certDir() string { return filepath.Join(filepath.Dir(a.userConfigPath()), "certs") }

// newClientCert makes a key pair for a remote and stores it, key mode 0600.
func (a *app) newClientCert(remoteName string) (certFile, keyFile string, pair tls.Certificate, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", pair, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		return "", "", pair, err
	}
	now := a.env.Now()
	tpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "jailops-client-" + remoteName},
		NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(5, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return "", "", pair, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", pair, err
	}
	dir := a.certDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", pair, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	certFile, keyFile = filepath.Join(dir, remoteName+".crt"), filepath.Join(dir, remoteName+".key")
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		return "", "", pair, err
	}
	if err := os.WriteFile(certFile, certPEM, 0o644); err != nil {
		return "", "", pair, err
	}
	pair, err = tls.X509KeyPair(certPEM, keyPEM)
	return certFile, keyFile, pair, err
}

// addByTrustToken redeems a trust token and saves the remote.
func (a *app) addByTrustToken(cmd *cobra.Command, name, address, token string, sw bool) error {
	url, err := normalizeAddress(address)
	if err != nil {
		return err
	}
	fp, err := sdk.TrustTokenFingerprint(token)
	if err != nil {
		return usagef("the trust token is malformed: %v", err)
	}
	if name == "" {
		name = strings.NewReplacer(".", "-", ":", "-").Replace(strings.Split(strings.TrimPrefix(url, "https://"), ":")[0])
	}
	user, _, err := a.loadAll()
	if err != nil {
		return err
	}
	if _, exists := user.Remotes[name]; exists {
		return usagef("remote %q already exists: remove it first or pick another --name", name)
	}
	if ok, err := a.dry("redeem a trust token at "+url+" and add remote "+name, map[string]string{"pinned": colonFP(fp)}); ok || err != nil {
		return err
	}
	certFile, keyFile, pair, err := a.newClientCert(name)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()
	res, err := sdk.RedeemTrustToken(ctx, url, token, pair, a.env.SDKOptions...)
	if err != nil {
		_ = os.Remove(certFile)
		_ = os.Remove(keyFile)
		return err
	}
	rc := RemoteConfig{URL: url, Fingerprint: res.ServerFingerprint, Cert: certFile, Key: keyFile}
	path := a.userConfigPath()
	cfg, err := loadConfig(path)
	if err != nil {
		return err
	}
	if cfg.Remotes == nil {
		cfg.Remotes = map[string]RemoteConfig{}
	}
	cfg.Remotes[name] = rc
	current := false
	if sw || cfg.Current == "" {
		cfg.Current, current = name, true
	}
	if err := saveConfig(path, cfg); err != nil {
		return err
	}
	c := res.Certificate
	msg := fmt.Sprintf("remote/%s added: trusted as %s (%s, %s), server pinned %s", name, c.Metadata.Name, c.Spec.Permission, scopeText(c.Spec.Projects, c.Spec.Global), colonFP(res.ServerFingerprint)[:23]+"...")
	if current {
		msg += "; now the current remote"
	}
	a.say("%s\n", msg)
	a.probeRemote(cmd.Context(), rc)
	return nil
}

// sessions and whoami ---------------------------------------------------------

func (a *app) sessionsCmd() *cobra.Command {
	sc := group("sessions", "List and revoke web sessions",
		"A session is a signed-in browser of the web interface. You see your own sessions; a global admin sees all. Revoking ends a session on its next request.",
		"jailops sessions list\njailops sessions revoke 3f9c...")
	sc.AddCommand(&cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List sessions",
		Long:    "List the sessions you may see, with when they started, were last used and expire. The one that made this request is marked.",
		Example: "jailops sessions list\njailops sessions list -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			items, err := collect(c.Sessions().List(cmd.Context()))
			if err != nil {
				return err
			}
			return a.emit(nonNil(items), nil, func() ([]string, [][]string) {
				var rows [][]string
				for _, s := range items {
					cur := ""
					if s.Current {
						cur = "*"
					}
					rows = append(rows, []string{cur, s.ID, s.User, dash(s.Remote), age(a.env.Now(), s.CreatedAt), age(a.env.Now(), s.LastSeen), s.ExpiresAt.UTC().Format("2006-01-02 15:04")})
				}
				return []string{"", "ID", "USER", "FROM", "AGE", "LAST SEEN", "EXPIRES"}, rows
			}, func() []string {
				var n []string
				for _, s := range items {
					n = append(n, s.ID)
				}
				return n
			})
		},
	})
	sc.AddCommand(&cobra.Command{
		Use:     "revoke ID...",
		Short:   "End sessions",
		Long:    "End one or more sessions. The browser behind a revoked session is signed out on its next request. The command asks for confirmation unless -y is given.",
		Example: "jailops sessions revoke 3f9c...\njailops sessions revoke -y a b",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, id := range args {
				if ok, err := a.dry("revoke session "+id, nil); ok || err != nil {
					if err != nil {
						return err
					}
					continue
				}
				if err := a.confirm(fmt.Sprintf("Revoke session %q?", id)); err != nil {
					return err
				}
				c, err := a.connect(cmd.Context())
				if err != nil {
					return err
				}
				if err := c.Sessions().Revoke(cmd.Context(), id); err != nil {
					return err
				}
				if err := a.finishOp(cmd.Context(), nil, "session", id, "revoked", nil); err != nil {
					return err
				}
			}
			return nil
		},
	})
	return sc
}

func (a *app) whoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show who the server thinks you are",
		Long: `Show the identity the server sees for this connection: its name, kind (user,
token, certificate, unix account or hub), role and scope, together with the
remote, host scope and project this client is using. An identity without a
scope can do nothing.`,
		Example: "jailops whoami\njailops --remote prod whoami -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			id, err := c.Whoami(cmd.Context())
			if err != nil {
				return err
			}
			if a.format != fmtTable {
				return a.emit(struct {
					api.Identity
					Remote  string `json:"remote"`
					Project string `json:"project"`
					Host    string `json:"host,omitempty"`
				}{id, keyOf(a.conn.Name), a.conn.CurrentProject(), a.conn.Host}, nil, nil, func() []string { return []string{id.User} })
			}
			w := a.env.Stdout
			kv := func(k string, v any) { fprintf(w, "%-12s %v\n", k+":", v) }
			kv("User", id.User)
			kv("Kind", id.Kind)
			kv("Role", id.Permission)
			kv("Scope", scopeText(id.Projects, id.Global))
			kv("Remote", keyOf(a.conn.Name)+" ("+a.conn.describe()+")")
			if a.conn.Host != "" {
				kv("Host", a.conn.Host)
			}
			kv("Project", a.conn.CurrentProject())
			return nil
		},
	}
}
