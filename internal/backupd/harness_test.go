package backupd_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"jailops.net/jailops/internal/auth"
	"jailops.net/jailops/internal/backupd"
	"jailops.net/jailops/internal/node"
	"jailops.net/jailops/internal/node/client"
	"jailops.net/jailops/internal/node/ctl"
	"jailops.net/jailops/internal/node/nodetest"
	"jailops.net/jailops/internal/repo/datastore"
	"jailops.net/jailops/internal/repo/s3"
	"jailops.net/jailops/internal/repo/s3fake"
	"jailops.net/jailops/internal/testdb"
)

// The tests do not run as root: the admin socket admits the test user as it admits root.
func TestMain(m *testing.M) {
	uid := uint32(os.Getuid())
	backupd.SetTestAdminUID(&uid)
	testdb.Main(m)
}

// rig is a running backup node with its listeners.
type rig struct {
	t      *testing.T
	Srv    *backupd.Server
	Cfg    *backupd.Config
	DB     *ctl.DB
	Raw    datastore.Backend
	Clock  *nodetest.Clock
	Fix    *nodetest.Fixture
	cancel context.CancelFunc
	done   chan error
	S3     *s3fake.Server
}

type backendKind string

const (
	memKind backendKind = "memory"
	s3Kind  backendKind = "s3fake"
	dirKind backendKind = "directory"
)

func newBackend(t *testing.T, kind backendKind, clock *nodetest.Clock) (datastore.Backend, *s3fake.Server) {
	t.Helper()
	switch kind {
	case memKind:
		m := datastore.NewMemBackend()
		m.SetClock(clock.Now)
		return m, nil
	case dirKind:
		b, err := datastore.NewDirBackend(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return b, nil
	}
	srv, err := s3fake.New(s3fake.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	_ = srv.EnsureBucket("repo")
	b, err := s3.New(s3.Config{Endpoint: srv.Endpoint(), Region: srv.Region(), Bucket: "repo", Prefix: "node1", NoTLS: true,
		Credentials: s3.Credentials{AccessKey: srv.AccessKey(), SecretKey: srv.SecretKey()}, BackoffBase: time.Millisecond, BackoffMax: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return b, srv
}

// startRig starts a node with the management API and the console on (they share the host listener).
func startRig(t *testing.T, kind backendKind, tune func(*node.Config)) *rig {
	t.Helper()
	return startRigWeb(t, kind, true, tune)
}

// startRigWeb starts a node with the management API and the console switched on or off.
func startRigWeb(t *testing.T, kind backendKind, web bool, tune func(*node.Config)) *rig {
	t.Helper()
	ctx := context.Background()
	r := &rig{t: t, Clock: nodetest.NewClock(), Fix: nodetest.NewFixture(t, 99)}
	r.Raw, r.S3 = newBackend(t, kind, r.Clock)
	db, err := ctl.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	r.DB = db
	dir, err := os.MkdirTemp("", "jo")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	yaml := fmt.Sprintf(`
hostname: testnode
listen: 127.0.0.1:0
admin_socket: %s
tls: {cert_file: %s, key_file: %s}
database: {dsn: "postgres://unused"}
datastore: {kind: directory, directory: %s}
web: {enabled: %t}
grace: 12h
`, filepath.Join(dir, "s"), filepath.Join(dir, "c.crt"), filepath.Join(dir, "c.key"), dir, web)
	cfg, err := backupd.ParseConfig([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	r.Cfg = cfg
	if _, _, err := cfg.EnsureServerCert(); err != nil {
		t.Fatal(err)
	}
	pp := auth.PasswordParams{MemoryKiB: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}
	if _, err := node.Init(ctx, r.Raw, node.InitOptions{ServerID: "node-rig", Host: "rig"}); err != nil {
		t.Fatal(err)
	}
	cfg.ServerID = "node-rig"
	srv, err := backupd.New(ctx, cfg, backupd.Options{Backend: r.Raw, DB: db, Now: r.Clock.Now, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		PasswordParams: &pp, Tune: func(c *node.Config) {
			c.MinGrace = time.Minute
			c.Grace = time.Hour
			if tune != nil {
				tune(c)
			}
		}})
	if err != nil {
		t.Fatal(err)
	}
	r.Srv = srv
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	sctx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	r.done = make(chan error, 1)
	go func() { r.done <- srv.Serve(sctx) }()
	t.Cleanup(r.stop)
	return r
}

func (r *rig) stop() {
	if r.cancel == nil {
		return
	}
	r.cancel()
	<-r.done
	r.cancel = nil
	r.Srv.Close()
}

// hostIdentity is a backup host: a certificate and a client for the node.
type hostIdentity struct {
	Name string
	Cert tls.Certificate
	FP   string
	C    *client.Client
}

func (r *rig) enroll(name string, scopes ...string) *hostIdentity {
	r.t.Helper()
	cert, _, _, err := auth.GenerateCertificate(auth.CertOptions{CommonName: name})
	if err != nil {
		r.t.Fatal(err)
	}
	fp := auth.Fingerprint(cert.Certificate[0])
	if len(scopes) == 0 {
		scopes = []string{"backup-push", "backup-pull"}
	}
	if _, err := backupd.EnrollHost(context.Background(), r.DB, backupd.HostBody{Name: name, Fingerprint: fp, Scopes: scopes}); err != nil {
		r.t.Fatal(err)
	}
	h := &hostIdentity{Name: name, Cert: cert, FP: fp}
	h.C = r.clientFor(cert)
	return h
}

func (r *rig) clientFor(cert tls.Certificate) *client.Client { return r.clientForDomain(cert, "") }

// clientForDomain is a host's client that names the key domain of its key when a session opens.
func (r *rig) clientForDomain(cert tls.Certificate, domain string) *client.Client {
	r.t.Helper()
	c, err := client.New(client.Config{Address: r.Srv.HostAddr(), Fingerprint: r.Srv.ServerFingerprint(), Cert: cert, Attempts: 2, KeyDomain: domain,
		BackoffBase: time.Millisecond, BackoffMax: 5 * time.Millisecond})
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(c.Close)
	return c
}

func (r *rig) admin() *backupd.AdminClient { return backupd.NewAdminClient(r.Srv.AdminSocket()) }

func (r *rig) httpFor(cert tls.Certificate) *http.Client {
	tc, err := auth.ClientConfigPinned(context.Background(), &cert, r.Srv.HostAddr(), r.Srv.ServerFingerprint())
	if err != nil {
		r.t.Fatal(err)
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: tc, DisableKeepAlives: true, ForceAttemptHTTP2: true}}
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

var testParams = auth.PasswordParams{MemoryKiB: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}
