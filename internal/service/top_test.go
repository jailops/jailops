package service_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"jailops.net/jailops/internal/daemon"
	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/ipam"
	"jailops.net/jailops/internal/service"
	"jailops.net/jailops/sdk/api"
)

// hostTools stands for sysctl, netstat and ps of the host: what each prints, and how often each was called.
type hostTools struct {
	mu      sync.Mutex
	out     map[string]string
	err     map[string]error
	calls   map[string]int
	block   map[string]bool
	psByJID map[string]string
}

func newHostTools() *hostTools {
	return &hostTools{out: map[string]string{}, err: map[string]error{}, calls: map[string]int{}, block: map[string]bool{}, psByJID: map[string]string{}}
}

func (h *hostTools) set(name, out string) {
	h.mu.Lock()
	h.out[name] = out
	h.mu.Unlock()
}

func (h *hostTools) count(name string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls[name]
}

func (h *hostTools) exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	h.mu.Lock()
	h.calls[name]++
	block, err, out := h.block[name], h.err[name], h.out[name]
	if name == "ps" {
		for i, a := range args {
			if a == "-J" && i+1 < len(args) {
				if v, ok := h.psByJID[args[i+1]]; ok {
					out = v
				}
			}
		}
	}
	h.mu.Unlock()
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return []byte(out), err
}

// topEnv makes an environment whose clock the test moves and whose host tools it scripts.
func topEnv(t *testing.T) (*dtest.Env, *service.Service, *hostTools, func(time.Duration)) {
	t.Helper()
	tools := newHostTools()
	var mu sync.Mutex
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	e := dtest.New(t, dtest.Options{Now: clock, Clients: func(c *daemon.Clients) { c.HostExec = tools.exec }})
	e.Seed()
	return e, e.D.Service(), tools, advance
}

func startJail(t *testing.T, e *dtest.Env, s *service.Service, project string, j api.Jail) {
	t.Helper()
	ctx := as(globalAdmin, project)
	op, err := s.CreateJail(ctx, j)
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, s, op); got.Status != api.OpSuccess {
		t.Fatalf("create: %+v\n%s", got.Error, e.Log())
	}
	op, err = s.JailAction(ctx, j.Metadata.Name, api.JailAction{Action: "start"})
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, s, op); got.Status != api.OpSuccess {
		t.Fatalf("start: %+v\n%s", got.Error, e.Log())
	}
}

const sysctlText = "vm.loadavg={ 0.50 0.40 0.30 }\nkern.cp_time=%d 0 %d 0 %d\nhw.ncpu=4\nhw.physmem=4294967296\nvm.stats.vm.v_wire_count=100000\nvm.stats.vm.v_active_count=24144\nvm.stats.vm.v_page_size=4096\nkstat.zfs.misc.arcstats.size=1048576\n"

func netstatFor(host string, in, out uint64) string {
	return fmt.Sprintf("Name Mtu Network Address Ipkts Ierrs Idrop Ibytes Opkts Oerrs Obytes Coll\n%s 1500 <Link#9> 02:aa:bb:cc:dd:ee 5 0 0 %d 6 0 %d 0\nlo0 16384 <Link#2> lo0 1 0 0 9 1 0 9 0\n", host, in, out)
}

// With racct on the numbers of a jail are the kernel's: pcpu, memoryuse and maxproc of `rctl -u`, and the limits of
// the spec come with them. The network rates compare two samples, so the first frame has none.
func TestTopMeasuresRunningJails(t *testing.T) {
	e, s, tools, advance := topEnv(t)
	web := dtest.WebJail("web")
	web.Spec.Resources = api.Resources{CPU: 2, Memory: "2G", Disk: "10G"}
	startJail(t, e, s, dtest.SeedProject, web)
	got, err := s.GetJail(as(globalAdmin, dtest.SeedProject), "web")
	if err != nil {
		t.Fatal(err)
	}
	epair, _, err := ipam.EpairNames(got.Metadata.UUID, 0)
	if err != nil {
		t.Fatal(err)
	}
	e.Run.On("rctl", []string{"-u", "jail:alpha_web"}, "cputime=297\nmemoryuse=407048192\nmaxproc=23\npcpu=137\nswapuse=1\n")
	tools.set("sysctl", fmt.Sprintf(sysctlText, 1000, 500, 8500))
	tools.set("netstat", netstatFor(epair, 1_000, 5_000))

	g := as(globalAdmin, "")
	f1, err := s.Top(g, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f1.IntervalSeconds != 0 || len(f1.Jails) != 1 {
		t.Fatalf("the first frame has nothing to compare with: %+v", f1)
	}
	j := f1.Jails[0]
	if j.Project != dtest.SeedProject || j.Name != "web" || j.CPUPercent == nil || *j.CPUPercent != 137 || j.MemoryBytes == nil || *j.MemoryBytes != 407048192 ||
		j.Processes == nil || *j.Processes != 23 || j.Approximate {
		t.Fatalf("%+v", j)
	}
	if j.CPULimitPercent != 200 || j.MemoryLimitBytes != 2<<30 || j.DiskQuotaBytes != 10<<30 {
		t.Fatalf("limits: %+v", j)
	}
	if j.NetInBytesPerSecond != nil || j.NetOutBytesPerSecond != nil {
		t.Fatalf("a rate needs two samples: %+v", j)
	}
	if f1.Host == nil || f1.Host.CPUs != 4 || f1.Host.Load1 != 0.5 || f1.Host.RunningJails != 1 || f1.Host.MemoryUsed == nil ||
		*f1.Host.MemoryUsed != (100000+24144)*4096 || f1.Host.ARCBytes == nil || f1.Host.CPUPercent != nil {
		t.Fatalf("host: %+v", f1.Host)
	}
	if len(f1.Notes) != 0 {
		t.Fatalf("racct is on: %v", f1.Notes)
	}

	// two seconds later the jail's interface moved 2000 bytes towards the jail and 10000 away from it; the host end
	// receives what the jail sends
	advance(2 * time.Second)
	tools.set("netstat", netstatFor(epair, 1_000+10_000, 5_000+2_000))
	tools.set("sysctl", fmt.Sprintf(sysctlText, 1100, 600, 8800))
	f2, err := s.Top(g, nil)
	if err != nil {
		t.Fatal(err)
	}
	j = f2.Jails[0]
	if f2.IntervalSeconds != 2 || j.NetInBytesPerSecond == nil || *j.NetInBytesPerSecond != 1000 || j.NetOutBytesPerSecond == nil || *j.NetOutBytesPerSecond != 5000 {
		t.Fatalf("rates: %+v %+v", f2.IntervalSeconds, j)
	}
	if f2.Host.CPUPercent == nil || *f2.Host.CPUPercent != 40 {
		t.Fatalf("host cpu: %+v", f2.Host.CPUPercent)
	}
	if j.UptimeSeconds <= 0 {
		t.Fatalf("uptime: %d", j.UptimeSeconds)
	}
}

// Without racct the numbers come from the process list, say so, and the frame says it once.
func TestTopWithoutRacctUsesTheProcessList(t *testing.T) {
	e, s, tools, _ := topEnv(t)
	startJail(t, e, s, dtest.SeedProject, dtest.WebJail("web"))
	e.SetSysctl("kern.racct.enable", "0")
	tools.set("sysctl", fmt.Sprintf(sysctlText, 1, 1, 1))
	jid := ""
	list, _ := e.Jails.List(context.Background())
	for _, j := range list {
		if j.Name == "alpha_web" {
			jid = fmt.Sprint(j.JID)
		}
	}
	if jid == "" {
		t.Fatal("the jail does not run")
	}
	tools.mu.Lock()
	tools.psByJID[jid] = " 1.5 1024\n 2.0 2048\n"
	tools.mu.Unlock()
	f, err := s.Top(as(globalAdmin, ""), nil)
	if err != nil {
		t.Fatal(err)
	}
	j := f.Jails[0]
	if !j.Approximate || j.CPUPercent == nil || *j.CPUPercent != 3.5 || j.MemoryBytes == nil || *j.MemoryBytes != 3072*1024 || j.Processes == nil || *j.Processes != 2 {
		t.Fatalf("%+v", j)
	}
	if !slices.ContainsFunc(f.Notes, func(n string) bool { return strings.HasPrefix(n, "racct is off: cpu and memory are approximate") }) {
		t.Fatalf("notes: %v", f.Notes)
	}
	for _, c := range e.Run.Argv() {
		if strings.HasPrefix(c, "rctl") {
			t.Fatalf("rctl ran although racct is off: %s", c)
		}
	}
}

// A reading that does not answer in time leaves its field out and says why; the frame still comes.
func TestTopSlowToolsGiveNA(t *testing.T) {
	defer service.SetTopCallTimeout(service.SetTopCallTimeout(150 * time.Millisecond))
	e, s, tools, _ := topEnv(t)
	startJail(t, e, s, dtest.SeedProject, dtest.WebJail("web"))
	e.Run.On("rctl", []string{"-u", "jail:alpha_web"}, "maxproc=3\nmemoryuse=1\npcpu=1\n")
	tools.mu.Lock()
	tools.block["netstat"], tools.block["sysctl"] = true, true
	tools.mu.Unlock()
	start := time.Now()
	f, err := s.Top(as(globalAdmin, ""), nil)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("a tool that does not answer held the frame for %v", time.Since(start))
	}
	if len(f.Jails) != 1 || f.Jails[0].NetInBytesPerSecond != nil || f.Host != nil {
		t.Fatalf("%+v host %+v", f.Jails, f.Host)
	}
	if !slices.ContainsFunc(f.Notes, func(n string) bool { return strings.Contains(n, "network counters could not be read") }) ||
		!slices.ContainsFunc(f.Notes, func(n string) bool { return strings.Contains(n, "host numbers could not be read") }) {
		t.Fatalf("notes: %v", f.Notes)
	}
}

// Who sees what: the host header is for a global identity, a project identity sees the jails of its projects, a
// selected project narrows a global identity, and labels narrow the jails.
func TestTopVisibility(t *testing.T) {
	e, s, tools, _ := topEnv(t)
	web := dtest.WebJail("web")
	web.Metadata.Labels = map[string]string{"role": "web"}
	startJail(t, e, s, dtest.SeedProject, web)
	startJail(t, e, s, dtest.SeedProject, dtest.WebJail("db"))
	makeProject(t, e, "beta")
	net := "lan-beta"
	startJail(t, e, s, "beta", dtest.WebJailOn("mail", net))
	tools.set("sysctl", fmt.Sprintf(sysctlText, 1, 1, 1))
	for _, k := range []string{"alpha_web", "alpha_db", "beta_mail"} {
		e.Run.On("rctl", []string{"-u", "jail:" + k}, "maxproc=1\nmemoryuse=1\npcpu=1\n")
	}

	names := func(f api.Top) []string {
		var out []string
		for _, j := range f.Jails {
			out = append(out, j.Project+"/"+j.Name)
		}
		return out
	}
	f, err := s.Top(as(globalAdmin, ""), nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"alpha/db", "alpha/web", "beta/mail"}; !slices.Equal(names(f), want) || f.Host == nil || f.Host.RunningJails != 3 {
		t.Fatalf("global: %v host %+v", names(f), f.Host)
	}
	f, err = s.Top(as(globalAdmin, "beta"), nil)
	if err != nil || !slices.Equal(names(f), []string{"beta/mail"}) || f.Host == nil {
		t.Fatalf("a global identity in one project: %v %v host %+v", names(f), err, f.Host)
	}
	f, err = s.Top(as(alphaOnly, ""), nil)
	if err != nil || !slices.Equal(names(f), []string{"alpha/db", "alpha/web"}) || f.Host != nil {
		t.Fatalf("a project identity: %v %v host %+v", names(f), err, f.Host)
	}
	if _, err := s.Top(as(alphaOnly, "beta"), nil); err == nil {
		t.Fatal("a project the identity has no scope in")
	}
	if _, err := s.Top(as(nobody, ""), nil); err == nil {
		t.Fatal("an identity without scope sees nothing")
	}
	f, err = s.Top(as(globalAdmin, ""), map[string]string{"role": "web"})
	if err != nil || !slices.Equal(names(f), []string{"alpha/web"}) {
		t.Fatalf("labels: %v %v", names(f), err)
	}
}

// One sample serves every caller: callers that arrive together wait for the one being taken, and a frame younger
// than a second is reused; an older one is measured again. Nothing runs between frames.
func TestTopSharesTheSample(t *testing.T) {
	e, s, tools, advance := topEnv(t)
	startJail(t, e, s, dtest.SeedProject, dtest.WebJail("web"))
	e.Run.On("rctl", []string{"-u", "jail:alpha_web"}, "maxproc=1\nmemoryuse=1\npcpu=1\n")
	tools.set("sysctl", fmt.Sprintf(sysctlText, 1, 1, 1))
	tools.set("netstat", "")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Top(as(globalAdmin, ""), nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := tools.count("sysctl"); n != 1 {
		t.Fatalf("eight callers took %d samples", n)
	}
	if _, err := s.Top(as(globalAdmin, ""), nil); err != nil || tools.count("sysctl") != 1 {
		t.Fatalf("a frame of the same second is reused: %d samples (%v)", tools.count("sysctl"), err)
	}
	advance(1500 * time.Millisecond)
	if _, err := s.Top(as(globalAdmin, ""), nil); err != nil || tools.count("sysctl") != 2 {
		t.Fatalf("an older frame is measured again: %d samples (%v)", tools.count("sysctl"), err)
	}
	// after a long quiet the rates start over: nothing to compare with
	advance(10 * time.Minute)
	f, err := s.Top(as(globalAdmin, ""), nil)
	if err != nil || f.IntervalSeconds != 0 || tools.count("sysctl") != 3 {
		t.Fatalf("after a quiet minute: interval %v, %d samples, %v", f.IntervalSeconds, tools.count("sysctl"), err)
	}
}

// A host that cannot be read still answers: the jails come without the numbers that need the tools.
func TestTopWithoutHostTools(t *testing.T) {
	e := dtest.New(t, dtest.Options{})
	e.Seed()
	s := e.D.Service()
	startJail(t, e, s, dtest.SeedProject, dtest.WebJail("web"))
	e.Run.On("rctl", []string{"-u", "jail:alpha_web"}, "maxproc=2\nmemoryuse=4096\npcpu=5\n")
	f, err := s.Top(as(globalAdmin, ""), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Jails) != 1 || f.Jails[0].Processes == nil || *f.Jails[0].Processes != 2 || f.Host != nil {
		t.Fatalf("%+v host %+v", f.Jails, f.Host)
	}
}
