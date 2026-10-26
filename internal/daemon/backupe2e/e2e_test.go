// Package backupe2e tests the daemon's host-side backups against a real
// backup node (internal/node/nodetest): key creation and escrow, a policy
// run, a restore into another project, and the audit and secrecy rules of the
// key routes.
package backupe2e

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"

	"jailops.net/jailops/internal/daemon"
	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/hostbackup"
	"jailops.net/jailops/internal/node"
	"jailops.net/jailops/internal/node/nodetest"
	"jailops.net/jailops/internal/testdb"
	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

func TestMain(m *testing.M) { testdb.Main(m) }

// dirMounter stands in for the .zfs/snapshot directories: a directory per
// dataset, and a snapshot is that directory.
type dirMounter struct {
	mu   sync.Mutex
	root string
	live map[string]string
}

func (m *dirMounter) dir(ds string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d, ok := m.live[ds]; ok {
		return d
	}
	d := filepath.Join(m.root, strings.NewReplacer("/", "_", "@", "-").Replace(ds))
	_ = os.MkdirAll(d, 0o755)
	m.live[ds] = d
	return d
}

func (m *dirMounter) SnapshotDir(_ context.Context, d hostbackup.Dataset, _ string) (string, error) {
	return m.dir(d.Name), nil
}
func (m *dirMounter) MountDir(_ context.Context, ds string) (string, error) { return m.dir(ds), nil }

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func setup(t *testing.T) (*dtest.Env, *dirMounter, *nodetest.Env, string) {
	t.Helper()
	return setupAt(t)
}

func setupAt(t *testing.T) (*dtest.Env, *dirMounter, *nodetest.Env, string) {
	t.Helper()
	env := nodetest.NewEnv(t, nodetest.Options{})
	h := env.Host(t, "host-1")
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env.Node.Handler().ServeHTTP(w, r.WithContext(node.WithHost(r.Context(), h)))
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	mnt := &dirMounter{root: t.TempDir(), live: map[string]string{}}
	e := dtest.New(t, dtest.Options{
		BackupTLS:     &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h2", "http/1.1"}},
		BackupMounter: mnt,
		Mutate: func(c *daemon.Config) {
			c.Backup = &daemon.BackupConf{
				KeyPath:            filepath.Join(c.DataDir, "backup.key"),
				Node:               daemon.BackupNode{Address: srv.Listener.Addr().String(), Fingerprint: strings.Repeat("0", 64), CertFile: "/x/cert.pem", KeyFile: "/x/key.pem"},
				RecoveryRecipients: []string{id.Recipient().String()},
				Concurrency:        2,
			}
		}})
	e.Seed()
	return e, mnt, env, srv.Listener.Addr().String()
}

func TestKeyRunRestoreAcrossProjects(t *testing.T) {
	e, mnt, _, addr := setup(t)
	c := ctxT(t)
	bk := e.Admin.Backups()

	// The extensions are advertised because a backend is configured.
	for _, ext := range []string{"backup_key", "backup_run", "backup_verify", "restore_preflight"} {
		if err := e.Admin.Require(c, ext); err != nil {
			t.Fatal(err)
		}
	}
	st, err := bk.KeyStatus(c)
	if err != nil || st.Status != "none" {
		t.Fatalf("%+v %v", st, err)
	}
	// Nothing is backed up without a key.
	made, err := bk.CreateKey(c)
	if err != nil {
		t.Fatal(err)
	}
	if made.RecoveryKey == "" || made.Domain == "" || !made.Escrowed {
		t.Fatalf("%+v", made)
	}
	if st, _ = bk.KeyStatus(c); st.Status != "escrowed" {
		t.Fatalf("%+v", st)
	}
	// A second key is refused: rotating means a new repository.
	if _, err := bk.CreateKey(c); !api.IsCode(err, api.CodeAlreadyExists) {
		t.Fatalf("%v", err)
	}
	// The recovery key is nowhere but in that one reply: not in the log, the
	// audit, the operations or the key status.
	assertNoSecret := func(where, text string) {
		if strings.Contains(text, made.RecoveryKey) {
			t.Fatalf("the recovery key appears in %s", where)
		}
	}
	assertNoSecret("the log", e.Log())
	entries, _ := e.Admin.Audit(c, 500)
	for _, a := range entries {
		assertNoSecret("the audit", fmt.Sprintf("%+v", a))
	}
	for op, err := range e.Admin.Operations().List(c) {
		if err != nil {
			t.Fatal(err)
		}
		assertNoSecret("an operation", fmt.Sprintf("%+v", op))
	}
	// The key routes are admin only and global only.
	if code, _ := e.Raw("GET", "/1.0/backup/key", e.Token(api.PermOperator), ""); code != 403 {
		t.Fatalf("operator read the key status: %d", code)
	}
	if code, _ := e.Raw("POST", "/1.0/backup/key", e.Token(api.PermOperator), "{}"); code != 403 {
		t.Fatalf("operator created a key: %d", code)
	}
	scoped := e.Scoped(api.PermAdmin, dtest.SeedProject)
	if _, err := scoped.Backups().KeyStatus(c); !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("a project admin read the key status: %v", err)
	}

	// A jail with data, a node target and a policy.
	jop, err := e.Admin.Jails().Create(c, dtest.WebJail("web"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jop.Wait(c); err != nil {
		t.Fatal(err)
	}
	web, err := e.Admin.Jails().Get(c, "web")
	if err != nil {
		t.Fatal(err)
	}
	lay, _ := e.D.Service().BackupJails(c)
	var dbDS string
	for _, j := range lay {
		if j.Name == "web" {
			for _, d := range j.Layout.Datasets {
				if strings.HasSuffix(d.Name, "/web/etc") {
					dbDS = d.Name
				}
			}
		}
	}
	if dbDS == "" {
		t.Fatalf("no volume dataset in %+v", lay)
	}
	_ = os.WriteFile(filepath.Join(mnt.dir(dbDS), "rc.conf"), []byte("rows rows rows"), 0o644)
	_ = web
	if _, err := bk.Targets().Create(c, api.BackupTarget{Metadata: api.ObjectMeta{Name: "vault"}, Spec: api.BackupTargetSpec{Kind: "node", Address: addr, Fingerprint: strings.Repeat("0", 64)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := bk.Policies().Create(c, api.BackupPolicy{Metadata: api.ObjectMeta{Name: "nightly"}, Spec: api.BackupPolicySpec{
		Selector: map[string]string{"all": ""}, Targets: []string{"vault"}, Schedule: "0 3 * * *", Retention: api.RetentionPolicy{Last: 3}}}); err != nil {
		t.Fatal(err)
	}
	// An operator cannot run it; an admin can.
	if _, err := e.Client(api.PermOperator).Backups().Run(c, "nightly"); !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("operator ran a policy: %v", err)
	}
	rop, err := bk.Run(c, "nightly")
	if err != nil {
		t.Fatal(err)
	}
	runs, err := rop.Wait(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || !runs[0].OK || runs[0].Jail != "web" || runs[0].Snapshot == "" {
		t.Fatalf("%+v", runs)
	}
	var backups []api.Backup
	for b, err := range bk.List(c) {
		if err != nil {
			t.Fatal(err)
		}
		backups = append(backups, b)
	}
	if len(backups) != 1 || backups[0].Spec.Jail != "web" {
		t.Fatalf("%+v", backups)
	}
	id := backups[0].Metadata.Name

	// Verify reads it back.
	vop, err := bk.Verify(c, id)
	if err != nil {
		t.Fatal(err)
	}
	if vr, err := vop.Wait(c); err != nil || !vr.OK {
		t.Fatalf("%+v %v", vr, err)
	}

	// Restore into another project: the project's grants apply and the jail
	// lands in its namespace, with spec.json and bundle.json written by the adopt.
	pc := e.Admin.Projects()
	betaNet := e.MakeNetwork("beta") // networks are not shared: beta has its own, and the restore maps lan onto it
	if _, err := pc.Create(c, api.Project{Metadata: api.ObjectMeta{Name: "beta"}, Spec: api.ProjectSpec{
		Networks: []api.ProjectNetwork{{Network: betaNet}},
		Storage:  []api.ProjectStorage{{Storage: "default"}}}}); err != nil {
		t.Fatal(err)
	}
	pf, err := bk.Preflight(c, api.RestoreRequest{Backup: id, As: "web2", Project: "beta", NetworkMap: map[string]string{"lan": betaNet}})
	if err != nil || !pf.OK {
		t.Fatalf("%+v %v", pf, err)
	}
	op, err := bk.Restore(c, api.RestoreRequest{Backup: id, As: "web2", Project: "beta", NetworkMap: map[string]string{"lan": betaNet}})
	if err != nil {
		var ae *api.Error
		if errors.As(err, &ae) {
			t.Fatalf("%v %v", err, ae.Detail)
		}
		t.Fatal(err)
	}
	j2, err := op.Wait(c)
	if err != nil {
		var ae *api.Error
		if errors.As(err, &ae) {
			t.Fatalf("%v %v", err, ae.Detail)
		}
		t.Fatal(err)
	}
	if j2.Metadata.Name != "web2" || j2.Metadata.Project != "beta" {
		t.Fatalf("%+v", j2.Metadata)
	}
	got, err := e.Admin.Project("beta").Jails().Get(c, "web2")
	if err != nil || got.Metadata.UUID == web.Metadata.UUID {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := e.Admin.Project(dtest.SeedProject).Jails().Get(c, "web2"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("the restored jail leaked into the first project: %v", err)
	}
	// The records the adopt wrote are on the restored container.
	for _, f := range []string{"spec.json", "bundle.json"} {
		found := false
		for p := range e.Files.Data {
			if strings.HasSuffix(p, "/beta_web2/"+f) || strings.HasSuffix(p, "/jails/web2/"+f) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s was not written by the adopt: %v", f, e.Files.Paths())
		}
	}
	// A project the caller cannot use is not_found, not forbidden.
	bob := e.Scoped(api.PermAdmin, dtest.SeedProject)
	if _, err := bob.Backups().Preflight(c, api.RestoreRequest{Backup: id, As: "x", Project: "beta"}); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("%v", err)
	}
	var _ *sdk.Client
}
