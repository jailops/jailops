package service

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"jailops.net/jailops/internal/ipam"
	"jailops.net/jailops/internal/sys/jailos"
	"jailops.net/jailops/internal/top"
	"jailops.net/jailops/internal/validate"
	"jailops.net/jailops/sdk/api"
)

// jailops top: one sample of the host and of every running jail, taken when a frame is asked for and shared by
// every caller. There is no loop that runs by itself: a frame younger than topMinAge is reused, a sample being
// taken is waited for, and the rates of a frame compare it with the sample before it, so a host nobody watches
// does no work at all. A reading that does not come back within topCallTimeout leaves its field out of the frame.
const (
	topMinAge     = time.Second
	topStaleAfter = time.Minute      // an older sample has no rates to give
	topDiskEvery  = 10 * time.Second // ZFS is read this often, not at every frame
	topParallel   = 4                // jails read at the same time
)

// topCallTimeout bounds one helper or tool call (a variable so that a test need not wait for it).
var topCallTimeout = 3 * time.Second

// topJailRaw is the reading of one running jail.
type topJailRaw struct {
	project, name, key string
	labels             map[string]string
	started            time.Time

	cpu                 *float64 // percent of one core
	rss                 *int64
	procs               *int
	approximate         bool
	haveNet             bool
	netIn, netOut       uint64 // counters, all the jail's interfaces added up
	cpuLimit            int
	memLimit, diskQuota int64
	disk                *int64
}

// topSample is one reading of everything; a frame is made from it.
type topSample struct {
	at    time.Time
	host  top.Host
	haveH bool
	jails map[string]*topJailRaw // by jail key
	notes []string
	// running is the number of running jails, in every project.
	running int
	pool    *topPool
}

// topPool is the capacity of the pool that holds the JailOps datasets.
type topPool struct {
	name                  string
	size, allocated, free uint64
}

// topState is what the service keeps between frames.
type topState struct {
	mu       sync.Mutex
	last     *topSample // the newest
	prev     *topSample // the one before it, what the rates of last compare with
	inflight chan struct{}
	diskAt   time.Time
	disk     map[string]int64
	pool     *topPool
}

// Top answers the live usage of the running jails the caller may see (the selected project, or every project the
// identity has) and, for a global identity, of the host. labels narrows the jails.
func (s *Service) Top(ctx context.Context, labels map[string]string) (api.Top, error) {
	all, projects, err := s.visibleProjects(ctx)
	if err != nil {
		return api.Top{}, err
	}
	cur, prev, err := s.topSampleNow(ctx)
	if err != nil {
		return api.Top{}, err
	}
	return s.topFrame(cur, prev, ActorFrom(ctx).Global, func(project string, l map[string]string) bool {
		return (all || slices.Contains(projects, project)) && MatchLabels(l, labels)
	}), nil
}

// topSampleNow returns the newest sample and the one before it, taking a new sample when the newest is older than
// topMinAge. Callers that arrive while one is being taken wait for it.
func (s *Service) topSampleNow(ctx context.Context) (cur, prev *topSample, err error) {
	st := &s.topSt
	for {
		st.mu.Lock()
		if st.last != nil && s.now().Sub(st.last.at) < topMinAge {
			cur, prev = st.last, st.prev
			st.mu.Unlock()
			return cur, prev, nil
		}
		if st.inflight != nil {
			wait := st.inflight
			st.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		}
		done := make(chan struct{})
		st.inflight = done
		last := st.last
		st.mu.Unlock()

		// The sample is taken for everybody: it does not stop when the caller that started it goes away.
		smp := s.takeTopSample(s.baseCtx)

		st.mu.Lock()
		if last != nil && smp.at.Sub(last.at) <= topStaleAfter {
			st.prev = last
		} else {
			st.prev = nil
		}
		st.last = smp
		st.inflight = nil
		cur, prev = st.last, st.prev
		st.mu.Unlock()
		close(done)
		return cur, prev, nil
	}
}

// takeTopSample reads the host and every running jail.
func (s *Service) takeTopSample(ctx context.Context) *topSample {
	smp := &topSample{at: s.now().UTC(), jails: map[string]*topJailRaw{}}
	running := s.runningJails(ctx)

	type entry struct {
		j   api.Jail
		key string
		jid int
	}
	var list []entry
	s.mu.RLock()
	for _, k := range sortedKeys(s.jails) {
		e := s.jails[k]
		key := jailKey(e.jail)
		jid, up := running[key]
		if !up {
			continue
		}
		raw := &topJailRaw{project: e.jail.Metadata.Project, name: e.jail.Metadata.Name, key: key,
			labels: e.jail.Metadata.Labels, started: s.status[key].startedAt}
		r := e.jail.Spec.Resources
		raw.cpuLimit = r.CPU * 100
		if r.Memory != "" {
			if n, err := validate.ParseSize(r.Memory); err == nil {
				raw.memLimit = int64(n)
			}
		}
		if r.Disk != "" {
			if n, err := validate.ParseSize(r.Disk); err == nil {
				raw.diskQuota = int64(n)
			}
		}
		smp.jails[key] = raw
		list = append(list, entry{j: cloneJail(e.jail), key: key, jid: jid})
	}
	s.mu.RUnlock()
	smp.running = len(running)

	// racct is read once for everybody
	racct := false
	if v, err := s.sysctl(ctx, "kern.racct.enable"); err == nil {
		racct = v == "1"
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, topParallel)
	var mu sync.Mutex
	failures := 0
	firstFail := ""
	for _, en := range list {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			raw := smp.jails[en.key]
			cctx, cancel := context.WithTimeout(ctx, topCallTimeout)
			defer cancel()
			var err error
			if racct && s.d.Run != nil {
				var u jailos.Usage
				if u, err = jailos.ReadUsage(cctx, s.d.Run, en.key); err == nil {
					cpu := float64(u.Counters["pcpu"])
					p, m := u.Processes, u.MemoryBytes
					raw.cpu, raw.procs, raw.rss = &cpu, &p, &m
				}
			} else if s.d.HostExec != nil {
				raw.approximate = true
				var out []byte
				if out, err = s.d.HostExec(cctx, "ps", "-ax", "-J", strconv.Itoa(en.jid), "-o", "pcpu=,rss="); err == nil {
					n, cpu, rss := top.ParsePS(string(out))
					raw.cpu, raw.procs, raw.rss = &cpu, &n, &rss
				}
			}
			if err != nil {
				mu.Lock()
				failures++
				if firstFail == "" {
					firstFail = err.Error()
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	switch {
	case !racct && len(list) > 0:
		smp.notes = append(smp.notes, "racct is off: cpu and memory are approximate (from the process list); kern.racct.enable=1 in /boot/loader.conf and a reboot give exact numbers")
	}
	if failures > 0 {
		smp.notes = append(smp.notes, fmt.Sprintf("cpu and memory of %d jail(s) could not be read: %s", failures, firstFail))
	}

	// network: one netstat for every interface
	if s.d.HostExec != nil && len(list) > 0 {
		nctx, cancel := context.WithTimeout(ctx, topCallTimeout)
		out, err := s.d.HostExec(nctx, "netstat", "-ibnW", "-f", "link")
		cancel()
		if err != nil {
			smp.notes = append(smp.notes, "network counters could not be read: "+err.Error())
		} else {
			ifs := top.ParseNetstat(string(out))
			for _, en := range list {
				raw := smp.jails[en.key]
				for i := range en.j.Spec.Network {
					host, _, err := ipam.EpairNames(en.j.Metadata.UUID, i)
					if err != nil {
						continue
					}
					if c, ok := ifs[host]; ok {
						// the host end of the epair: what it receives is what the jail sends
						raw.haveNet = true
						raw.netIn += c.OutBytes
						raw.netOut += c.InBytes
					}
				}
			}
		}
	}

	// disk: ZFS is read every topDiskEvery, not at every frame
	if len(list) > 0 {
		st := &s.topSt
		st.mu.Lock()
		stale := st.disk == nil || smp.at.Sub(st.diskAt) >= topDiskEvery
		st.mu.Unlock()
		if stale {
			keys := make([]string, 0, len(list))
			for _, en := range list {
				keys = append(keys, en.key)
			}
			dctx, cancel := context.WithTimeout(ctx, 2*topCallTimeout)
			used := s.jailsDiskUsed(dctx, keys)
			pool := s.topReadPool(dctx)
			cancel()
			st.mu.Lock()
			st.disk, st.diskAt, st.pool = used, smp.at, pool
			st.mu.Unlock()
		}
		st.mu.Lock()
		for k, raw := range smp.jails {
			if n, ok := st.disk[k]; ok {
				v := n
				raw.disk = &v
			}
		}
		smp.pool = st.pool
		st.mu.Unlock()
	}

	// the host
	if s.d.HostExec != nil {
		hctx, cancel := context.WithTimeout(ctx, topCallTimeout)
		args := append([]string{"-e"}, top.SysctlNames...)
		out, err := s.d.HostExec(hctx, "sysctl", args...)
		cancel()
		// sysctl exits 1 when one name is missing and still prints the others: the output is read either way
		if h, perr := top.ParseSysctl(string(out), smp.at); perr == nil {
			smp.host, smp.haveH = h, true
		} else if err != nil {
			smp.notes = append(smp.notes, "the host numbers could not be read: "+err.Error())
		}
	}
	return smp
}

// topFrame makes the frame of one caller from a sample and the one before it.
func (s *Service) topFrame(cur, prev *topSample, host bool, visible func(project string, labels map[string]string) bool) api.Top {
	f := api.Top{MeasuredAt: cur.at, Jails: []api.TopJail{}}
	var dt time.Duration
	if prev != nil {
		dt = cur.at.Sub(prev.at)
		if dt > 0 {
			f.IntervalSeconds = dt.Seconds()
		}
	}
	keys := make([]string, 0, len(cur.jails))
	for k := range cur.jails {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	approx := false
	for _, k := range keys {
		r := cur.jails[k]
		if !visible(r.project, r.labels) {
			continue
		}
		j := api.TopJail{Project: r.project, Name: r.name, CPULimitPercent: r.cpuLimit, MemoryLimitBytes: r.memLimit,
			DiskQuotaBytes: r.diskQuota, CPUPercent: r.cpu, MemoryBytes: r.rss, Processes: r.procs, DiskBytes: r.disk,
			Approximate: r.approximate}
		if !r.started.IsZero() {
			if d := cur.at.Sub(r.started); d > 0 {
				j.UptimeSeconds = int64(d.Seconds())
			}
		}
		if r.haveNet && prev != nil {
			if p, ok := prev.jails[k]; ok && p.haveNet {
				if v, ok := top.Rate(p.netIn, r.netIn, dt); ok {
					j.NetInBytesPerSecond = &v
				}
				if v, ok := top.Rate(p.netOut, r.netOut, dt); ok {
					j.NetOutBytesPerSecond = &v
				}
			}
		}
		approx = approx || r.approximate
		f.Jails = append(f.Jails, j)
	}
	f.Notes = append(f.Notes, cur.notes...)
	if !approx {
		f.Notes = slices.DeleteFunc(f.Notes, func(n string) bool { return strings.HasPrefix(n, "racct is off") })
	}
	if host && cur.haveH {
		h := cur.host
		th := &api.TopHost{Load1: h.Load[0], Load5: h.Load[1], Load15: h.Load[2], CPUs: h.NCPU, MemoryTotal: h.PhysMem, RunningJails: cur.running}
		if prev != nil && prev.haveH {
			if v, ok := top.CPUBusy(prev.host, h); ok {
				th.CPUPercent = &v
			}
		}
		if h.HaveMem {
			used := (h.Wired + h.Active) * h.PageSize
			th.MemoryUsed = &used
		}
		if h.HaveARC {
			arc := h.ARC
			th.ARCBytes = &arc
		}
		if p := cur.pool; p != nil {
			size, alloc, free := p.size, p.allocated, p.free
			th.Pool, th.PoolSize, th.PoolAllocated, th.PoolFree = p.name, &size, &alloc, &free
		}
		f.Host = th
	}
	return f
}

// topReadPool reads the pool of the JailOps datasets with the ZFS client the resources route uses; nil when it
// cannot be read, and the header goes without it.
func (s *Service) topReadPool(ctx context.Context) *topPool {
	if s.d.ZClient == nil {
		return nil
	}
	pools, err := s.d.ZClient.ListPools(ctx)
	if err != nil {
		return nil
	}
	for _, p := range pools {
		if p.Name == s.d.Pool {
			return &topPool{name: p.Name, size: p.Size, allocated: p.Allocated, free: p.Free}
		}
	}
	return nil
}
