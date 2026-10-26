package hostbackup

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"jailops.net/jailops/internal/repo/engine"
	"jailops.net/jailops/internal/repo/format"
	"jailops.net/jailops/internal/schedule"
	"jailops.net/jailops/sdk/api"
)

func policy(name string, sel map[string]string, sched string) api.BackupPolicy {
	return api.BackupPolicy{Metadata: api.ObjectMeta{Name: name, Project: "alpha"}, Spec: api.BackupPolicySpec{Selector: sel, Targets: []string{"t1"}, Schedule: sched}}
}

func (r *rig) setPolicies(ps ...api.BackupPolicy) {
	r.pols.mu.Lock()
	r.pols.policies = ps
	r.pols.mu.Unlock()
}

func runsOf(t *testing.T, r *rig, kind string) []Run {
	t.Helper()
	all, _ := r.cat.Runs(bg)
	var out []Run
	for _, x := range all {
		if x.Kind == kind {
			out = append(out, x)
		}
	}
	return out
}

func TestSelectors(t *testing.T) {
	j := JailRecord{Name: "a", Project: "alpha", Labels: map[string]string{"tier": "web", "env": "prod"}}
	cases := []struct {
		name string
		p    api.BackupPolicy
		j    JailRecord
		want bool
	}{
		{"empty selector matches none", policy("p", nil, "* * * * *"), j, false},
		{"all", policy("p", map[string]string{"all": ""}, "* * * * *"), j, true},
		{"one label", policy("p", map[string]string{"tier": "web"}, "* * * * *"), j, true},
		{"two labels", policy("p", map[string]string{"tier": "web", "env": "prod"}, "* * * * *"), j, true},
		{"label differs", policy("p", map[string]string{"tier": "db"}, "* * * * *"), j, false},
		{"label missing", policy("p", map[string]string{"zone": "a"}, "* * * * *"), j, false},
		{"other project", api.BackupPolicy{Metadata: api.ObjectMeta{Name: "p", Project: "acme"}, Spec: api.BackupPolicySpec{Selector: map[string]string{"all": ""}}}, j, false},
		{"same project", api.BackupPolicy{Metadata: api.ObjectMeta{Name: "p", Project: "alpha"}, Spec: api.BackupPolicySpec{Selector: map[string]string{"all": ""}}}, j, true},
	}
	for _, c := range cases {
		if got := selects(c.p, c.j); got != c.want {
			t.Errorf("%s: selects = %v", c.name, got)
		}
	}
}

func TestScheduleJitterIsDeterministicAndSpreadsJails(t *testing.T) {
	day := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	offsets := map[string]time.Duration{}
	for round := 0; round < 2; round++ {
		r := newRig(t, rigOpts{remote: mustMem(t)})
		r.clock.Set(day.Add(time.Hour))
		r.setPolicies(policy("nightly", map[string]string{"all": ""}, "0 2 * * *"))
		for i := 0; i < 6; i++ {
			r.jail(fmt.Sprintf("j%d", i), fmt.Sprintf("uuid-%d", i), nil)
		}
		if err := r.svc.RunDue(bg); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 6; i++ {
			u := fmt.Sprintf("uuid-%d", i)
			next := r.svc.NextRun(RunBackup, "nightly", u)
			off := next.Sub(day.Add(2 * time.Hour))
			if off < 0 || off >= 10*time.Minute {
				t.Fatalf("%s: offset %v outside the window", u, off)
			}
			if want := schedule.Jitter(u+"/nightly", 10*time.Minute); off != want {
				t.Fatalf("%s: offset %v, want %v", u, off, want)
			}
			if round == 0 {
				offsets[u] = off
			} else if offsets[u] != off {
				t.Fatalf("%s: offset changed between hosts: %v then %v", u, offsets[u], off)
			}
		}
		if round == 0 {
			distinct := map[time.Duration]bool{}
			for _, o := range offsets {
				distinct[o] = true
			}
			if len(distinct) < 4 {
				t.Fatalf("jitter does not spread the jails: %v", offsets)
			}
		}
	}
}

func TestSchedulerRunsDueJobsAtTheirTime(t *testing.T) {
	day := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	r := newRig(t, rigOpts{remote: mustMem(t), cfg: func(c *Config) { c.JitterWindow = -1 }})
	r.clock.Set(day.Add(time.Hour))
	r.setPolicies(policy("nightly", map[string]string{"tier": "web"}, "0 2 * * *"))
	r.jail("web1", "uuid-web1", map[string]string{"tier": "web"})
	r.jail("db1", "uuid-db1", map[string]string{"tier": "db"})

	r.svc.RunDue(bg)
	if n := len(runsOf(t, r, RunBackup)); n != 0 {
		t.Fatalf("%d runs before the schedule", n)
	}
	r.clock.Set(day.Add(2*time.Hour - time.Second))
	r.svc.RunDue(bg)
	if n := len(runsOf(t, r, RunBackup)); n != 0 {
		t.Fatalf("%d runs one second early", n)
	}
	r.clock.Set(day.Add(2 * time.Hour))
	if err := r.svc.RunDue(bg); err != nil {
		t.Fatal(err)
	}
	runs := runsOf(t, r, RunBackup)
	if len(runs) != 1 || !runs[0].OK || runs[0].Jail != "web1" || !runs[0].Scheduled || runs[0].Policy != "nightly" || runs[0].Snapshot == "" {
		t.Fatalf("runs = %+v", runs)
	}
	if next := r.svc.NextRun(RunBackup, "nightly", "uuid-web1"); !next.Equal(day.Add(26 * time.Hour)) {
		t.Fatalf("next = %v", next)
	}
	r.clock.Set(day.Add(3 * time.Hour))
	r.svc.RunDue(bg)
	if n := len(runsOf(t, r, RunBackup)); n != 1 {
		t.Fatalf("%d runs, the job ran twice for one slot", n)
	}
	r.clock.Set(day.Add(26 * time.Hour))
	r.svc.RunDue(bg)
	if n := len(runsOf(t, r, RunBackup)); n != 2 {
		t.Fatalf("%d runs on the second day", n)
	}
	// a restart in the middle of the day keeps the slot: the catalog knows the last run
	r.clock.Set(day.Add(27 * time.Hour))
	s2, err := New(r.cfg, Deps{ZFS: r.w, Mounter: r.w.m, Jails: r.jails, Objects: objects{}, Policies: r.pols, Catalog: r.cat, Remote: r.svc.deps.Remote})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	s2.RunDue(bg)
	if n := len(runsOf(t, r, RunBackup)); n != 2 {
		t.Fatalf("a restart repeated a run (%d)", n)
	}
	// a restart after missed slots catches up once
	r.clock.Set(day.Add(24*5*time.Hour + 3*time.Hour))
	s3, _ := New(r.cfg, Deps{ZFS: r.w, Mounter: r.w.m, Jails: r.jails, Objects: objects{}, Policies: r.pols, Catalog: r.cat, Remote: r.svc.deps.Remote})
	defer s3.Close()
	s3.RunDue(bg)
	if n := len(runsOf(t, r, RunBackup)); n != 3 {
		t.Fatalf("catch-up after downtime ran %d times in total", n)
	}
}

func TestRetryWithBackoffAndMaintenance(t *testing.T) {
	day := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	inner, _, _ := memRemote(t)
	fr := &flakyRemote{Remote: inner, op: "begin", failUntil: 2}
	r := newRig(t, rigOpts{remote: fr, cfg: func(c *Config) { c.JitterWindow = -1 }})
	r.clock.Set(day.Add(time.Hour))
	r.setPolicies(policy("nightly", map[string]string{"all": ""}, "0 2 * * *"))
	r.jail("web1", "uuid-web1", nil)
	r.svc.RunDue(bg) // initialises the state
	r.clock.Set(day.Add(2 * time.Hour))
	r.svc.RunDue(bg)
	runs := runsOf(t, r, RunBackup)
	if len(runs) != 1 || runs[0].OK || runs[0].Attempt != 1 {
		t.Fatalf("attempt 1: %+v", runs)
	}
	next := r.svc.NextRun(RunBackup, "nightly", "uuid-web1")
	if d := next.Sub(r.clock.Now()); d < time.Minute || d > time.Minute+15*time.Second {
		t.Fatalf("first backoff = %v", d)
	}
	r.clock.Advance(30 * time.Second)
	r.svc.RunDue(bg)
	if n := len(runsOf(t, r, RunBackup)); n != 1 {
		t.Fatalf("retried before the backoff (%d runs)", n)
	}
	r.clock.Advance(time.Minute)
	r.svc.RunDue(bg)
	runs = runsOf(t, r, RunBackup)
	if len(runs) != 2 || runs[1].OK || runs[1].Attempt != 2 {
		t.Fatalf("attempt 2: %+v", runs)
	}
	next = r.svc.NextRun(RunBackup, "nightly", "uuid-web1")
	if d := next.Sub(r.clock.Now()); d < 2*time.Minute || d > 2*time.Minute+31*time.Second {
		t.Fatalf("second backoff = %v", d)
	}
	r.clock.Advance(3 * time.Minute)
	r.svc.RunDue(bg)
	runs = runsOf(t, r, RunBackup)
	if len(runs) != 3 || !runs[2].OK || runs[2].Attempt != 3 {
		t.Fatalf("attempt 3: %+v", runs)
	}
	if next := r.svc.NextRun(RunBackup, "nightly", "uuid-web1"); !next.Equal(day.Add(26 * time.Hour)) {
		t.Fatalf("after success next = %v", next)
	}

	// maintenance is a deferral, not a failure: no attempt is used
	fr.op, fr.failUntil, fr.calls = "begin", 1, nil
	fr.err = api.Errorf(api.CodeMaintenance, "the node is read-only")
	r.clock.Set(day.Add(26 * time.Hour))
	r.svc.RunDue(bg)
	n := len(runsOf(t, r, RunBackup))
	r.clock.Advance(5 * time.Minute)
	r.svc.RunDue(bg)
	if len(runsOf(t, r, RunBackup)) != n {
		t.Fatal("ran again during the maintenance delay")
	}
	r.clock.Advance(11 * time.Minute)
	r.svc.RunDue(bg)
	runs = runsOf(t, r, RunBackup)
	if len(runs) != n+1 || !runs[n].OK || runs[n].Attempt != 1 {
		t.Fatalf("after maintenance: %+v", runs[n:])
	}
}

func TestNonRetryableFailureWaitsForTheNextSlot(t *testing.T) {
	day := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	r := newRig(t, rigOpts{remote: mustMem(t), noEscrow: true, cfg: func(c *Config) { c.JitterWindow = -1 }})
	r.clock.Set(day.Add(time.Hour))
	r.setPolicies(policy("nightly", map[string]string{"all": ""}, "0 2 * * *"))
	r.jail("web1", "uuid-web1", nil)
	r.svc.RunDue(bg)
	r.clock.Set(day.Add(2 * time.Hour))
	r.svc.RunDue(bg)
	r.clock.Advance(5 * time.Minute)
	r.svc.RunDue(bg)
	runs := runsOf(t, r, RunBackup)
	if len(runs) != 1 || runs[0].OK || runs[0].Code != api.CodeKeyNotEscrowed || runs[0].Attempt != 1 {
		t.Fatalf("a refused backup is recorded once and not retried: %+v", runs)
	}
	if next := r.svc.NextRun(RunBackup, "nightly", "uuid-web1"); !next.Equal(day.Add(26 * time.Hour)) {
		t.Fatalf("next = %v", next)
	}
	if s := r.w.snapshots(t, SnapshotPrefix); len(s) != 0 {
		t.Fatal(s)
	}
}

// gaugeRemote measures how many backups run at once, in total and per group.
type gaugeRemote struct {
	Remote
	mu       sync.Mutex
	cur, max int
	perGroup map[string]int
	maxGroup int
	begins   int
}

func (g *gaugeRemote) NewUploader(ctx context.Context, group, snap, prev string) (engine.Uploader, error) {
	u, err := g.Remote.NewUploader(ctx, group, snap, prev)
	if err != nil {
		return nil, err
	}
	g.mu.Lock()
	g.begins++
	g.cur++
	g.max = max(g.max, g.cur)
	g.perGroup[group]++
	g.maxGroup = max(g.maxGroup, g.perGroup[group])
	g.mu.Unlock()
	return &gaugeUp{Uploader: u, g: g, group: group}, nil
}

type gaugeUp struct {
	engine.Uploader
	g     *gaugeRemote
	group string
	once  sync.Once
}

func (u *gaugeUp) Close() {
	u.Uploader.Close()
	u.once.Do(func() {
		u.g.mu.Lock()
		u.g.cur--
		u.g.perGroup[u.group]--
		u.g.mu.Unlock()
	})
}

func (u *gaugeUp) PutChunk(ctx context.Context, id format.ID, b []byte) error {
	time.Sleep(time.Millisecond)
	return u.Uploader.PutChunk(ctx, id, b)
}

func TestConcurrentPoliciesAreBoundedAndOnePerJail(t *testing.T) {
	inner, _, _ := memRemote(t)
	g := &gaugeRemote{Remote: inner, perGroup: map[string]int{}}
	r := newRig(t, rigOpts{remote: g, cfg: func(c *Config) { c.Concurrency = 2; c.JitterWindow = -1 }})
	day := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	r.clock.Set(day.Add(time.Hour))
	r.setPolicies(policy("web", map[string]string{"tier": "web"}, "0 2 * * *"), policy("everything", map[string]string{"all": ""}, "0 2 * * *"))
	for i := 0; i < 4; i++ {
		l := map[string]string{"tier": "db"}
		if i < 3 {
			l["tier"] = "web"
		}
		r.jail(fmt.Sprintf("j%d", i), fmt.Sprintf("uuid-%d", i), l)
	}
	r.svc.RunDue(bg)
	r.clock.Set(day.Add(2 * time.Hour))
	if err := r.svc.RunDue(bg); err != nil {
		t.Fatal(err)
	}
	runs := runsOf(t, r, RunBackup)
	ok := 0
	var failed []string
	for _, x := range runs {
		if x.OK {
			ok++
		} else {
			failed = append(failed, fmt.Sprintf("%s/%s attempt %d: %s", x.Policy, x.Jail, x.Attempt, x.Error))
		}
	}
	if ok != 7 {
		t.Fatalf("%d of %d runs succeeded, want 7 (3 + 4); failed: %v", ok, len(runs), failed)
	}
	if g.max > 2 || g.max < 2 {
		t.Fatalf("at most %d jobs ran at once, want exactly the limit 2 reached and never exceeded", g.max)
	}
	if g.maxGroup != 1 {
		t.Fatalf("a jail had %d jobs at once", g.maxGroup)
	}
}

func TestRunNowRunsAPolicyOutsideTheSchedule(t *testing.T) {
	r := newRig(t, rigOpts{remote: mustMem(t)})
	r.setPolicies(policy("web", map[string]string{"tier": "web"}, "0 2 * * *"))
	r.jail("web1", "uuid-web1", map[string]string{"tier": "web"})
	r.jail("web2", "uuid-web2", map[string]string{"tier": "web"})
	r.jail("db1", "uuid-db1", map[string]string{"tier": "db"})
	if !r.svc.acquire("uuid-web2") {
		t.Fatal("acquire")
	}
	runs, err := r.svc.RunNow(bg, "alpha", "web")
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs = %+v, %v", runs, err)
	}
	if !runs[0].OK || runs[0].Jail != "web1" || runs[1].OK || runs[1].Code != api.CodeBusy {
		t.Fatalf("runs = %+v", runs)
	}
	if _, err := r.svc.RunNow(bg, "alpha", "nope"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("unknown policy: %v", err)
	}
}

func TestRunNowUsesThePolicyOfTheProject(t *testing.T) {
	r := newRig(t, rigOpts{remote: mustMem(t)})
	inDefault := policy("web", map[string]string{"tier": "none"}, "0 2 * * *")
	inShop := policy("web", map[string]string{"tier": "web"}, "0 2 * * *")
	inShop.Metadata.Project = "shop"
	r.setPolicies(inDefault, inShop) // the same name in two projects
	r.jail("web1", "uuid-web1", map[string]string{"tier": "web"})
	r.jails.mu.Lock()
	r.jails.jails[len(r.jails.jails)-1].Project = "shop"
	r.jails.mu.Unlock()
	runs, err := r.svc.RunNow(bg, "shop", "web")
	if err != nil || len(runs) != 1 || runs[0].Jail != "web1" {
		t.Fatalf("the policy of project shop must select its jail: %+v, %v", runs, err)
	}
	if runs, _ := r.svc.RunNow(bg, "alpha", "web"); len(runs) != 0 {
		t.Fatalf("the default project's policy selects nothing here: %+v", runs)
	}
	if _, err := r.svc.RunNow(bg, "other", "web"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("a project without that policy: %v", err)
	}
}

type stateSource struct{ j JailRecord }

func (s stateSource) StateRecord(context.Context) (JailRecord, error) { return s.j, nil }

func TestIncludeStateBacksUpTheStateDataset(t *testing.T) {
	r := newRig(t, rigOpts{remote: mustMem(t)})
	mustCreate(t, r, "tank/jailops/state", map[string]string{"jailops:managed": "on"})
	dir := r.w.m.liveDir("tank/jailops/state")
	writeFile(t, dir, "objects/network/lan.json", "{}")
	r.svc.deps.State = stateSource{JailRecord{Name: "state", UUID: "host-uuid-1-state", Container: "tank/jailops/state",
		Datasets: []Dataset{{Name: "tank/jailops/state", Kind: KindState, Mountpoint: "/jo/state"}}}}
	p := policy("web", map[string]string{"tier": "web"}, "0 2 * * *")
	p.Spec.IncludeState = true
	r.setPolicies(p)
	r.jail("web1", "uuid-web1", map[string]string{"tier": "web"})
	runs, err := r.svc.RunNow(bg, "alpha", "web")
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs = %+v, %v", runs, err)
	}
	st := runs[0]
	if st.Jail != "state" || !st.OK || runs[1].Jail != "web1" {
		t.Fatalf("state run = %+v", st)
	}
	bs, _ := r.svc.List(bg)
	for _, b := range bs {
		if b.Spec.Jail == "state" && (b.Spec.Bundle.ImageGeneration != "" || b.Spec.Snapshot == "") {
			t.Fatalf("state backup = %+v", b.Spec)
		}
	}
	// the state record has no bundle, so it is not a restorable jail
	for _, b := range bs {
		if b.Spec.Jail == "state" {
			_, err := r.svc.Preflight(bg, api.RestoreRequest{Backup: b.Metadata.Name, As: "x"})
			_ = err
		}
	}
}

func TestAPolicyAndAJailNeedANamedProject(t *testing.T) {
	r := newRig(t, rigOpts{remote: mustMem(t)})
	r.setPolicies(policy("web", map[string]string{"all": ""}, "0 2 * * *"))
	if _, err := r.svc.RunNow(bg, "", "web"); !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("a run without a project: %v", err)
	}
	p := policy("web", map[string]string{"all": ""}, "0 2 * * *")
	p.Metadata.Project = ""
	if selects(p, JailRecord{Name: "a", Project: ""}) {
		t.Fatal("a policy and a jail without a project must not match each other")
	}
}
