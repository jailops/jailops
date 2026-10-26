package hostbackup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"jailops.net/jailops/internal/backup"
	"jailops.net/jailops/internal/repo/datastore"
	"jailops.net/jailops/sdk/api"
)

type fakeDrill struct {
	mu        sync.Mutex
	calls     []string
	healthErr error
	r         *rig
	datasets  []string // scratch datasets seen while the jail "runs"
}

func (f *fakeDrill) add(s string) { f.mu.Lock(); f.calls = append(f.calls, s); f.mu.Unlock() }
func (f *fakeDrill) CreateNetwork(_ context.Context, n string, _ api.NetworkSpec) error {
	f.add("net+")
	return nil
}
func (f *fakeDrill) DestroyNetwork(context.Context, string) error { f.add("net-"); return nil }
func (f *fakeDrill) StartJail(_ context.Context, p backup.DrillPlan) error {
	f.add("start")
	f.datasets = f.r.w.datasets(f.r.t, p.Scratch)
	return nil
}
func (f *fakeDrill) StopJail(context.Context, backup.DrillPlan) error { f.add("stop"); return nil }
func (f *fakeDrill) Health(context.Context, backup.DrillPlan) error {
	f.add("health")
	return f.healthErr
}

// corruptAChunk flips a byte in the middle of one stored chunk blob.
func corruptAChunk(t *testing.T, b *datastore.MemBackend) string {
	t.Helper()
	var key string
	var size int64
	_ = b.List(bg, "chunks/", func(o datastore.ObjectInfo) error {
		if o.Size > size {
			key, size = o.Key, o.Size
		}
		return nil
	})
	rc, err := b.Get(bg, key)
	if err != nil {
		t.Fatal(err)
	}
	blob := new(bytes.Buffer)
	blob.ReadFrom(rc)
	rc.Close()
	bad := append([]byte(nil), blob.Bytes()...)
	bad[len(bad)/2] ^= 0xff
	if err := b.Delete(bg, key); err != nil {
		t.Fatal(err)
	}
	if err := b.Put(bg, key, bytes.NewReader(bad), int64(len(bad)), datastore.PutOptions{IfAbsent: true}); err != nil {
		t.Fatal(err)
	}
	return key
}

func TestRestoreTestsDeepExtractFullPass(t *testing.T) {
	remote, _, _ := memRemote(t)
	r := newRig(t, rigOpts{remote: remote, cfg: func(c *Config) {
		c.ScratchDataset = "tank/jailops/scratch"
		c.ScratchMountBase = "/jos"
	}})
	fd := &fakeDrill{r: r}
	r.svc.deps.Drill = fd
	j := r.jail("web1", "uuid-web1", nil)
	bk := r.backup(j).Targets[0].Backup

	for _, d := range []backup.Depth{backup.DepthDeep, backup.DepthExtract, backup.DepthFull} {
		run, err := r.svc.RunDrill(bg, bk.Metadata.Name, d)
		if err != nil || !run.OK || run.Depth != string(d) {
			t.Fatalf("%s: %+v, %v", d, run, err)
		}
	}
	if got := strings.Join(fd.calls, ","); got != "net+,start,health,stop,net-" {
		t.Fatalf("full test calls = %s", got)
	}
	if len(fd.datasets) < 5 {
		t.Fatalf("scratch datasets while running = %v", fd.datasets)
	}
	if ds := r.w.datasets(t, "tank/jailops/scratch"); len(ds) > 1 {
		t.Fatalf("the full test left datasets: %v", ds)
	}
	ents, _ := os.ReadDir(r.cfg.ScratchDir)
	if len(ents) != 0 {
		t.Fatalf("the extract test left %d scratch entries", len(ents))
	}
	if n := len(runsOf(t, r, RunDrill)); n != 3 {
		t.Fatalf("%d drill runs recorded", n)
	}
	// a failing health check fails the test and still cleans up
	fd.healthErr = errors.New("service down")
	fd.calls = nil
	run, err := r.svc.RunDrill(bg, bk.Metadata.Name, backup.DepthFull)
	if err == nil || run.OK || !strings.Contains(err.Error(), "health check failed") {
		t.Fatalf("health failure = %+v, %v", run, err)
	}
	if got := strings.Join(fd.calls, ","); got != "net+,start,health,stop,net-" {
		t.Fatalf("calls = %s", got)
	}
	if ds := r.w.datasets(t, "tank/jailops/scratch"); len(ds) > 1 {
		t.Fatalf("left datasets: %v", ds)
	}
	// without a runner, and with a host that cannot run a test jail, a full test is unsupported, never a pass
	r.svc.deps.Drill = nil
	r.svc.deps.Host = plainHost{r.host}
	if _, err := r.svc.RunDrill(bg, bk.Metadata.Name, backup.DepthFull); !api.IsCode(err, api.CodeUnsupported) {
		t.Fatalf("full without runner: %v", err)
	}
}

func TestRestoreTestCatchesACorruptChunkAndMarksTheSnapshotFailed(t *testing.T) {
	for _, d := range []backup.Depth{backup.DepthDeep, backup.DepthExtract} {
		t.Run(string(d), func(t *testing.T) {
			remote, st, b := memRemote(t)
			r := newRig(t, rigOpts{remote: remote})
			j := r.jail("web1", "uuid-web1", nil)
			bk := r.backup(j).Targets[0].Backup
			if run, err := r.svc.RunDrill(bg, bk.Metadata.Name, d); err != nil || !run.OK {
				t.Fatalf("healthy: %+v, %v", run, err)
			}
			corruptAChunk(t, b)
			run, err := r.svc.RunDrill(bg, bk.Metadata.Name, d)
			if !errors.Is(err, ErrVerifyFailed) || run.OK || len(run.Problems) == 0 {
				t.Fatalf("corrupt chunk: %+v, %v", run, err)
			}
			// reported to the repository: the snapshot is failed and never a base again
			key := r.svc.currentKey()
			group := key.Keys.GroupID("host-uuid-1", "uuid-web1")
			vr, err := st.ReadVerify(bg, group, bk.Spec.Snapshot)
			if err != nil || vr == nil || !vr.Failed || vr.Mode != string(d) {
				t.Fatalf("verify record = %+v, %v", vr, err)
			}
			second, err := r.svc.BackupJail(bg, j, CaptureOptions{})
			if err != nil {
				t.Fatal(err)
			}
			sr := second.Targets[0].Result
			if !sr.Full || !strings.Contains(sr.Streams[0].WhyFull, "failed verification") {
				t.Fatalf("the next backup used the failed snapshot as a base: %+v", sr.Streams[0])
			}
		})
	}
}

func TestRestoreTestNeverRunsWithoutTheKeyOrOnTheNode(t *testing.T) {
	remote, _, _ := memRemote(t)
	r := newRig(t, rigOpts{remote: remote})
	j := r.jail("web1", "uuid-web1", nil)
	bk := r.backup(j).Targets[0].Backup
	h2 := newRig(t, rigOpts{remote: remote, noKey: true})
	h2.cat.PutBackup(bg, bk)
	if _, err := h2.svc.RunDrill(bg, bk.Metadata.Name, backup.DepthDeep); !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("drill without the key: %v", err)
	}
	env := &drillEnv{s: r.svc}
	if env.Kind() == backup.EnvBackupNode || !env.HoldsKey(bk.Spec.KeyDomain) || env.HoldsKey("other") {
		t.Fatal("the drill environment is not a key-holding host")
	}
	// the planner refuses a candidate that is a backup node
	plan, _ := backup.PlanDrill(bk, backup.DepthDeep, "", "d1")
	if _, err := backup.SelectExecutor(plan, []backup.Executor{{Name: "node", Kind: backup.EnvBackupNode, Keys: map[string]bool{bk.Spec.KeyDomain: true}}}); err == nil {
		t.Fatal("the backup node was selected to run a restore test")
	}
}

func TestScheduledRestoreTest(t *testing.T) {
	day := mustDay()
	remote, _, _ := memRemote(t)
	r := newRig(t, rigOpts{remote: remote, cfg: func(c *Config) { c.JitterWindow = -1 }})
	j := r.jail("web1", "uuid-web1", nil)
	p := policy("nightly", map[string]string{"all": ""}, "0 2 * * *")
	p.Spec.VerifySchedule, p.Spec.VerifyMode = "0 4 * * *", "extract"
	r.setPolicies(p)
	r.clock.Set(day.Add(time.Hour))
	r.svc.RunDue(bg)
	r.clock.Set(day.Add(2 * time.Hour))
	r.svc.RunDue(bg)
	if n := len(runsOf(t, r, RunDrill)); n != 0 {
		t.Fatalf("%d drills before their time", n)
	}
	r.clock.Set(day.Add(4 * time.Hour))
	if err := r.svc.RunDue(bg); err != nil {
		t.Fatal(err)
	}
	ds := runsOf(t, r, RunDrill)
	if len(ds) != 1 || !ds[0].OK || ds[0].Depth != "extract" || !ds[0].Scheduled || ds[0].Jail != j.Name {
		t.Fatalf("drills = %+v", ds)
	}
	// a damaged backup fails the scheduled test and is not retried
	corruptAChunkOf(t, remote)
	r.clock.Set(day.Add(28 * time.Hour))
	r.svc.RunDue(bg)
	r.clock.Set(day.Add(28*time.Hour + 5*time.Minute))
	r.svc.RunDue(bg)
	ds = runsOf(t, r, RunDrill)
	last := ds[len(ds)-1]
	if last.OK || len(last.Problems) == 0 {
		t.Fatalf("drills = %+v", ds)
	}
}

func mustDay() time.Time { return time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC) }

// corruptAChunkOf corrupts a chunk of the in-memory repository behind a remote.
func corruptAChunkOf(t *testing.T, r Remote) {
	t.Helper()
	d := r.(*dirRemote)
	corruptAChunk(t, d.s.Backend().(*datastore.MemBackend))
}

// plainHost is a Host that is not a DrillHost.
type plainHost struct{ Host }
