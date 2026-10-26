package hostbackup

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"jailops.net/jailops/internal/node/wire"
	"jailops.net/jailops/internal/repo/engine"
	"jailops.net/jailops/internal/repo/format"
	"jailops.net/jailops/internal/repo/keys"
	"jailops.net/jailops/internal/sys/zfs"
	"jailops.net/jailops/sdk/api"
)

// countingUploader answers Missing from a set of ids it "has".
type countingUploader struct {
	engine.Uploader
	mu    sync.Mutex
	has   map[format.ID]bool
	calls int
	max   int
	err   error
}

func (c *countingUploader) Missing(_ context.Context, ids []format.ID) ([]format.ID, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.max = max(c.max, len(ids))
	if c.err != nil {
		return nil, c.err
	}
	var out []format.ID
	for _, id := range ids {
		if !c.has[id] {
			out = append(out, id)
		}
	}
	return out, nil
}
func (c *countingUploader) Close() {}

func idN(n int) format.ID {
	var id format.ID
	binary.BigEndian.PutUint64(id[:8], uint64(n)+1)
	return id
}

func TestMissingQueriesAreBatched(t *testing.T) {
	cu := &countingUploader{has: map[format.ID]bool{}}
	for i := 0; i < 100; i += 2 {
		cu.has[idN(i)] = true // even ids are present
	}
	b := newBatchUploader(bg, cu)
	var wg sync.WaitGroup
	got := make([][]format.ID, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m, err := b.Missing(bg, []format.ID{idN(i)})
			if err != nil {
				t.Error(err)
			}
			got[i] = m
		}()
	}
	wg.Wait()
	for i, m := range got {
		if present := i%2 == 0; present != (len(m) == 0) {
			t.Fatalf("id %d: missing = %v", i, m)
		}
	}
	if cu.calls >= 50 {
		t.Fatalf("100 one-id queries made %d requests: not batched", cu.calls)
	}
	// ids found present are asked once
	calls := cu.calls
	if m, _ := b.Missing(bg, []format.ID{idN(0)}); len(m) != 0 || cu.calls != calls {
		t.Fatalf("a pinned id was asked again (%d calls)", cu.calls-calls)
	}
	// a big query is split at the protocol limit
	big := make([]format.ID, 5000)
	for i := range big {
		big[i] = idN(1000 + i)
	}
	cu.max = 0
	if m, err := b.Missing(bg, big); err != nil || len(m) != 5000 || cu.max > wire.MaxMissingBatch {
		t.Fatalf("big query: %d missing, largest request %d, %v", len(m), cu.max, err)
	}
	// errors reach every waiter
	cu.err = errInjected
	var wg2 sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg2.Add(1)
		go func() {
			defer wg2.Done()
			if _, err := b.Missing(bg, []format.ID{idN(5000 + i)}); !errors.Is(err, errInjected) {
				t.Errorf("err = %v", err)
			}
		}()
	}
	wg2.Wait()
	// and a cancelled caller does not hang
	ctx, cancel := context.WithCancel(bg)
	cancel()
	cu.err = nil
	if _, err := b.Missing(ctx, []format.ID{idN(9000)}); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestBackupBatchesItsMissingQueries(t *testing.T) {
	inner, _, _ := memRemote(t)
	r := newRig(t, rigOpts{remote: &batchProbe{Remote: inner}, cfg: func(c *Config) { c.EngineWorkers = 16 }})
	j := r.jail("web1", "uuid-web1", nil)
	res := r.backup(j).Targets[0].Result
	probe := r.svc.deps.Remote.(*batchProbe)
	if res.NewChunks == 0 || probe.calls == 0 {
		t.Fatalf("no uploads or no queries: %+v", res)
	}
	if probe.asked < probe.calls {
		t.Fatalf("asked %d ids in %d requests", probe.asked, probe.calls)
	}
	t.Logf("%d ids in %d requests", probe.asked, probe.calls)
}

type batchProbe struct {
	Remote
	mu           sync.Mutex
	calls, asked int
}

func (p *batchProbe) NewUploader(ctx context.Context, g, s, prev string) (engine.Uploader, error) {
	u, err := p.Remote.NewUploader(ctx, g, s, prev)
	if err != nil {
		return nil, err
	}
	return &probeUp{Uploader: u, p: p}, nil
}

type probeUp struct {
	engine.Uploader
	p *batchProbe
}

func (u *probeUp) Missing(ctx context.Context, ids []format.ID) ([]format.ID, error) {
	u.p.mu.Lock()
	u.p.calls++
	u.p.asked += len(ids)
	u.p.mu.Unlock()
	return u.Uploader.Missing(ctx, ids)
}

func TestFileCatalogPersists(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state", "catalog.json")
	c, err := OpenFileCatalog(p)
	if err != nil {
		t.Fatal(err)
	}
	b := api.Backup{Metadata: api.ObjectMeta{Name: "20260101000000-aa"}, Spec: api.BackupSpec{Jail: "web1", Snapshot: "20260101000000-aa"}}
	if err := c.PutBackup(bg, b); err != nil {
		t.Fatal(err)
	}
	if err := c.PutRun(bg, Run{ID: "r1", Jail: "web1", OK: true}); err != nil {
		t.Fatal(err)
	}
	c2, err := OpenFileCatalog(p)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, _ := c2.GetBackup(bg, b.Metadata.Name)
	runs, _ := c2.Runs(bg)
	if !ok || got.Spec.Jail != "web1" || len(runs) != 1 {
		t.Fatalf("reloaded = %+v %v %v", got, ok, runs)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", fi.Mode())
	}
	if err := os.WriteFile(p, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileCatalog(p); err == nil {
		t.Fatal("a damaged catalog loaded")
	}
}

func TestCleanupStaleOnlyTouchesOldOwnedSnapshots(t *testing.T) {
	r := newRig(t, rigOpts{remote: mustMem(t)})
	j := r.jail("web1", "uuid-web1", nil)
	now := r.clock.Now()
	seq := 0
	mk := func(name string, age time.Duration, managed string) string {
		seq++
		sn := SnapshotPrefix + now.Add(-age).Format("20060102T150405") + "-ab1" + string(rune('0'+seq))
		if name != "" {
			sn = name
		}
		props := map[string]string{}
		if managed != "" {
			props[snapManaged] = managed
		}
		if err := r.w.Fake.Snapshot(bg, zfs.SnapshotOpts{Names: []string{j.Container + "@" + sn}, Props: props}); err != nil {
			t.Fatal(err)
		}
		return sn
	}
	old := mk("", 10*time.Hour, "on")
	fresh := mk("", time.Hour, "on")
	foreign := mk("", 10*time.Hour, "")
	other := mk("daily-1", 0, "on")
	n, err := r.svc.CleanupStale(bg)
	if err != nil || n != 1 {
		t.Fatalf("cleanup = %d, %v", n, err)
	}
	has := func(sn string) bool { ok, _ := r.w.Exists(bg, j.Container+"@"+sn); return ok }
	if has(old) || !has(fresh) || !has(foreign) || !has(other) {
		t.Fatalf("old=%v fresh=%v foreign=%v other=%v", has(old), has(fresh), has(foreign), has(other))
	}
}

func TestDirectoryTarget(t *testing.T) {
	dir := t.TempDir()
	// an attached disk: a directory repository
	{
		b, err := dirBackend(dir)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := storeInit(b); err != nil {
			t.Fatal(err)
		}
	}
	r := newRig(t, rigOpts{noEscrow: true})
	r.pols.targets = []api.BackupTarget{{Metadata: api.ObjectMeta{Name: "disk"}, Spec: api.BackupTargetSpec{Kind: "directory", Path: dir}}}
	if err := r.svc.EscrowKey(bg, []string{r.recipient}); err == nil {
		t.Fatal("escrow needs the default repository, which is not configured")
	}
	j := r.jail("web1", "uuid-web1", nil)
	res, err := r.svc.BackupJail(bg, j, CaptureOptions{Targets: []string{"disk"}})
	codeOf(t, err, api.CodeKeyNotEscrowed)
	_ = res
	rm, err := r.svc.remoteFor(bg, "disk")
	if err != nil {
		t.Fatal(err)
	}
	if err := keys.Escrow(bg, r.svc.currentKey(), []string{r.recipient}, rm); err != nil {
		t.Fatal(err)
	}
	res, err = r.svc.BackupJail(bg, j, CaptureOptions{Targets: []string{"disk"}, Policy: "usb"})
	if err != nil || res.Targets[0].Backup.Spec.Target != "disk" {
		t.Fatalf("backup to a directory: %v", err)
	}
	if _, err := r.svc.Restore(bg, api.RestoreRequest{Backup: res.Targets[0].Backup.Metadata.Name, As: "web2"}); err != nil {
		t.Fatal(err)
	}
}

func TestOSMounterReadsTheSnapshotDirectory(t *testing.T) {
	mp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(mp, ".zfs", "snapshot", "jailops-backup-x"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := &OSMounter{ZFS: zfs.NewFake("tank")}
	d, err := m.SnapshotDir(bg, Dataset{Name: "tank/a", Mountpoint: mp}, "jailops-backup-x")
	if err != nil || !strings.HasSuffix(d, filepath.Join(".zfs", "snapshot", "jailops-backup-x")) {
		t.Fatalf("dir = %q, %v", d, err)
	}
	if _, err := m.SnapshotDir(bg, Dataset{Name: "tank/a", Mountpoint: mp}, "nope"); err == nil {
		t.Fatal("a missing snapshot directory was accepted")
	}
	if _, err := m.SnapshotDir(bg, Dataset{Name: "tank/a", Mountpoint: "none"}, "x"); err == nil {
		t.Fatal("a dataset without a mountpoint was accepted")
	}
}

func TestConfigValidation(t *testing.T) {
	if _, err := New(Config{}, Deps{ZFS: zfs.NewFake("tank")}); !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("no host uuid: %v", err)
	}
	if _, err := New(Config{HostUUID: "h", Node: NodeConfig{Address: "x:1"}}, Deps{ZFS: zfs.NewFake("tank")}); !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("no fingerprint: %v", err)
	}
	if _, err := New(Config{HostUUID: "h"}, Deps{}); err == nil {
		t.Fatal("no zfs")
	}
	// no key file is not an error; the default path is the documented one
	s, err := New(Config{HostUUID: "h", KeyPath: filepath.Join(t.TempDir(), "k")}, Deps{ZFS: zfs.NewFake("tank")})
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Status(bg); st.Status != keys.StatusNone {
		t.Fatalf("status = %v", st.Status)
	}
	if DefaultKeyPath != "/var/db/jailops/backup.key" {
		t.Fatal(DefaultKeyPath)
	}
}

func TestDeleteIsNodeSide(t *testing.T) {
	r := newRig(t, rigOpts{remote: mustMem(t)})
	if err := r.svc.Delete(bg, "x"); !api.IsCode(err, api.CodeUnsupported) {
		t.Fatalf("delete = %v", err)
	}
}

func TestObserverSeesEveryRun(t *testing.T) {
	r := newRig(t, rigOpts{remote: mustMem(t)})
	obs := &obsRec{}
	r.svc.deps.Observer = obs
	j := r.jail("web1", "uuid-web1", nil)
	r.backup(j)
	r.svc.RunDrill(bg, r.lastBackup(), "deep")
	if len(obs.runs) != 2 || obs.runs[0].Kind != RunBackup || obs.runs[1].Kind != RunDrill {
		t.Fatalf("observed %+v", obs.runs)
	}
}

type obsRec struct {
	mu   sync.Mutex
	runs []Run
}

func (o *obsRec) RunFinished(_ context.Context, r Run) {
	o.mu.Lock()
	o.runs = append(o.runs, r)
	o.mu.Unlock()
}

func (r *rig) lastBackup() string {
	bs, _ := r.svc.List(bg)
	return bs[0].Metadata.Name
}
