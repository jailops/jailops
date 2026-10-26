package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"time"
)

// Fingerprint is the lowercase hex SHA-256 of a certificate's DER encoding.
func Fingerprint(der []byte) string {
	h := sha256.Sum256(der)
	return hex.EncodeToString(h[:])
}

// CertFingerprint fingerprints a parsed certificate.
func CertFingerprint(c *x509.Certificate) string { return Fingerprint(c.Raw) }

// NormalizeFingerprint accepts the usual spellings (upper case, colon
// separated, spaces) and returns the canonical form, or an error.
func NormalizeFingerprint(s string) (string, error) {
	n := strings.ToLower(strings.NewReplacer(":", "", " ", "", "-", "").Replace(s))
	if len(n) != 64 {
		return "", errors.New("fingerprint must be 64 hex characters (SHA-256)")
	}
	if _, err := hex.DecodeString(n); err != nil {
		return "", errors.New("fingerprint is not hexadecimal")
	}
	return n, nil
}

// Pins is a concurrency-safe set of trusted certificate fingerprints.
type Pins struct {
	mu sync.RWMutex
	m  map[string]struct{}
}

// NewPins builds a set from fingerprints in any accepted spelling.
func NewPins(fps ...string) (*Pins, error) {
	p := &Pins{m: map[string]struct{}{}}
	for _, f := range fps {
		if err := p.Add(f); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// Add trusts a fingerprint.
func (p *Pins) Add(fp string) error {
	n, err := NormalizeFingerprint(fp)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.m[n] = struct{}{}
	p.mu.Unlock()
	return nil
}

// Remove revokes a fingerprint.
func (p *Pins) Remove(fp string) {
	if n, err := NormalizeFingerprint(fp); err == nil {
		p.mu.Lock()
		delete(p.m, n)
		p.mu.Unlock()
	}
}

// Contains reports whether the canonical fingerprint is trusted.
func (p *Pins) Contains(fp string) bool {
	p.mu.RLock()
	_, ok := p.m[fp]
	p.mu.RUnlock()
	return ok
}

// Len returns the number of pins.
func (p *Pins) Len() int { p.mu.RLock(); defer p.mu.RUnlock(); return len(p.m) }

// CertOptions describe a certificate to generate.
type CertOptions struct {
	CommonName string
	// DNSNames and IPs become subject alternative names. The name
	// PinnedServerName is always added so pinned clients can verify.
	DNSNames []string
	IPs      []net.IP
	Validity time.Duration // default one year
	Now      time.Time     // default time.Now()
}

// PinnedServerName is the SAN every JailOps certificate carries. Pinned
// clients verify the server against the pinned certificate under this name,
// so renaming or readdressing a host does not invalidate a pin.
const PinnedServerName = "jailops.invalid"

// GenerateCertificate creates a self-signed ECDSA P-256 certificate usable
// for both server and client authentication, and returns it with its PEM
// encodings.
func GenerateCertificate(o CertOptions) (cert tls.Certificate, certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return cert, nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return cert, nil, nil, err
	}
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	if o.Validity == 0 {
		o.Validity = 365 * 24 * time.Hour
	}
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: o.CommonName, Organization: []string{"jailops"}},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(o.Validity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		DNSNames:              append([]string{PinnedServerName}, o.DNSNames...),
		IPAddresses:           o.IPs,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return cert, nil, nil, err
	}
	kb, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return cert, nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kb})
	cert, err = tls.X509KeyPair(certPEM, keyPEM)
	return cert, certPEM, keyPEM, err
}

// CheckPeerLeaf validates the dates and key usage of a pinned leaf. Pinning
// replaces chain building, so the checks chain building would do on the leaf
// are repeated here.
func CheckPeerLeaf(leaf *x509.Certificate, now time.Time, usage x509.ExtKeyUsage) error {
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return errors.New("peer certificate is outside its validity period")
	}
	if len(leaf.ExtKeyUsage) > 0 {
		for _, u := range leaf.ExtKeyUsage {
			if u == usage || u == x509.ExtKeyUsageAny {
				return nil
			}
		}
		return errors.New("peer certificate lacks the required key usage")
	}
	return nil
}

// ServerConfig builds the server side of pinned mTLS: TLS 1.3 only, a client
// certificate is required and its fingerprint must be in pins. The pin set is
// consulted per handshake, so adding or revoking a pin takes effect at once.
func ServerConfig(cert tls.Certificate, pins *Pins) *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAnyClientCert,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			return verifyPinned(raw, pins.Contains, x509.ExtKeyUsageClientAuth)
		},
	}
}

func verifyPinned(raw [][]byte, trusted func(string) bool, usage x509.ExtKeyUsage) error {
	if len(raw) == 0 {
		return errors.New("peer presented no certificate")
	}
	if !trusted(Fingerprint(raw[0])) {
		return errors.New("peer certificate fingerprint is not trusted")
	}
	leaf, err := x509.ParseCertificate(raw[0])
	if err != nil {
		return err
	}
	return CheckPeerLeaf(leaf, time.Now(), usage)
}

// ClientConfigForPeer builds the client side when the peer's full certificate
// is known: the peer must present exactly that certificate (verified by the
// standard library with the certificate as its only trust root, under
// PinnedServerName). client may be nil for server-only TLS.
func ClientConfigForPeer(client *tls.Certificate, peer *x509.Certificate) (*tls.Config, error) {
	if peer == nil {
		return nil, errors.New("auth: no peer certificate")
	}
	if err := peer.VerifyHostname(PinnedServerName); err != nil {
		return nil, fmt.Errorf("auth: peer certificate is not a JailOps certificate: %w", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(peer)
	cfg := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: PinnedServerName}
	if client != nil {
		cfg.Certificates = []tls.Certificate{*client}
	}
	return cfg, nil
}

// FetchPeerCertificate connects to addr and returns the certificate the peer
// presents without trusting it. No handshake with verification disabled is
// used: the handshake is made against an empty trust store, fails, and the
// unverified chain carried by the verification error is returned. The caller
// must compare its fingerprint with a pin before relying on it.
func FetchPeerCertificate(ctx context.Context, addr string) (*x509.Certificate, error) {
	d := tls.Dialer{Config: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: x509.NewCertPool(), ServerName: PinnedServerName}}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err == nil {
		conn.Close()
		return nil, errors.New("auth: peer unexpectedly trusted by an empty trust store")
	}
	var cve *tls.CertificateVerificationError
	if errors.As(err, &cve) && len(cve.UnverifiedCertificates) > 0 {
		return cve.UnverifiedCertificates[0], nil
	}
	return nil, fmt.Errorf("auth: fetch peer certificate: %w", err)
}

// ClientConfigPinned connects once to learn the peer's certificate, requires
// its fingerprint to equal fingerprint, and returns a config that trusts only
// that certificate. The returned config verifies the real connection, so a
// peer swapping certificates between the two connections is rejected.
func ClientConfigPinned(ctx context.Context, client *tls.Certificate, addr, fingerprint string) (*tls.Config, error) {
	want, err := NormalizeFingerprint(fingerprint)
	if err != nil {
		return nil, err
	}
	peer, err := FetchPeerCertificate(ctx, addr)
	if err != nil {
		return nil, err
	}
	if got := CertFingerprint(peer); got != want {
		return nil, fmt.Errorf("auth: peer %s presents fingerprint %s, expected %s", addr, got, want)
	}
	return ClientConfigForPeer(client, peer)
}

// ClusterConfigs returns the server and client configs for member to member
// traffic. All members share one cluster certificate (separate from each
// member's API certificate); a peer is accepted only if it presents that same
// certificate, whose fingerprint is carried in join tokens.
func ClusterConfigs(clusterCert tls.Certificate) (server, client *tls.Config, err error) {
	if len(clusterCert.Certificate) == 0 {
		return nil, nil, errors.New("auth: empty cluster certificate")
	}
	leaf, err := x509.ParseCertificate(clusterCert.Certificate[0])
	if err != nil {
		return nil, nil, err
	}
	pins, err := NewPins(CertFingerprint(leaf))
	if err != nil {
		return nil, nil, err
	}
	client, err = ClientConfigForPeer(&clusterCert, leaf)
	if err != nil {
		return nil, nil, err
	}
	return ServerConfig(clusterCert, pins), client, nil
}
