package sdk_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
	"jailops.net/jailops/sdk/fake"
)

func mustJail(t *testing.T, c *sdk.Client, name string, spec api.JailSpec) api.Jail {
	t.Helper()
	op, err := c.Jails().Create(ctxT(t), sdk.NewJail(name, spec))
	if err != nil {
		t.Fatal(err)
	}
	j, err := op.Wait(ctxT(t))
	if err != nil {
		t.Fatal(err)
	}
	return j
}

// conflictingJails makes the first GetETag stale by changing the object
// behind the client's back, as a second writer would.
type conflictingJails struct {
	*sdk.JailClient
	srv   *fake.Server
	calls atomic.Int32
}

func (c *conflictingJails) GetETag(ctx context.Context, name string) (api.Jail, string, error) {
	j, e, err := c.JailClient.GetETag(ctx, name)
	if c.calls.Add(1) <= 2 {
		c.srv.Touch("web", name)
	}
	return j, e, err
}

func TestModifyRetriesOnConflict(t *testing.T) {
	srv, c := newEnv(t)
	seedBasics(srv)
	mustJail(t, c, "web", webSpec())
	w := &conflictingJails{JailClient: c.Jails(), srv: srv}
	applied := 0
	err := sdk.Modify(ctxT(t), w, "web", func(j *api.Jail) { applied++; j.Spec.Env = map[string]string{"A": "1"} })
	if err != nil {
		t.Fatal(err)
	}
	if applied != 3 {
		t.Fatalf("fn applied %d times, want 3", applied)
	}
	j, _ := srv.JailIn("web", "web")
	if j.Spec.Env["A"] != "1" {
		t.Fatal("change not stored")
	}
	// Retry budget is honoured.
	w2 := &conflictingJails{JailClient: c.Jails(), srv: srv}
	w2.calls.Store(-1000)
	err = sdk.Modify(ctxT(t), w2, "web", func(j *api.Jail) { j.Spec.Env["A"] = "2" }, sdk.MaxConflictRetries(1))
	if !api.IsCode(err, api.CodeConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
}

func TestStaleIfMatch(t *testing.T) {
	srv, c := newEnv(t)
	seedBasics(srv)
	j := mustJail(t, c, "web", webSpec())
	_, etag, _ := c.Jails().GetETag(ctxT(t), "web")
	srv.Touch("web", "web")
	_, err := c.Jails().Update(ctxT(t), "web", j, etag)
	var ae *api.Error
	if !errors.As(err, &ae) || ae.Code != api.CodeConflict || !errors.Is(err, &api.Error{Code: api.CodeConflict}) {
		t.Fatalf("got %v", err)
	}
	// Patch merges.
	got, err := c.Jails().Patch(ctxT(t), "web", map[string]any{"spec": map[string]any{"hostname": "h1"}}, "")
	if err != nil || got.Spec.Hostname != "h1" {
		t.Fatalf("patch: %v %v", got.Spec.Hostname, err)
	}
}

func TestOperationWaitDelayedAndFailure(t *testing.T) {
	srv, c := newEnv(t)
	seedBasics(srv)
	srv.SetOperationDelay(80 * time.Millisecond)
	start := time.Now()
	op, err := c.Jails().Create(ctxT(t), sdk.NewJail("web", webSpec()))
	if err != nil {
		t.Fatal(err)
	}
	if op.Info().Status.Final() {
		t.Fatal("operation should be pending")
	}
	if _, err := op.Wait(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 70*time.Millisecond {
		t.Fatal("did not wait")
	}
	srv.SetOperationDelay(0)
	srv.FailOperation("jails.start", api.Errorf(api.CodeBusy, "disk on fire"))
	op, _ = c.Jails().Start(ctxT(t), "web")
	_, err = op.Wait(ctxT(t))
	if !api.IsCode(err, api.CodeBusy) || !strings.Contains(err.Error(), "disk on fire") {
		t.Fatalf("got %v", err)
	}
	op, _ = c.Jails().Start(ctxT(t), "web")
	j, err := op.Wait(ctxT(t))
	if err != nil || j.Status.Power != api.PowerRunning || j.Status.JID == 0 {
		t.Fatalf("start: %+v %v", j.Status, err)
	}
	if _, err := c.Jails().Delete(ctxT(t), "web"); !api.IsCode(err, api.CodeBusy) {
		t.Fatalf("delete running: %v", err)
	}
}

func TestOperationCancel(t *testing.T) {
	srv, c := newEnv(t)
	seedBasics(srv)
	srv.SetOperationDelay(5 * time.Second)
	op, err := c.Jails().Create(ctxT(t), sdk.NewJail("web", webSpec()))
	if err != nil {
		t.Fatal(err)
	}
	if err := op.Cancel(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := op.Wait(ctxT(t)); !errors.Is(err, sdk.ErrCancelled) {
		t.Fatalf("got %v", err)
	}
	if _, ok := srv.JailIn("web", "web"); ok {
		t.Fatal("cancelled create left a jail")
	}
	n := 0
	for range c.Operations().List(ctxT(t)) {
		n++
	}
	if n != 1 {
		t.Fatalf("operations: %d", n)
	}
}

func TestWaitFallsBackToPolling(t *testing.T) {
	srv := fake.New()
	seedBasics(srv)
	var waits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/wait") {
			waits.Add(1)
			http.NotFound(w, r)
			return
		}
		srv.ServeHTTP(w, r)
	}))
	defer ts.Close()
	c, err := sdk.Connect(ctxT(t), sdk.Remote(ts.URL), sdk.WithProject("web"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	srv.SetOperationDelay(30 * time.Millisecond)
	j := mustJail(t, c, "web", webSpec())
	if j.Metadata.Name != "web" || waits.Load() == 0 {
		t.Fatalf("waits=%d", waits.Load())
	}
}

func TestEventsReconnectWithLastEventID(t *testing.T) {
	srv, c := newEnv(t)
	seedBasics(srv)
	ctx, cancel := context.WithCancel(ctxT(t))
	defer cancel()
	out := make(chan string, 16)
	go func() {
		for ev, err := range c.Events(ctx, sdk.Types(api.EventLifecycle)) {
			if err != nil {
				return
			}
			var le api.LifecycleEvent
			_ = jsonUnmarshal(ev.Metadata, &le)
			out <- le.Source
		}
	}()
	waitFor(t, "stream", func() bool { return srv.EventStreams() == 1 })
	mk := func(n string) {
		_, err := c.Firewall().AddressSets().Create(ctxT(t), api.AddressSet{Metadata: api.ObjectMeta{Name: n}})
		if err != nil {
			t.Fatal(err)
		}
	}
	mk("a")
	if got := <-out; got != "/1.0/firewall/address-sets/a" {
		t.Fatal(got)
	}
	srv.DisconnectEventStreams()
	mk("b") // may land while the client is reconnecting
	mk("c")
	for _, want := range []string{"b", "c"} {
		select {
		case got := <-out:
			if !strings.HasSuffix(got, "/"+want) {
				t.Fatalf("got %s want %s", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("event lost across reconnect")
		}
	}
	select {
	case extra := <-out:
		t.Fatalf("duplicate event %s", extra)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
}

func TestEventsFatalAndClose(t *testing.T) {
	srv, c := newEnv(t, fake.WithToken("secret"))
	_ = srv
	for _, err := range c.Events(ctxT(t)) {
		if !api.IsCode(err, api.CodeUnauthorized) {
			t.Fatalf("got %v", err)
		}
		break
	}
	_, c2 := newEnv(t)
	done := make(chan error, 1)
	go func() {
		var last error
		for _, err := range c2.Events(context.Background()) {
			last = err
		}
		done <- last
	}()
	time.Sleep(50 * time.Millisecond)
	c2.Close()
	select {
	case err := <-done:
		if !errors.Is(err, sdk.ErrClosed) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not end the stream")
	}
	if _, err := c2.Info(ctxT(t)); !errors.Is(err, sdk.ErrClosed) {
		t.Fatalf("after close: %v", err)
	}
}

func TestErrorMappingAndRetries(t *testing.T) {
	srv, c := newEnv(t)
	seedBasics(srv)
	// Non-JSON statuses map to codes.
	for status, code := range map[int]api.Code{
		400: api.CodeInvalid, 401: api.CodeUnauthorized, 403: api.CodeForbidden, 404: api.CodeNotFound,
		409: api.CodeAlreadyExists, 412: api.CodeConflict, 501: api.CodeUnsupported, 500: api.CodeInternal,
	} {
		srv.ClearFailures()
		srv.FailStatus("POST", "/1.0/networks", status, -1)
		_, err := c.Networks().Create(ctxT(t), api.Network{Metadata: api.ObjectMeta{Name: "x"}})
		if !api.IsCode(err, code) {
			t.Errorf("status %d: got %v want %s", status, err, code)
		}
	}
	if err := func() error { _, err := c.Networks().Create(ctxT(t), api.Network{}); return err }(); err == nil {
		t.Fatal("expected error")
	}
	srv.ClearFailures()
	// Idempotent GET is retried through 503.
	srv.FailStatus("GET", "/1.0/jails", 503, 2)
	n := 0
	for _, err := range c.Jails().List(ctxT(t)) {
		if err != nil {
			t.Fatal(err)
		}
		n++
	}
	count := func(method string) (k int) {
		for _, r := range srv.Requests() {
			if r.Method == method && r.Path == "/1.0/jails" {
				k++
			}
		}
		return
	}
	if count("GET") != 3 {
		t.Fatalf("GET attempts: %d", count("GET"))
	}
	// POST is never retried.
	srv.FailStatus("POST", "/1.0/jails", 503, 1)
	_, err := c.Jails().Create(ctxT(t), sdk.NewJail("web", webSpec()))
	if !api.IsCode(err, api.CodeBusy) || count("POST") != 1 {
		t.Fatalf("err=%v posts=%d", err, count("POST"))
	}
	// Structured server errors keep code, hint and add the request id.
	srv.FailRequests("GET", "/1.0/jails/web", &api.Error{Code: api.CodeNoQuorum, Message: "no quorum", Hint: "wait"}, -1)
	_, err = c.Jails().Get(ctxT(t), "web")
	var ae *api.Error
	if !errors.As(err, &ae) || ae.Hint != "wait" || ae.Detail["request_id"] == "" {
		t.Fatalf("got %#v", err)
	}
}

func TestHeadersAndLimits(t *testing.T) {
	srv, c := newEnv(t)
	seedBasics(srv)
	_ = c.Jails().Get
	c.Jails().List(ctxT(t))(func(api.Jail, error) bool { return true })
	var seen []fake.RecordedRequest = srv.Requests()
	last := seen[len(seen)-1]
	if !strings.HasPrefix(last.Header.Get("User-Agent"), "jailops-sdk/") || len(last.Header.Get("X-Request-ID")) != 16 {
		t.Fatalf("headers: %v", last.Header)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	small, err := sdk.Connect(ctxT(t), sdk.Remote(ts.URL), sdk.WithMaxResponseSize(64), sdk.WithProject("web"))
	if err == nil {
		small.Close()
		t.Fatal("expected size limit error")
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Fatal(err)
	}
}

func TestTokenAuth(t *testing.T) {
	srv := fake.New(fake.WithToken("tok"))
	seedBasics(srv)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	anon, err := sdk.Connect(ctxT(t), sdk.Remote(ts.URL), sdk.WithProject("web"))
	if err != nil {
		t.Fatal(err)
	}
	defer anon.Close()
	for _, err := range anon.Jails().List(ctxT(t)) {
		if !api.IsCode(err, api.CodeUnauthorized) {
			t.Fatalf("got %v", err)
		}
	}
	c, _ := sdk.Connect(ctxT(t), sdk.Remote(ts.URL, sdk.WithToken("tok")), sdk.WithProject("web"))
	defer c.Close()
	for _, err := range c.Jails().List(ctxT(t)) {
		t.Fatal(err)
	}
}

func TestHubHostScoping(t *testing.T) {
	a, b := fake.New(fake.WithHostName("host-a")), fake.New(fake.WithHostName("host-b"))
	seedBasics(a)
	seedBasics(b)
	hub, c := newEnv(t, fake.WithHosts(map[string]*fake.Server{"host-a": a, "host-b": b}))
	_ = hub
	h := c.Host("host-b")
	mustJail(t, h, "web", webSpec())
	if _, ok := b.JailIn("web", "web"); !ok {
		t.Fatal("jail not on host-b")
	}
	if _, ok := a.JailIn("web", "web"); ok {
		t.Fatal("jail leaked to host-a")
	}
	info, err := h.Info(ctxT(t))
	if err != nil || info.Hostname != "host-b" || !h.Has("jails") || h.HostName() != "host-b" {
		t.Fatalf("%+v %v", info, err)
	}
	if _, err := c.Host("nope").Jails().Get(ctxT(t), "web"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatal(err)
	}
	h.Close() // no-op on a scoped client
	if _, err := c.Info(ctxT(t)); err != nil {
		t.Fatal(err)
	}
}

func TestExtensionsAndStubs(t *testing.T) {
	srv, c := newEnv(t, fake.WithExtensions("operations", "jails"))
	seedBasics(srv)
	mustJail(t, c, "web", webSpec())
	before := len(srv.Requests())
	_, err := c.Jails().Snapshots("web").Create(ctxT(t), "s1", "")
	if !errors.Is(err, sdk.ErrUnsupported) || !sdk.IsUnsupported(err) {
		t.Fatalf("got %v", err)
	}
	if len(srv.Requests()) != before {
		t.Fatal("unsupported call reached the server")
	}
	if err := c.Require(ctxT(t), "events"); !errors.Is(err, sdk.ErrUnsupported) {
		t.Fatal(err)
	}
	if err := c.Require(ctxT(t), "jails"); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotRollbackUpgrade(t *testing.T) {
	srv, c := newEnv(t)
	seedBasics(srv)
	mustJail(t, c, "web", webSpec())
	ctx := ctxT(t)
	sop, err := c.Jails().Snapshots("web").Create(ctx, "before", "pre-change")
	if err != nil {
		t.Fatal(err)
	}
	sn, err := sop.Wait(ctx)
	if err != nil || sn.Spec.Comment != "pre-change" {
		t.Fatalf("%+v %v", sn, err)
	}
	for sn, err := range c.Jails().Snapshots("web").List(ctx) {
		if err != nil || sn.Metadata.Name != "before" {
			t.Fatal(sn, err)
		}
	}
	// Root rollback without a previous generation fails; upgrade makes one.
	if _, err := c.Jails().Rollback(ctx, "web", api.JailRollback{}); !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("got %v", err)
	}
	srv.SeedGeneration("freebsd-15")
	uop, _ := c.Jails().Upgrade(ctx, "web", api.JailUpgrade{})
	j, err := uop.Wait(ctx)
	if err != nil || j.Status.Generation != "g2" {
		t.Fatalf("%+v %v", j.Status, err)
	}
	rop, err := c.Jails().Rollback(ctx, "web", api.JailRollback{Scope: api.RollbackRoot})
	if err != nil {
		t.Fatal(err)
	}
	if j, err = rop.Wait(ctx); err != nil || j.Status.Generation != "g1" {
		t.Fatalf("%+v %v", j.Status, err)
	}
	// Data loss must be confirmed.
	_, err = c.Jails().Rollback(ctx, "web", api.JailRollback{Scope: api.RollbackAll, Snapshot: "before"})
	if !api.IsCode(err, api.CodeDataLoss) {
		t.Fatalf("got %v", err)
	}
	rop, err = c.Jails().Rollback(ctx, "web", api.JailRollback{Scope: api.RollbackData, Snapshot: "before", ConfirmDataLoss: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = rop.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	dop, _ := c.Jails().Snapshots("web").Delete(ctx, "before")
	if _, err := dop.Wait(ctx); err != nil || len(srv.Snapshots("web", "web")) != 0 {
		t.Fatal(err)
	}
}

func TestIPAMAndNetworks(t *testing.T) {
	srv, c := newEnv(t)
	seedBasics(srv)
	ctx := ctxT(t)
	for _, n := range []string{"a", "b", "c"} {
		mustJail(t, c, n, webSpec())
	}
	op, _ := c.Jails().Create(ctx, sdk.NewJail("d", webSpec()))
	if _, err := op.Wait(ctx); !api.IsCode(err, api.CodePoolExhausted) {
		t.Fatalf("got %v", err)
	}
	var addrs []string
	for l, err := range c.Networks().Leases(ctx, "lan") {
		if err != nil {
			t.Fatal(err)
		}
		addrs = append(addrs, l.Address)
	}
	if strings.Join(addrs, ",") != "10.20.0.10,10.20.0.11,10.20.0.12" {
		t.Fatal(addrs)
	}
	// Freed addresses are reused; a fixed address in use conflicts.
	del, _ := c.Jails().Delete(ctx, "b")
	if _, err := del.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	j := mustJail(t, c, "e", webSpec())
	if j.Spec.Network[0].IPv4 != "10.20.0.11" {
		t.Fatal(j.Spec.Network[0].IPv4)
	}
	spec := webSpec()
	spec.Network[0].IPv4 = "10.20.0.10"
	op, _ = c.Jails().Create(ctx, sdk.NewJail("f", spec))
	if _, err := op.Wait(ctx); !api.IsCode(err, api.CodeIPConflict) {
		t.Fatalf("got %v", err)
	}
	if err := c.Networks().Delete(ctx, "lan"); !api.IsCode(err, api.CodeBusy) {
		t.Fatalf("got %v", err)
	}
	n, _ := c.Networks().Get(ctx, "lan")
	if n.Status.Allocated != 3 || n.Status.Free != 0 {
		t.Fatalf("%+v", n.Status)
	}
}

func TestImagesBackupsAndAccess(t *testing.T) {
	srv, c := newEnv(t)
	seedBasics(srv)
	ctx := ctxT(t)
	op, err := c.Images().Build(ctx, "freebsd-15", api.ImageBuild{FromGeneration: 1})
	if err != nil {
		t.Fatal(err)
	}
	g, err := op.Wait(ctx)
	if err != nil || g.Number != 2 || g.Parent != 1 {
		t.Fatalf("%+v %v", g, err)
	}
	n := 0
	for _, err := range c.Images().Generations(ctx, "freebsd-15") {
		if err != nil {
			t.Fatal(err)
		}
		n++
	}
	if n != 2 {
		t.Fatal(n)
	}
	img, _ := c.Images().Get(ctx, "freebsd-15")
	if img.Status.Aliases["stable"] != 2 {
		t.Fatalf("%+v", img.Status)
	}
	mustJail(t, c, "web", webSpec())
	if err := c.Images().Delete(ctx, "freebsd-15"); !api.IsCode(err, api.CodeImageInUse) {
		t.Fatal(err)
	}

	// Backup restore with preflight.
	spec := webSpec()
	spec.Network[0].Network = "old-net"
	srv.SeedBackup(api.Backup{
		Metadata: api.ObjectMeta{Name: "web-1"},
		Spec:     api.BackupSpec{Jail: "web", Snapshot: "s", Bundle: api.RestoreBundle{Spec: spec}},
	})
	pf, err := c.Backups().Preflight(ctx, api.RestoreRequest{Backup: "web-1", As: "web2"})
	if err != nil || pf.OK || len(pf.Missing) != 1 {
		t.Fatalf("%+v %v", pf, err)
	}
	if _, err := c.Backups().Restore(ctx, api.RestoreRequest{Backup: "web-1", As: "web2"}); !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("restore without OK preflight: %v", err)
	}
	req := api.RestoreRequest{Backup: "web-1", As: "web2", NetworkMap: map[string]string{"old-net": "lan"}}
	if pf, _ = c.Backups().Preflight(ctx, req); !pf.OK {
		t.Fatalf("%+v", pf)
	}
	rop, err := c.Backups().Restore(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	j, err := rop.Wait(ctx)
	if err != nil || j.Metadata.Name != "web2" || j.Spec.Network[0].IPv4 == "" || j.Spec.Network[0].IPv4 == "auto" {
		t.Fatalf("%+v %v", j, err)
	}

	// Secrets are shown once.
	tok, err := c.Tokens().Create(ctx, api.Token{Metadata: api.ObjectMeta{Name: "ci"}, Spec: api.TokenSpec{Permission: api.PermOperator, Global: true}})
	if err != nil || !strings.HasPrefix(tok.Secret, "jot_") {
		t.Fatalf("%+v %v", tok, err)
	}
	again, _ := c.Tokens().Get(ctx, "ci")
	if again.Secret != "" {
		t.Fatal("secret returned twice")
	}
	uc, err := c.Users().Create(ctx, api.UserCreate{Metadata: api.ObjectMeta{Name: "ann"}, Spec: api.UserSpec{Permission: api.PermAdmin, Global: true}})
	if err != nil || uc.Password == "" || uc.User.Metadata.Name != "ann" {
		t.Fatalf("%+v %v", uc, err)
	}
	if _, err := c.Certificates().Create(ctx, api.Certificate{Metadata: api.ObjectMeta{Name: "h"}, Spec: api.CertificateSpec{Fingerprint: "ab", Kind: "host", Global: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Backups().Targets().Create(ctx, api.BackupTarget{Metadata: api.ObjectMeta{Name: "t"}, Spec: api.BackupTargetSpec{Kind: "bogus"}}); !api.IsCode(err, api.CodeInvalid) {
		t.Fatal(err)
	}
	if _, err := c.Firewall().Policies().Create(ctx, api.FirewallPolicy{Metadata: api.ObjectMeta{Name: "web"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Remotes().Create(ctx, api.Remote{Metadata: api.ObjectMeta{Name: "r"}}); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteURLRules(t *testing.T) {
	if _, err := sdk.Connect(ctxT(t), sdk.Remote("http://example.com"), sdk.WithProject("web")); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("plain http to a remote host must be refused: %v", err)
	}
	if _, err := sdk.Connect(ctxT(t), sdk.Remote("ftp://x"), sdk.WithProject("web")); err == nil {
		t.Fatal("ftp accepted")
	}
	if _, err := sdk.Connect(ctxT(t), sdk.Remote("https://x", sdk.WithPinnedFingerprint("zz")), sdk.WithProject("web")); err == nil {
		t.Fatal("bad pin accepted")
	}
}

func TestProjectScopingAndNewMethods(t *testing.T) {
	srv, c := newEnv(t)
	seedBasics(srv)
	ctx := ctxT(t)
	query := func() string {
		r := srv.Requests()
		return r[len(r)-1].Query
	}
	// There is no default project: a client without one is refused by the server, and a client with one sends it.
	ts0 := httptest.NewServer(srv)
	defer ts0.Close()
	un, err := sdk.Connect(ctx, sdk.Remote(ts0.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer un.Close()
	if _, err := un.Jails().Get(ctx, "nope"); !api.IsCode(err, api.CodeInvalid) || !strings.Contains(err.Error(), "a project is required") {
		t.Fatalf("a request without a project: %v", err)
	}
	if _, err := c.Jails().Get(ctx, "nope"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatal(err)
	}
	if q := query(); q != "project=web" {
		t.Fatalf("query %q", q)
	}
	if _, err := c.Projects().Create(ctx, api.Project{Metadata: api.ObjectMeta{Name: "db"}, Spec: api.ProjectSpec{
		Networks: []api.ProjectNetwork{{Network: "backend"}}, Storage: []api.ProjectStorage{{Storage: "*"}}}}); err == nil {
		t.Fatal("a project granted a network that does not exist")
	}
	w := un.Project("web")
	if w.ProjectName() != "web" || un.ProjectName() != "" {
		t.Fatal("project names")
	}
	if _, err := mustJailIn(t, w, "app").Metadata, error(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Jails().Get(ctx, "app"); err != nil {
		t.Fatal(err)
	}
	if q := query(); q != "project=web" {
		t.Fatalf("query %q", q)
	}
	if _, err := un.Project("db").Jails().Get(ctx, "app"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("another project must not see it: %v", err)
	}
	// A request's own project wins, and Host keeps the project.
	if _, err := w.Operations().Get(ctx, "x"); err == nil {
		t.Fatal("unknown operation")
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	pc, err := sdk.Connect(ctx, sdk.Remote(ts.URL, sdk.WithProject("web")))
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	if _, err := pc.Jails().Get(ctx, "app"); err != nil {
		t.Fatalf("WithProject: %v", err)
	}
	if _, err := pc.Project("db").Jails().Get(ctx, "app"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("Project must override WithProject: %v", err)
	}
	// Identity, trust, sessions, storage.
	id, err := c.Whoami(ctx)
	if err != nil || !id.Global || id.Permission != api.PermAdmin {
		t.Fatalf("%+v %v", id, err)
	}
	tt, err := c.Trust().CreateToken(ctx, api.TrustTokenRequest{Name: "laptop", Permission: api.PermReadonly, Projects: []string{"web"}})
	if err != nil || !strings.HasPrefix(tt.Token, sdk.TrustTokenPrefix) || tt.ExpiresAt.IsZero() {
		t.Fatalf("%+v %v", tt, err)
	}
	if _, err := sdk.TrustTokenFingerprint("jot_abc"); err == nil {
		t.Fatal("not a trust token")
	}
	if _, err := sdk.TrustTokenFingerprint("joc_!!!.x"); err == nil {
		t.Fatal("garbage accepted")
	}
	n := 0
	for _, err := range c.Sessions().List(ctx) {
		if err != nil {
			t.Fatal(err)
		}
		n++
	}
	if n != 0 {
		t.Fatalf("%d sessions", n)
	}
	if err := c.Sessions().Revoke(ctx, "nope"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatal(err)
	}
	st, err := c.Storage().Get(ctx, "default")
	if err != nil || st.Status.Pool == "" {
		t.Fatalf("%+v %v", st, err)
	}
	if _, err := c.Projects().Aggregate(ctx, "web"); !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("aggregate on a daemon that is not a hub: %v", err)
	}
}

func mustJailIn(t *testing.T, c *sdk.Client, name string) api.Jail {
	return mustJail(t, c, name, webSpec())
}
