package backupd_test

import (
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"jailops.net/jailops/internal/backupd"
	"jailops.net/jailops/internal/node"
	"jailops.net/jailops/internal/node/ctl"
	"jailops.net/jailops/internal/node/nodetest"
	"jailops.net/jailops/internal/testdb"
	"jailops.net/jailops/sdk/api"
)

func tableCounts(t *testing.T, db *ctl.DB) map[string]int {
	t.Helper()
	rows, err := db.Pool().Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		names = append(names, n)
	}
	rows.Close()
	out := map[string]int{}
	for _, n := range names {
		var c int
		if err := db.Pool().QueryRow(ctx, `SELECT count(*) FROM `+n).Scan(&c); err != nil {
			t.Fatal(err)
		}
		out[n] = c
	}
	return out
}

// Every table is accounted for: rebuilt from the repository, operational and
// transient, or not recoverable. A new table must be classified here.
func TestEveryTableIsClassified(t *testing.T) {
	r := startRig(t, memKind, nil)
	classified := map[string]string{}
	for _, n := range ctl.RebuiltTables {
		classified[n] = "rebuilt"
	}
	for _, n := range ctl.NotRecoverableTable {
		classified[n] = "not recoverable"
	}
	for _, n := range ctl.OperationalTables {
		classified[n] = "operational"
	}
	for n := range tableCounts(t, r.DB) {
		if classified[n] == "" {
			t.Errorf("table %s is not classified in ctl.RebuiltTables, NotRecoverableTable or OperationalTables", n)
		}
	}
}

func TestLosingTheDatabaseCostsOnlyWhatTheRepositoryCannotHold(t *testing.T) {
	r := startRig(t, memKind, nil)
	f := r.Fix
	ha, hb := r.enroll("host-a"), r.enroll("host-b")
	ga, gb := f.Group("host-a", "web"), f.Group("host-b", "db")
	var all []*nodetest.Backup
	for i, c := range []struct {
		h *hostIdentity
		g string
	}{{ha, ga}, {ha, ga}, {hb, gb}} {
		b := f.NewBackup(t, c.g, nodetest.SnapshotID(r.Clock.Now(), i+1), manyChunks(t, f, 4))
		upload(t, c.h.C, b, "")
		all = append(all, b)
		r.Clock.Advance(time.Minute)
	}
	// state that must come back: protection, a deletion request, a failed verification, the owner of each group
	a := r.admin()
	base := "/api/v1/repo/admin"
	if err := a.Call(ctx, "PUT", base+"/groups/"+ga+"/snapshots/"+all[0].Snapshot+"/protected", node.ProtectBody{Protected: true}, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Call(ctx, "POST", base+"/groups/"+gb+"/snapshots/"+all[2].Snapshot+"/deletion", node.DeletionBody{Delay: "72h"}, nil); err != nil {
		t.Fatal(err)
	}
	r.Raw.(interface{ Corrupt(string, int) }).Corrupt(chunkKey(all[1].Chunks[0].ID), 40)
	adminJob[node.VerifyStats](t, r, "/verify", node.VerifyOptions{Mode: "all"})
	// state that cannot come back
	_ = backupd.CreateUser(ctx, r.DB, "alice", "a password that is long", "admin", testParams)
	_, _ = r.DB.CreateToken(ctx, "ci", []byte("hash"), "readonly", nil, nil)
	_ = r.DB.SetRetentionPolicy(ctx, "default", api.RetentionPolicy{Daily: 7}, "UTC", "alice")

	before, err := r.DB.Snapshots(ctx, "")
	if err != nil || len(before) != 3 {
		t.Fatalf("%d %v", len(before), err)
	}
	counts := tableCounts(t, r.DB)
	for _, n := range ctl.NotRecoverableTable {
		if counts[n] == 0 {
			t.Fatalf("test setup: table %s is empty", n)
		}
	}

	// --- the database is gone: a new empty one, the same repository, the same node identity
	r.stop()
	db2, err := ctl.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db2.Close)
	if err := db2.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	pp := testParams
	srv, err := backupd.New(ctx, r.Cfg, backupd.Options{Backend: r.Raw, DB: db2, Now: r.Clock.Now, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), PasswordParams: &pp,
		Tune: func(c *node.Config) { c.MinGrace = time.Minute; c.Grace = time.Hour }})
	if err != nil {
		t.Fatalf("the node must come back on its repository with the configured server id: %v", err)
	}
	defer srv.Close()
	for n, c := range tableCounts(t, db2) {
		if c != 0 && n != "schema_migrations" {
			t.Errorf("a fresh database has rows in %s", n)
		}
	}
	st, _, err := srv.Node.Reconcile(ctx, "recovery")
	if err != nil || st.Added != 3 || st.Groups != 2 {
		t.Fatalf("%+v %v", st, err)
	}
	after, err := db2.Snapshots(ctx, "")
	if err != nil || len(after) != len(before) {
		t.Fatalf("%d snapshots after reconcile %v", len(after), err)
	}
	for i := range before {
		b, a := before[i], after[i]
		if b.GroupID != a.GroupID || b.SnapshotID != a.SnapshotID || !b.CommittedAt.Equal(a.CommittedAt) || b.IndexDigest != a.IndexDigest ||
			b.ManifestDigest != a.ManifestDigest || b.ChunkCount != a.ChunkCount || b.Protected != a.Protected || b.VerifyState != a.VerifyState ||
			(b.PendingDeletionAfter == nil) != (a.PendingDeletionAfter == nil) || b.PendingDeletionBy != a.PendingDeletionBy {
			t.Errorf("snapshot %d differs after the rebuild:\n before %+v\n after  %+v", i, b, a)
		}
	}
	// what was lost is exactly the list the documentation gives
	got := tableCounts(t, db2)
	if l, _ := db2.AuditLog(ctx, "", 100, 0); len(l) != got["audit"] {
		t.Fatal("audit count")
	} else {
		for _, e := range l {
			if e.Action != "reconcile.start" && e.Action != "reconcile.finish" {
				t.Errorf("the audit log of the lost database came back: %+v", e)
			}
		}
		got["audit"] = 0 // only the entries of the reconcile itself
	}
	for _, n := range ctl.NotRecoverableTable {
		if got[n] != 0 {
			t.Errorf("table %s has %d rows after the rebuild: it cannot be rebuilt from the repository, so something is reading it from elsewhere", n, got[n])
		}
	}
	// ownership came back from the repository: the host that made a group is the only one that can write to it
	hosts, _ := db2.Hosts(ctx)
	if len(hosts) != 0 {
		t.Fatalf("enrolments reappeared: %v", hosts)
	}
	evil, err := backupd.EnrollHost(ctx, db2, backupd.HostBody{Name: "evil", Fingerprint: fmt.Sprintf("%064x", 7)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Node.Begin(ctx, node.Host{ID: evil.ID, Name: evil.Name, Scopes: evil.Scopes}, ga, nodetest.SnapshotID(r.Clock.Now(), 50), ""); code(err) != api.CodeForbidden {
		t.Fatalf("after the database loss another host claimed a group: %v", err)
	}
	// the original host is enrolled again (same name, new certificate) and carries on, with its base
	again, err := backupd.EnrollHost(ctx, db2, backupd.HostBody{Name: "host-a", Fingerprint: fmt.Sprintf("%064x", 8)})
	if err != nil {
		t.Fatal(err)
	}
	si, err := srv.Node.Begin(ctx, node.Host{ID: again.ID, Name: again.Name, Scopes: again.Scopes}, ga, nodetest.SnapshotID(r.Clock.Now(), 51), all[1].Snapshot)
	if err != nil || si.Previous != all[1].Snapshot {
		t.Fatalf("%+v %v", si, err)
	}
	// verification state came back: the corrupt chunk is still found, and the failed snapshot is known as failed
	failed := 0
	for _, s := range after {
		if s.VerifyState == "failed" {
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("%d failed snapshots after the rebuild, want 1", failed)
	}
}
