package sdk_test

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/fake"
)

func TestPinnedFingerprintTLS(t *testing.T) {
	srv := fake.New()
	ts := httptest.NewUnstartedServer(srv)
	ts.Config.ErrorLog = log.New(io.Discard, "", 0)
	ts.StartTLS()
	defer ts.Close()
	cert := ts.Certificate()
	fp := sdk.Fingerprint(cert)

	c, err := sdk.Connect(ctxT(t), sdk.Remote(ts.URL, sdk.WithPinnedFingerprint(fp)), sdk.WithProject("web"))
	if err != nil {
		t.Fatalf("pinned connect: %v", err)
	}
	c.Close()
	// Colon and prefix forms are accepted.
	colon := strings.ToUpper(fp[:2])
	for i := 2; i < len(fp); i += 2 {
		colon += ":" + strings.ToUpper(fp[i:i+2])
	}
	c, err = sdk.Connect(ctxT(t), sdk.Remote(ts.URL, sdk.WithPinnedFingerprint("sha256:"+colon)), sdk.WithProject("web"))
	if err != nil {
		t.Fatal(err)
	}
	c.Close()

	wrong := strings.Repeat("00", 32)
	if _, err = sdk.Connect(ctxT(t), sdk.Remote(ts.URL, sdk.WithPinnedFingerprint(wrong)), sdk.WithProject("web")); err == nil ||
		!strings.Contains(err.Error(), "pinned fingerprint") {
		t.Fatalf("wrong pin: %v", err)
	}
	// A pin mismatch is not retried: one TCP connection attempt chain only.
	if _, err = sdk.Connect(ctxT(t), sdk.Remote(ts.URL), sdk.WithProject("web")); err == nil {
		t.Fatal("self-signed certificate accepted without a pin")
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	c, err = sdk.Connect(ctxT(t), sdk.Remote(ts.URL, sdk.WithRootCAs(pool)), sdk.WithProject("web"))
	if err != nil {
		t.Fatalf("root CA connect: %v", err)
	}
	c.Close()
	// With a root pool the pin is checked in addition.
	if _, err = sdk.Connect(ctxT(t), sdk.Remote(ts.URL, sdk.WithRootCAs(pool), sdk.WithPinnedFingerprint(wrong)), sdk.WithProject("web")); err == nil {
		t.Fatal("pin ignored next to a root pool")
	}
}

func TestMutualTLS(t *testing.T) {
	srv := fake.New()
	ts := httptest.NewUnstartedServer(srv)
	ts.Config.ErrorLog = log.New(io.Discard, "", 0)
	ts.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert}
	ts.StartTLS()
	defer ts.Close()
	fp := sdk.Fingerprint(ts.Certificate())
	if _, err := sdk.Connect(ctxT(t), sdk.Remote(ts.URL, sdk.WithPinnedFingerprint(fp)), sdk.WithProject("web")); err == nil {
		t.Fatal("connected without a client certificate")
	}
	// Reuse the test server's key pair as the client identity.
	clientCert := ts.TLS.Certificates[0]
	c, err := sdk.Connect(ctxT(t), sdk.Remote(ts.URL, sdk.WithPinnedFingerprint(fp), sdk.WithClientCert(clientCert)), sdk.WithProject("web"))
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}

func TestLocalSocketAndEnv(t *testing.T) {
	srv := fake.New()
	sock := filepath.Join(t.TempDir(), "j.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	hs := &http.Server{Handler: srv}
	go hs.Serve(l)
	defer hs.Close()

	t.Setenv(sdk.EnvSocket, sock)
	c, err := sdk.Connect(ctxT(t), sdk.Local(), sdk.WithProject("web"))
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	t.Setenv(sdk.EnvSocket, filepath.Join(t.TempDir(), "missing.sock"))
	if _, err := sdk.Connect(ctxT(t), sdk.Local(sdk.WithRetries(0)), sdk.WithProject("web")); err == nil {
		t.Fatal("connected to a missing socket")
	}
	c, err = sdk.Connect(ctxT(t), sdk.Local(sdk.WithSocket(sock)), sdk.WithProject("web")) // option beats env
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	_ = os.Remove(sock)
}
