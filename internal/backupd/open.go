package backupd

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"jailops.net/jailops/internal/auth"
	"jailops.net/jailops/internal/node"
	"jailops.net/jailops/internal/node/ctl"
	"jailops.net/jailops/internal/repo/datastore"
	"jailops.net/jailops/internal/repo/s3"
)

// OpenBackend opens the configured datastore.
func (c *Config) OpenBackend() (datastore.Backend, error) {
	switch c.Datastore.Kind {
	case "directory":
		return datastore.NewDirBackend(c.Datastore.Directory)
	case "s3":
		s := c.Datastore.S3
		creds, err := ReadCredentialsFile(s.CredentialsFile)
		if err != nil {
			return nil, err
		}
		cfg := s3.Config{Endpoint: s.Endpoint, Region: s.Region, Bucket: s.Bucket, Prefix: s.Prefix, Credentials: creds, NoTLS: s.NoTLS, CAFile: s.CAFile}
		if s.PathStyle {
			cfg.Addressing = s3.AddressingPath
		}
		return s3.New(cfg)
	}
	return nil, fmt.Errorf("backupd: unknown datastore kind %q", c.Datastore.Kind)
}

// ReadCredentialsFile reads access_key=, secret_key= and session_token= lines
// from a file only its owner can read.
func ReadCredentialsFile(path string) (s3.Credentials, error) {
	var c s3.Credentials
	b, err := ReadSecretFile(path)
	if err != nil {
		return c, err
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return c, fmt.Errorf("backupd: %s: a line is not key=value", path)
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "access_key":
			c.AccessKey = v
		case "secret_key":
			c.SecretKey = v
		case "session_token":
			c.SessionToken = v
		default:
			return c, fmt.Errorf("backupd: %s: unknown key %q", path, strings.TrimSpace(k))
		}
	}
	if c.AccessKey == "" || c.SecretKey == "" {
		return c, fmt.Errorf("backupd: %s: access_key and secret_key are required", path)
	}
	return c, nil
}

// OpenDB connects to PostgreSQL and migrates the schema.
func (c *Config) OpenDB(ctx context.Context) (*ctl.DB, error) {
	dsn, err := c.DSN()
	if err != nil {
		return nil, err
	}
	db, err := ctl.Open(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// NodeConfig builds the node configuration for an opened backend and database.
func (c *Config) NodeConfig(b datastore.Backend, db *ctl.DB, adopt bool) node.Config {
	return node.Config{Backend: b, DB: db, ServerID: c.ServerID, Hostname: c.Hostname, Adopt: adopt, Lease: time.Duration(c.Lease), Grace: time.Duration(c.Grace),
		DeletionDelay: time.Duration(c.DeletionDelay), RetentionDefault: c.Retention.Policy(), RetentionTimezone: c.Retention.Timezone}
}

// SyncStaticHosts enrols the hosts of the configuration file that are not known yet.
func (c *Config) SyncStaticHosts(ctx context.Context, db *ctl.DB) error {
	for _, h := range c.Hosts {
		fp, err := auth.NormalizeFingerprint(h.Fingerprint)
		if err != nil {
			return err
		}
		if err := db.EnsureHost(ctx, h.Name, fp, h.Scopes, "from the configuration file"); err != nil {
			return fmt.Errorf("backupd: enrolling %s: %w", h.Name, err)
		}
	}
	return nil
}

// LoadServerCert loads the node's server certificate.
func (c *Config) LoadServerCert() (tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(c.TLS.CertFile, c.TLS.KeyFile)
	if err != nil {
		return cert, fmt.Errorf("backupd: server certificate: %w (run `jailops-backupd init`)", err)
	}
	return cert, nil
}

// EnsureServerCert creates a self-signed server certificate when neither file exists and
// returns its fingerprint. Hosts pin this fingerprint.
func (c *Config) EnsureServerCert() (fingerprint string, created bool, err error) {
	_, cerr := os.Stat(c.TLS.CertFile)
	_, kerr := os.Stat(c.TLS.KeyFile)
	switch {
	case cerr == nil && kerr == nil:
		cert, err := c.LoadServerCert()
		if err != nil {
			return "", false, err
		}
		return auth.Fingerprint(cert.Certificate[0]), false, nil
	case cerr == nil || kerr == nil:
		return "", false, errors.New("backupd: only one of the server certificate and key exists; remove it or restore the other")
	}
	host, _ := os.Hostname()
	if c.Hostname != "" {
		host = c.Hostname
	}
	opts := auth.CertOptions{CommonName: "jailops-backupd " + host, Validity: 10 * 365 * 24 * time.Hour}
	if host != "" {
		opts.DNSNames = []string{host}
	}
	for _, a := range localIPs() {
		opts.IPs = append(opts.IPs, a)
	}
	cert, certPEM, keyPEM, err := auth.GenerateCertificate(opts)
	if err != nil {
		return "", false, err
	}
	if err := os.MkdirAll(filepath.Dir(c.TLS.CertFile), 0o755); err != nil {
		return "", false, err
	}
	if err := os.MkdirAll(filepath.Dir(c.TLS.KeyFile), 0o755); err != nil {
		return "", false, err
	}
	if err := os.WriteFile(c.TLS.KeyFile, keyPEM, 0o600); err != nil {
		return "", false, err
	}
	if err := os.WriteFile(c.TLS.CertFile, certPEM, 0o644); err != nil {
		return "", false, err
	}
	return auth.Fingerprint(cert.Certificate[0]), true, nil
}

func localIPs() []net.IP {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []net.IP
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLinkLocalUnicast() {
			out = append(out, n.IP)
		}
	}
	return out
}

// FingerprintOfPEM returns the SHA-256 fingerprint of the first certificate in a PEM file's content.
func FingerprintOfPEM(pemData []byte) (string, error) {
	for {
		var blk *pem.Block
		blk, pemData = pem.Decode(pemData)
		if blk == nil {
			return "", errors.New("no certificate found")
		}
		if blk.Type == "CERTIFICATE" {
			c, err := x509.ParseCertificate(blk.Bytes)
			if err != nil {
				return "", err
			}
			return auth.CertFingerprint(c), nil
		}
	}
}
