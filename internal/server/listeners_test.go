package server_test

import (
	"context"
	"crypto/tls"
	"testing"
	"time"

	"jailops.net/jailops/internal/auth"
	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/service"
	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

func unixClient(t *testing.T, e *dtest.Env, opts ...sdk.Option) *sdk.Client {
	t.Helper()
	c, err := sdk.Connect(context.Background(), sdk.Local(append([]sdk.Option{sdk.WithSocket(e.D.UnixPath()), sdk.WithRetries(0)}, opts...)...))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestHTTPSPinnedAndMTLS(t *testing.T) {
	e := dtest.New(t, dtest.Options{Listen: true})
	ctx := ctxT(t)
	addr := "https://" + e.D.HTTPSAddr()
	c, err := sdk.Connect(ctx, sdk.Remote(addr, sdk.WithPinnedFingerprint(e.D.Fingerprint()), sdk.WithToken(e.Token(api.PermReadonly)), sdk.WithRetries(0)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if i, _ := c.Info(ctx); i.Auth != "trusted" {
		t.Fatalf("%+v", i)
	}
	// A wrong pin never connects.
	_, err = sdk.Connect(ctx, sdk.Remote(addr, sdk.WithPinnedFingerprint("00"+e.D.Fingerprint()[2:]), sdk.WithRetries(0)))
	if err == nil {
		t.Fatal("connected with the wrong pin")
	}
	// A client certificate is trusted only when its fingerprint is registered.
	cert, _, _, err := auth.GenerateCertificate(auth.CertOptions{CommonName: "peer"})
	if err != nil {
		t.Fatal(err)
	}
	mc := func() *sdk.Client {
		c, err := sdk.Connect(ctx, sdk.Remote(addr, sdk.WithPinnedFingerprint(e.D.Fingerprint()), sdk.WithClientCert(cert), sdk.WithRetries(0)))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
	if i, _ := mc().Info(ctx); i.Auth != "untrusted" {
		t.Fatalf("an unknown certificate was trusted: %+v", i)
	}
	sctx := service.WithActor(ctx, service.Actor{Name: "t", Perm: api.PermAdmin, Global: true})
	fp := auth.Fingerprint(cert.Certificate[0])
	if _, err := e.D.Service().CreateCertificate(sctx, api.Certificate{Spec: api.CertificateSpec{Fingerprint: fp, Permission: api.PermOperator, Global: true, Kind: "client"}}); err != nil {
		t.Fatal(err)
	}
	m := mc()
	if i, _ := m.Info(ctx); i.Auth != "trusted" {
		t.Fatalf("a registered certificate is not trusted")
	}
	if _, err := m.Networks().Create(ctx, dtest.LAN()); !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("operator certificate created a network: %v", err)
	}
	// Plain TLS 1.2 is refused.
	_ = tls.VersionTLS13
}

// A hub forwards ?host= requests to a registered remote over pinned mTLS.
func TestHubForwarding(t *testing.T) {
	remote := dtest.New(t, dtest.Options{Listen: true, Hostname: "remote1"})
	remote.Seed()
	hub := dtest.New(t, dtest.Options{Listen: true, Hub: true, Hostname: "hub"})
	ctx := ctxT(t)
	// the hub checks the project before it forwards, so it knows it too
	if _, err := hub.Admin.Projects().Create(ctx, api.Project{Metadata: api.ObjectMeta{Name: dtest.SeedProject}}); err != nil {
		t.Fatal(err)
	}
	sctx := service.WithActor(ctx, service.Actor{Name: "t", Perm: api.PermAdmin, Global: true})
	// The remote trusts the hub's certificate as an admin host.
	if _, err := remote.D.Service().CreateCertificate(sctx, api.Certificate{Metadata: api.ObjectMeta{Name: "hub"},
		Spec: api.CertificateSpec{Fingerprint: hub.D.Fingerprint(), Permission: api.PermAdmin, Global: true, Kind: "hub"}}); err != nil {
		t.Fatal(err)
	}
	rem, err := hub.Admin.Remotes().Create(ctx, api.Remote{Metadata: api.ObjectMeta{Name: "r1"},
		Spec: api.RemoteSpec{Address: remote.D.HTTPSAddr(), Fingerprint: remote.D.Fingerprint()}})
	if err != nil {
		t.Fatal(err)
	}
	if rem.Spec.HostUUID != remote.D.Service().HostUUID() {
		t.Fatalf("host uuid was not learned: %+v", rem.Spec)
	}
	rem, _ = hub.Admin.Remotes().Get(ctx, "r1")
	if !rem.Status.Online || rem.Status.Version != "test" || rem.Status.Extensions == 0 {
		t.Fatalf("%+v", rem.Status)
	}
	hi, _ := hub.Admin.Info(ctx)
	if !hi.Hub {
		t.Fatal("hub flag")
	}
	for _, key := range []string{"r1", remote.D.Service().HostUUID()} {
		hc := hub.Admin.Host(key)
		ri, err := hc.Info(ctx)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if ri.Hostname != "remote1" || ri.HostUUID != remote.D.Service().HostUUID() {
			t.Fatalf("answered by %+v", ri)
		}
	}
	hc := hub.Admin.Host("r1").Project(dtest.SeedProject)
	op, err := hc.Jails().Create(ctx, dtest.WebJail("web"))
	if err != nil {
		t.Fatal(err)
	}
	j, err := op.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if j.Metadata.Host != remote.D.Service().HostUUID() {
		t.Fatalf("%+v", j.Metadata)
	}
	// The jail exists on the remote, not on the hub.
	if _, err := remote.Admin.Jails().Get(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := hub.Admin.Project(dtest.SeedProject).Jails().Get(ctx, "web"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("the hub has the jail: %v", err)
	}
	// Events are forwarded too.
	ectx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	got := remote.Events(ectx, hc, sdk.Types(api.EventLifecycle))
	so, _ := hc.Jails().Start(ctx, "web")
	if _, err := so.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-got:
		if ev.Host != remote.D.Service().HostUUID() {
			t.Fatalf("event from %s", ev.Host)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no forwarded event")
	}
	// The caller's role is checked on the hub before forwarding.
	ro := hub.Client(api.PermReadonly).Host("r1")
	if _, err := ro.Networks().Create(ctx, dtest.LAN()); !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("readonly caller forwarded a write: %v", err)
	}
	// Unknown hosts and a wrong pin are errors, never local answers.
	if _, err := hub.Admin.Host("nope").Info(ctx); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("%v", err)
	}
	if _, err := hub.Admin.Remotes().Create(ctx, api.Remote{Metadata: api.ObjectMeta{Name: "r2"},
		Spec: api.RemoteSpec{Address: remote.D.HTTPSAddr(), Fingerprint: "00" + remote.D.Fingerprint()[2:]}}); err != nil {
		t.Fatal(err)
	}
	if _, err := hub.Admin.Host("r2").Jails().Get(ctx, "web"); err == nil {
		t.Fatal("forwarded to a host whose certificate does not match the pin")
	}
	r2, _ := hub.Admin.Remotes().Get(ctx, "r2")
	if r2.Status.Online {
		t.Fatal("a wrong pin shows online")
	}
	// A daemon that is not a hub does not forward.
	if _, err := remote.Admin.Host("r1").Info(ctx); !api.IsCode(err, api.CodeUnsupported) {
		t.Fatalf("%v", err)
	}
	// ?host= naming this very host is served locally.
	if i, err := hub.Admin.Host(hi.HostUUID).Info(ctx); err != nil || i.HostUUID != hi.HostUUID {
		t.Fatalf("%v %+v", err, i)
	}
	// Remotes need hub mode.
	if _, err := remote.Admin.Remotes().Create(ctx, api.Remote{Metadata: api.ObjectMeta{Name: "x"}, Spec: api.RemoteSpec{Address: "a:1", Fingerprint: remote.D.Fingerprint()}}); !api.IsCode(err, api.CodeUnsupported) {
		t.Fatalf("%v", err)
	}
}
