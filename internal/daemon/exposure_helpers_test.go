package daemon_test

import (
	"crypto/tls"
	"os"
	"testing"
	"time"

	"jailops.net/jailops/internal/auth"
)

func writeCert(t *testing.T, crt, key, name string) {
	t.Helper()
	_, certPEM, keyPEM, err := auth.GenerateCertificate(auth.CertOptions{CommonName: name, DNSNames: []string{name}, Validity: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	// Write the key first, as a renewal tool does; a reload that sees only one new file keeps the pair in use.
	if err := os.WriteFile(key, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(crt, certPEM, 0o644); err != nil {
		t.Fatal(err)
	}
}

// serverName returns the common name of the certificate the listener presents now.
func serverName(t *testing.T, addr string) string {
	t.Helper()
	c, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return c.ConnectionState().PeerCertificates[0].Subject.CommonName
}
