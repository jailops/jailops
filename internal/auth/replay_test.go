package auth_test

import (
	"crypto/x509"
	"encoding/pem"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"jailops.net/jailops/internal/auth"
	"jailops.net/jailops/sdk/api"
)

var recordClaim = flag.Bool("record-claim", false, "write a new recorded hub claim (only for a release that changes the claim on purpose)")

// A claim that a hub of an earlier release signed is still accepted by a host of this one. The files in testdata/hubhost are
// the hub certificate and the header value of a recorded claim; the host checks them with the current code at the time the
// claim was valid. A change of the claim's members, its signing domain or its checks that breaks this breaks every hub and host
// that are not upgraded together.
func TestHubClaimOfAnEarlierReleaseIsAccepted(t *testing.T) {
	dir := filepath.Join("testdata", "hubhost")
	issued := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	if *recordClaim {
		cert, _ := hubCert(t)
		hdr, err := auth.SignRequestor(cert, auth.RequestorClaim{Name: "ops", Kind: "user", Role: api.PermAdmin, Projects: []string{"shop"},
			IssuedAt: issued.Unix(), Expires: issued.Add(30 * time.Second).Unix(), Audience: "host-a", Method: "GET", Path: "/api/v1/jails"})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		must := func(err error) {
			if err != nil {
				t.Fatal(err)
			}
		}
		must(os.WriteFile(filepath.Join(dir, "claim.header"), []byte(hdr+"\n"), 0o644))
		must(os.WriteFile(filepath.Join(dir, "hub.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0o644))
		return
	}
	hdr, err := os.ReadFile(filepath.Join(dir, "claim.header"))
	if err != nil {
		t.Fatal(err)
	}
	pemBytes, err := os.ReadFile(filepath.Join(dir, "hub.pem"))
	if err != nil {
		t.Fatal(err)
	}
	blk, _ := pem.Decode(pemBytes)
	if blk == nil {
		t.Fatal("hub.pem holds no certificate")
	}
	peer, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	c, err := auth.VerifyRequestor(peer, string(trimNL(hdr)), auth.RequestorExpect{Audience: "host-a", Method: "GET", Path: "/api/v1/jails", Now: issued.Add(10 * time.Second)})
	if err != nil {
		t.Fatalf("the recorded claim is refused: %v", err)
	}
	if c.Name != "ops" || c.Role != api.PermAdmin || len(c.Projects) != 1 || c.Projects[0] != "shop" {
		t.Errorf("the recorded claim reads as %+v", c)
	}
	if _, err := auth.VerifyRequestor(peer, string(trimNL(hdr)), auth.RequestorExpect{Audience: "host-b", Method: "GET", Path: "/api/v1/jails", Now: issued.Add(10 * time.Second)}); err == nil {
		t.Error("the claim is accepted by another audience")
	}
}

func trimNL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}
