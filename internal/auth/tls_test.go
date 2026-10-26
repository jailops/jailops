package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func mustCert(t *testing.T, o CertOptions) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	c, _, _, err := GenerateCertificate(o)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return c, leaf
}

// serve starts a TLS echo-ish server: it accepts, reads one byte, writes "ok".
func serve(t *testing.T, cfg *tls.Config) string {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 1)
				if _, err := io.ReadFull(c, buf); err != nil {
					return
				}
				c.Write([]byte("ok"))
			}()
		}
	}()
	return ln.Addr().String()
}

// roundTrip dials, sends a byte and expects "ok"; TLS 1.3 reports a rejected
// client certificate on the first read, so the read is part of the test.
func roundTrip(addr string, cfg *tls.Config) error {
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 3 * time.Second}, Config: cfg}
	c, err := d.DialContext(context.Background(), "tcp", addr)
	if err != nil {
		return err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Write([]byte("x")); err != nil {
		return err
	}
	buf := make([]byte, 2)
	_, err = io.ReadFull(c, buf)
	return err
}

func TestFingerprint(t *testing.T) {
	_, leaf := mustCert(t, CertOptions{CommonName: "a"})
	fp := CertFingerprint(leaf)
	if len(fp) != 64 || fp != strings.ToLower(fp) || fp != Fingerprint(leaf.Raw) {
		t.Fatal(fp)
	}
	colon := strings.ToUpper(fp[:2])
	for i := 2; i < 64; i += 2 {
		colon += ":" + strings.ToUpper(fp[i:i+2])
	}
	for _, in := range []string{fp, strings.ToUpper(fp), colon, " " + fp} {
		if n, err := NormalizeFingerprint(in); err != nil || n != fp {
			t.Errorf("%q: %v %s", in, err, n)
		}
	}
	for _, bad := range []string{"", "abc", strings.Repeat("g", 64), fp + "00"} {
		if _, err := NormalizeFingerprint(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestPins(t *testing.T) {
	_, a := mustCert(t, CertOptions{CommonName: "a"})
	p, err := NewPins(CertFingerprint(a))
	if err != nil || p.Len() != 1 || !p.Contains(CertFingerprint(a)) {
		t.Fatal(err)
	}
	p.Remove(strings.ToUpper(CertFingerprint(a)))
	if p.Contains(CertFingerprint(a)) || p.Len() != 0 {
		t.Fatal("remove")
	}
	if _, err := NewPins("zz"); err == nil {
		t.Fatal("bad pin")
	}
	p.Remove("junk") // ignored
}

func TestMutualTLSPinned(t *testing.T) {
	srvCert, srvLeaf := mustCert(t, CertOptions{CommonName: "srv"})
	cliCert, cliLeaf := mustCert(t, CertOptions{CommonName: "cli"})
	otherCert, _ := mustCert(t, CertOptions{CommonName: "intruder"})
	pins, _ := NewPins(CertFingerprint(cliLeaf))
	srvCfg := ServerConfig(srvCert, pins)
	if srvCfg.MinVersion != tls.VersionTLS13 || srvCfg.ClientAuth != tls.RequireAnyClientCert {
		t.Fatal("server config weak")
	}
	addr := serve(t, srvCfg)

	good, err := ClientConfigForPeer(&cliCert, srvLeaf)
	if err != nil {
		t.Fatal(err)
	}
	if good.MinVersion != tls.VersionTLS13 {
		t.Fatal("client min version")
	}
	if err := roundTrip(addr, good); err != nil {
		t.Fatalf("pinned client refused: %v", err)
	}

	// unpinned client certificate
	bad, _ := ClientConfigForPeer(&otherCert, srvLeaf)
	if err := roundTrip(addr, bad); err == nil {
		t.Fatal("unpinned client accepted")
	}
	// no client certificate
	none, _ := ClientConfigForPeer(nil, srvLeaf)
	if err := roundTrip(addr, none); err == nil {
		t.Fatal("anonymous client accepted")
	}
	// client pins a different server certificate: handshake must fail
	_, wrongSrv := mustCert(t, CertOptions{CommonName: "srv"})
	wrong, _ := ClientConfigForPeer(&cliCert, wrongSrv)
	if err := roundTrip(addr, wrong); err == nil {
		t.Fatal("client accepted an unpinned server")
	}
	// revocation takes effect for new connections
	pins.Remove(CertFingerprint(cliLeaf))
	if err := roundTrip(addr, good); err == nil {
		t.Fatal("revoked client accepted")
	}
	pins.Add(CertFingerprint(cliLeaf))
	if err := roundTrip(addr, good); err != nil {
		t.Fatalf("re-added pin refused: %v", err)
	}
}

func TestExpiredCertificatesRefused(t *testing.T) {
	now := time.Now()
	srvCert, srvLeaf := mustCert(t, CertOptions{CommonName: "srv"})
	// client cert expired yesterday but pinned
	oldCli, oldLeaf := mustCert(t, CertOptions{CommonName: "old", Now: now.AddDate(-2, 0, 0), Validity: 365 * 24 * time.Hour})
	pins, _ := NewPins(CertFingerprint(oldLeaf))
	addr := serve(t, ServerConfig(srvCert, pins))
	cfg, _ := ClientConfigForPeer(&oldCli, srvLeaf)
	if err := roundTrip(addr, cfg); err == nil {
		t.Fatal("expired pinned client certificate accepted")
	}
	// expired server certificate pinned by client
	oldSrv, oldSrvLeaf := mustCert(t, CertOptions{CommonName: "oldsrv", Now: now.AddDate(-2, 0, 0), Validity: 365 * 24 * time.Hour})
	cliCert, cliLeaf := mustCert(t, CertOptions{CommonName: "cli"})
	pins2, _ := NewPins(CertFingerprint(cliLeaf))
	addr2 := serve(t, ServerConfig(oldSrv, pins2))
	cfg2, _ := ClientConfigForPeer(&cliCert, oldSrvLeaf)
	if err := roundTrip(addr2, cfg2); err == nil {
		t.Fatal("expired server certificate accepted")
	}
}

func TestClientConfigPinnedLearnsAndVerifies(t *testing.T) {
	srvCert, srvLeaf := mustCert(t, CertOptions{CommonName: "srv"})
	cliCert, cliLeaf := mustCert(t, CertOptions{CommonName: "cli"})
	pins, _ := NewPins(CertFingerprint(cliLeaf))
	addr := serve(t, ServerConfig(srvCert, pins))
	ctx := context.Background()

	cfg, err := ClientConfigPinned(ctx, &cliCert, addr, strings.ToUpper(CertFingerprint(srvLeaf)))
	if err != nil {
		t.Fatalf("pin by fingerprint: %v", err)
	}
	if err := roundTrip(addr, cfg); err != nil {
		t.Fatalf("connection with learned pin: %v", err)
	}
	_, other := mustCert(t, CertOptions{CommonName: "x"})
	if _, err := ClientConfigPinned(ctx, &cliCert, addr, CertFingerprint(other)); err == nil || !strings.Contains(err.Error(), "expected") {
		t.Fatalf("wrong fingerprint accepted: %v", err)
	}
	if _, err := ClientConfigPinned(ctx, &cliCert, addr, "bad"); err == nil {
		t.Fatal("bad fingerprint accepted")
	}
	if _, err := ClientConfigPinned(ctx, &cliCert, "127.0.0.1:1", CertFingerprint(srvLeaf)); err == nil {
		t.Fatal("dead address accepted")
	}
	// a peer that swaps certificates after the pin was learned is rejected
	swapCert, _ := mustCert(t, CertOptions{CommonName: "mitm"})
	addr2 := serve(t, ServerConfig(swapCert, pins))
	if err := roundTrip(addr2, cfg); err == nil {
		t.Fatal("swapped server certificate accepted")
	}
}

func TestClientConfigForPeerRejectsForeignCert(t *testing.T) {
	if _, err := ClientConfigForPeer(nil, nil); err == nil {
		t.Fatal("nil peer")
	}
	// a certificate without the JailOps SAN cannot be pinned this way
	foreign := &x509.Certificate{}
	if _, err := ClientConfigForPeer(nil, foreign); err == nil {
		t.Fatal("foreign cert accepted")
	}
}

func TestClusterConfigs(t *testing.T) {
	cluster, _ := mustCert(t, CertOptions{CommonName: "cluster"})
	srv, cli, err := ClusterConfigs(cluster)
	if err != nil {
		t.Fatal(err)
	}
	addr := serve(t, srv)
	if err := roundTrip(addr, cli); err != nil {
		t.Fatalf("member with cluster cert refused: %v", err)
	}
	// a member API certificate is not the cluster certificate
	memberCert, memberLeaf := mustCert(t, CertOptions{CommonName: "member"})
	apiPins, _ := NewPins(CertFingerprint(memberLeaf))
	_ = apiPins
	asMember, _ := ClientConfigForPeer(&memberCert, mustLeaf(t, cluster))
	if err := roundTrip(addr, asMember); err == nil {
		t.Fatal("non-cluster certificate accepted on cluster listener")
	}
	if _, _, err := ClusterConfigs(tls.Certificate{}); err == nil {
		t.Fatal("empty cert")
	}
}

func mustLeaf(t *testing.T, c tls.Certificate) *x509.Certificate {
	l, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return l
}
