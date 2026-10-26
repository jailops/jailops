package backupd_test

import (
	"crypto/tls"
	"net"
	"testing"
	"time"

	"jailops.net/jailops/internal/auth"
	"jailops.net/jailops/internal/node/client"
	"jailops.net/jailops/internal/node/nodetest"
	"jailops.net/jailops/sdk/api"
)

func TestHostAuthentication(t *testing.T) {
	r := startRig(t, memKind, nil)
	h := r.enroll("h")
	g := r.Fix.Group("h", "j")
	if _, err := h.C.Info(ctx); err != nil {
		t.Fatal(err)
	}
	// a certificate nobody enrolled is refused in the handshake
	stranger, _, _, _ := auth.GenerateCertificate(auth.CertOptions{CommonName: "stranger"})
	if _, err := r.clientFor(stranger).Info(ctx); err == nil {
		t.Fatal("an unenrolled certificate was accepted")
	}
	// no certificate at all
	conn, err := tls.Dial("tcp", r.Srv.HostAddr(), &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13})
	if err == nil {
		// TLS 1.3: the handshake completes on the client side before the server verifies; the first read fails
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		_, werr := conn.Write([]byte("GET /1.0/repo HTTP/1.1\r\nHost: x\r\n\r\n"))
		buf := make([]byte, 10)
		_, rerr := conn.Read(buf)
		conn.Close()
		if werr == nil && rerr == nil {
			t.Fatal("a client without a certificate was served")
		}
	}
	// plain HTTP on the TLS port gets nothing
	if c, err := net.Dial("tcp", r.Srv.HostAddr()); err == nil {
		c.SetDeadline(time.Now().Add(time.Second))
		_, _ = c.Write([]byte("GET /1.0/repo HTTP/1.1\r\nHost: x\r\n\r\n"))
		buf := make([]byte, 20)
		n, _ := c.Read(buf)
		c.Close()
		if string(buf[:n]) == "HTTP/1.1 200 OK" {
			t.Fatal("plain HTTP was served")
		}
	}
	// a node whose certificate does not match the pin is refused by the client
	wrongFP := "00" + r.Srv.ServerFingerprint()[2:]
	bad, err := client.New(client.Config{Address: r.Srv.HostAddr(), Fingerprint: wrongFP, Cert: h.Cert, Attempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bad.Info(ctx); err == nil {
		t.Fatal("the client accepted a node with an unexpected certificate")
	}

	// revocation takes effect on the next request, even on a connection that is open
	_, err = h.C.Begin(ctx, g, nodetest.SnapshotID(r.Clock.Now(), 1), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.DB.RevokeHost(ctx, "h"); err != nil { // as the CLI does: another process, no cache invalidation
		t.Fatal(err)
	}
	deadline := time.Now().Add(6 * time.Second)
	for {
		_, err := h.C.Info(ctx)
		if err != nil {
			if code(err) != api.CodeUnauthorized {
				t.Fatalf("after the revocation: %v", err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a revoked host is still served after 6 seconds")
		}
		time.Sleep(200 * time.Millisecond)
	}
	// its open session was aborted: the group is free for someone else's lease
	sessions, _ := r.DB.OpenSessions(ctx)
	if len(sessions) != 0 {
		t.Fatalf("%d sessions of a revoked host are still open", len(sessions))
	}
	if _, err := r.Srv.Node.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
}
