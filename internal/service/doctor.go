package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"jailops.net/jailops/internal/apps"
	"jailops.net/jailops/internal/ipam"
	"jailops.net/jailops/internal/plan"
	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/internal/sys/zfs"
	"jailops.net/jailops/sdk/api"
)

type checkSet struct{ list []api.DoctorCheck }

func (c *checkSet) add(name string, st api.DoctorStatus, msg, hint string) {
	c.list = append(c.list, api.DoctorCheck{Name: name, Status: st, Message: msg, Hint: hint})
}

// Doctor runs the diagnostics. Checks that need FreeBSD report "unknown"
// elsewhere; nothing is faked.
func (s *Service) Doctor(ctx context.Context) api.DoctorReport {
	c := &checkSet{}
	s.doctorState(ctx, c)
	s.doctorRelease(ctx, c)
	s.doctorKernel(ctx, c)
	s.doctorSysctl(ctx, c)
	s.doctorHost(ctx, c)
	s.doctorZFS(ctx, c)
	s.doctorVolumes(ctx, c)
	s.doctorPF(ctx, c)
	s.doctorNDPProxy(c)
	s.doctorNDPUplink(ctx, c)
	s.doctorStaticARP(ctx, c)
	s.doctorDNS(c)
	s.doctorRuntime(ctx, c)
	s.doctorAppsLocal(c)
	rep := api.DoctorReport{Time: s.now().UTC(), Host: s.d.Hostname, Checks: c.list, Status: api.DoctorOK}
	rank := map[api.DoctorStatus]int{api.DoctorOK: 0, api.DoctorUnknown: 1, api.DoctorWarn: 2, api.DoctorFail: 3}
	for _, ch := range c.list {
		if rank[ch.Status] > rank[rep.Status] {
			rep.Status = ch.Status
		}
	}
	return rep
}

func (s *Service) doctorState(ctx context.Context, c *checkSet) {
	if rs := s.d.State.Recoveries(); len(rs) > 0 {
		var l []string
		for _, r := range rs {
			l = append(l, r.Path+": "+r.Action)
		}
		c.add("state.files", api.DoctorWarn, "state files were repaired at start: "+strings.Join(l, "; "), "check the .previous copies under the state directory")
	} else {
		c.add("state.files", api.DoctorOK, "state files are intact", "")
	}
	if probs, err := s.d.DB.IntegrityCheck(ctx); err != nil {
		c.add("db.integrity", api.DoctorFail, err.Error(), "")
	} else if len(probs) > 0 {
		c.add("db.integrity", api.DoctorFail, strings.Join(probs, "; "), "restore the database from the latest VACUUM INTO copy")
	} else {
		c.add("db.integrity", api.DoctorOK, "the operational database is healthy", "")
	}
	recs, err := s.journal.List(ctx)
	switch {
	case err != nil:
		c.add("operations.journal", api.DoctorFail, err.Error(), "")
	default:
		var bad []string
		for _, r := range recs {
			if r.State == plan.StateFailed {
				bad = append(bad, r.ID)
			}
		}
		if len(bad) > 0 {
			c.add("operations.journal", api.DoctorFail, "a rollback did not complete for: "+strings.Join(bad, ", "), "inspect the datasets of these jails, then remove the journal record")
		} else if len(recs) > 0 {
			c.add("operations.journal", api.DoctorOK, "operations are in flight", "")
		} else {
			c.add("operations.journal", api.DoctorOK, "no interrupted operations", "")
		}
	}
}

func (s *Service) runOK(ctx context.Context, name string, args ...string) (ok, available bool, out string) {
	if s.d.Run == nil {
		return false, false, ""
	}
	res, err := s.d.Run.Run(ctx, run.Cmd{Name: name, Args: args, Timeout: 10 * time.Second})
	if err == nil {
		return true, true, string(res.Stdout)
	}
	if _, isExit := run.IsExit(err); isExit {
		return false, true, ""
	}
	return false, false, err.Error()
}

func (s *Service) doctorKernel(ctx context.Context, c *checkSet) {
	needLinux := false
	s.mu.RLock()
	for _, e := range s.jails {
		if e.jail.Spec.OS == api.OSLinux {
			needLinux = true
		}
	}
	s.mu.RUnlock()
	mods := []struct {
		name     string
		required bool
	}{{"if_bridge", true}, {"if_epair", true}, {"pf", true}, {"zfs", true}, {"linux64", needLinux}, {"linprocfs", needLinux}, {"linsysfs", needLinux}, {"fdescfs", needLinux}}
	for _, m := range mods {
		ok, avail, _ := s.runOK(ctx, "kldstat", "-q", "-n", m.name)
		switch {
		case !avail && m.name == "if_bridge" && s.bridgeSysctl(ctx):
			c.add("kernel.module."+m.name, api.DoctorOK, "loaded (its sysctl tree exists)", "")
		case !avail:
			c.add("kernel.module."+m.name, api.DoctorUnknown, "cannot be checked here: kldstat is not available to the daemon", "")
		case ok:
			c.add("kernel.module."+m.name, api.DoctorOK, "loaded", "")
		case m.required:
			c.add("kernel.module."+m.name, api.DoctorFail, "not loaded", "kldload "+m.name+" and add it to /boot/loader.conf")
		default:
			c.add("kernel.module."+m.name, api.DoctorOK, "not loaded, not needed", "")
		}
	}
}

func (s *Service) doctorSysctl(ctx context.Context, c *checkSet) {
	nets, _ := s.netKind.list()
	var v4, v6 bool
	for _, n := range nets {
		if n.Spec.IPv4 != nil {
			v4 = true
		}
		if n.Spec.IPv6 != nil && n.Spec.IPv6.Mode != api.IPv6None {
			v6 = true
		}
	}
	racct := false
	s.mu.RLock()
	for _, e := range s.jails {
		r := e.jail.Spec.Resources
		if r.Memory != "" || r.PIDs > 0 || r.CPU > 0 {
			racct = true
		}
	}
	s.mu.RUnlock()
	check := func(name, want string, needed bool, why, hint string) {
		got, err := s.sysctl(ctx, name)
		switch {
		case err != nil:
			c.add("sysctl."+name, api.DoctorUnknown, err.Error(), "")
		case got == want:
			c.add("sysctl."+name, api.DoctorOK, name+"="+got, "")
		case needed:
			if hint == "" {
				hint = "sysctl " + name + "=" + want + " and add it to /etc/sysctl.conf"
			}
			if name == SysctlOnlinkNS { // recommended, not needed to work: a segment whose routers do not probe from off-link addresses is fine without it
				c.add("sysctl."+name, api.DoctorWarn, name+" is "+got+", JailOps recommends "+want+" ("+why+")", hint)
				break
			}
			c.add("sysctl."+name, api.DoctorFail, name+" is "+got+", JailOps needs "+want+" ("+why+")", hint)
		default:
			c.add("sysctl."+name, api.DoctorOK, name+"="+got+" (not needed now)", "")
		}
	}
	check("net.inet6.ip6.forwarding", "1", v6, "jails reach IPv6 through the host", "")
	check("net.inet.ip.forwarding", "1", v4, "jails reach IPv4 through the host", "")
	check("kern.racct.enable", "1", racct, "resource limits use rctl; set it in /boot/loader.conf", "")
	check(SysctlOnlinkNS, "1", s.hasProxyNetwork(), "the routers of a shared IPv6 segment probe the host from off-link addresses", onlinkNSHint)
}

func (s *Service) doctorZFS(ctx context.Context, c *checkSet) {
	if s.d.ZClient == nil {
		c.add("zfs.compat", api.DoctorUnknown, "no ZFS client", "")
		return
	}
	pools, err := s.d.ZClient.ListPools(ctx)
	if err != nil {
		c.add("zfs.pool", api.DoctorUnknown, err.Error(), "")
		return
	}
	for _, si := range s.storageList() {
		pn := poolOf(si.Root)
		if pn == s.d.Pool {
			continue
		}
		for _, p := range pools {
			if p.Name != pn {
				continue
			}
			if p.Health != "ONLINE" {
				c.add("zfs.pool."+pn, api.DoctorFail, "pool "+p.Name+" of storage "+si.Name+" is "+p.Health, "zpool status "+p.Name)
			} else {
				c.add("zfs.pool."+pn, api.DoctorOK, "pool "+p.Name+" of storage "+si.Name+" is ONLINE", "")
			}
		}
	}
	found := false
	for _, p := range pools {
		if p.Name != s.d.Pool {
			continue
		}
		found = true
		if p.Health != "ONLINE" {
			c.add("zfs.pool", api.DoctorFail, "pool "+p.Name+" is "+p.Health, "zpool status "+p.Name)
		} else {
			c.add("zfs.pool", api.DoctorOK, "pool "+p.Name+" is ONLINE", "")
		}
	}
	if !found {
		c.add("zfs.pool", api.DoctorFail, "pool "+s.d.Pool+" does not exist", "")
		return
	}
	active, err := s.d.ZClient.ActiveFeatures(ctx, s.d.Pool)
	if err != nil {
		c.add("zfs.compat", api.DoctorUnknown, err.Error(), "")
		return
	}
	content := ""
	if s.d.CompatFile != "" {
		dir := s.d.CompatDir
		if dir == "" {
			dir = zfs.DefaultCompatDir
		}
		content, _ = zfs.LoadCompat(dir, s.d.CompatFile)
	}
	res := zfs.CheckCompatDetail(active, content)
	switch res.Verdict {
	case zfs.Verified:
		c.add("zfs.compat", api.DoctorOK, "pool features fit "+s.d.CompatFile, "")
	case zfs.No:
		c.add("zfs.compat", api.DoctorFail, "active features not in "+s.d.CompatFile+": "+strings.Join(res.Blocking, ", "), "JailOps never runs zpool upgrade; replication to older hosts will fail")
	default:
		if s.d.CompatFile == "" {
			c.add("zfs.compat", api.DoctorOK, "no replication target configured; features of "+s.d.Pool+" are not compared with an older release", "")
		} else {
			c.add("zfs.compat", api.DoctorUnknown, "the compatibility file "+s.d.CompatFile+" was not found", "")
		}
	}
}

// doctorVolumes finds the volume datasets of jails made before volumes were
// created with canmount=noauto: one with canmount=on is mounted by the boot's
// zfs mount -a, before the jail root it lies in.
func (s *Service) doctorVolumes(ctx context.Context, c *checkSet) {
	if s.d.ZClient == nil {
		return
	}
	var keys []string
	s.mu.RLock()
	for k, e := range s.jails {
		if len(e.jail.Spec.Volumes) > 0 {
			keys = append(keys, k)
		}
	}
	s.mu.RUnlock()
	sort.Strings(keys)
	var fixes []string
	for _, k := range keys {
		parent := s.lcfg.ContainerDS(k) + "/data"
		l, err := s.d.ZClient.List(ctx, zfs.ListOpts{Root: parent, Depth: 1, Types: []string{"filesystem"}, Props: []string{"canmount"}})
		if err != nil {
			continue
		}
		for _, d := range l {
			if d.Name != parent && d.Props["canmount"] == "on" {
				fixes = append(fixes, "zfs set canmount=noauto "+d.Name)
			}
		}
	}
	if len(fixes) == 0 {
		c.add("jail.volumes", api.DoctorOK, "the volume datasets of the jails are mounted with their jails (canmount=noauto)", "")
		return
	}
	c.add("jail.volumes", api.DoctorWarn, fmt.Sprintf("%d volume dataset(s) have canmount=on, so the boot mounts them before the jail root they lie in", len(fixes)),
		strings.Join(fixes, "; "))
}

func (s *Service) doctorPF(ctx context.Context, c *checkSet) {
	if s.d.Run == nil {
		c.add("pf.anchors", api.DoctorUnknown, "no command runner", "")
		return
	}
	_, a1, nat := s.runOK(ctx, "pfctl", "-s", "nat")
	_, a2, rules := s.runOK(ctx, "pfctl", "-s", "rules")
	if !a1 || !a2 {
		c.add("pf.anchors", api.DoctorUnknown, "pfctl is not available here", "")
		return
	}
	hasNat := strings.Contains(nat, `anchor "jailops/*"`)
	hasFilter := strings.Contains(rules, `anchor "jailops/*"`)
	if hasNat && hasFilter {
		c.add("pf.anchors", api.DoctorOK, "the jailops anchors are in the ruleset", "")
		if early := quickPassesBeforeAnchor(rules); len(early) > 0 {
			c.add("pf.order", api.DoctorWarn, "rules of pf.conf that come before the jailops anchor pass traffic for good (quick), so policies cannot filter it: "+strings.Join(early, "; "),
				`move anchor "jailops/*" above them, or accept that this traffic is not filtered by JailOps`)
		} else {
			c.add("pf.order", api.DoctorOK, "no quick pass rule comes before the jailops anchor", "")
		}
		if hasScrub(rules) {
			c.add("pf.scrub", api.DoctorOK, "pf.conf normalises packets (scrub)", "")
		} else {
			c.add("pf.scrub", api.DoctorWarn, "pf.conf has no scrub rule: fragments are not reassembled and TCP MSS is not clamped",
				"add `scrub in all fragment reassemble` (and `max-mss` for a tunnel or a smaller MTU) near the top of /etc/pf.conf; see docs/install.md")
		}
	} else {
		c.add("pf.anchors", api.DoctorFail, "the jailops anchor lines are missing from pf.conf", "add to /etc/pf.conf, and reload it with pfctl -f /etc/pf.conf. pf wants the order: nat-anchor and rdr-anchor with the translation rules, before the first block or pass rule; anchor with the filter rules:\n"+render.AnchorLines())
	}
}

func (s *Service) doctorRuntime(ctx context.Context, c *checkSet) {
	// Rendered versus live, from the reconciler's last pass.
	if s.d.Reconciler != nil {
		rep := s.d.Reconciler.Last()
		switch {
		case rep.At.IsZero():
			c.add("reconcile", api.DoctorUnknown, "no reconcile pass has run yet", "")
		case rep.Err != nil:
			c.add("reconcile", api.DoctorFail, "the last pass failed: "+rep.Err.Error(), "")
		case len(rep.Drift) > 0 || len(rep.Failures) > 0:
			var l []string
			for k, v := range rep.Failures {
				l = append(l, k+": "+v)
			}
			for k, v := range rep.Drift {
				l = append(l, k+": "+strings.Join(v, "; "))
			}
			c.add("reconcile", api.DoctorWarn, strings.Join(l, " | "), "")
		default:
			c.add("reconcile", api.DoctorOK, "rendered and live state agree", "")
		}
	}
	// Stale epairs.
	if s.d.Net != nil {
		known := map[string]bool{}
		s.mu.RLock()
		for _, e := range s.jails {
			known[e.jail.Metadata.UUID] = true
		}
		s.mu.RUnlock()
		ifs, err := s.d.Net.List(ctx)
		if err != nil {
			c.add("network.epairs", api.DoctorUnknown, err.Error(), "")
		} else {
			var stale []string
			for _, i := range ifs {
				if ipam.IsEpairName(i.Name) && strings.HasPrefix(i.Description, "jailops:") && !known[strings.TrimPrefix(i.Description, "jailops:")] {
					stale = append(stale, i.Name)
				}
			}
			if len(stale) > 0 {
				c.add("network.epairs", api.DoctorWarn, "epairs without a jail: "+strings.Join(stale, ", "), "the reconciler removes them on its next pass")
			} else {
				c.add("network.epairs", api.DoctorOK, "no stale epairs", "")
			}
		}
	}
	// Degraded jails.
	var deg []string
	s.mu.RLock()
	for n, e := range s.jails {
		if len(e.problems) > 0 {
			deg = append(deg, n)
		}
	}
	s.mu.RUnlock()
	if len(deg) > 0 {
		c.add("jails.health", api.DoctorWarn, "jails with damaged datasets or records: "+strings.Join(deg, ", "), "")
	} else {
		c.add("jails.health", api.DoctorOK, "all jails are consistent", "")
	}
	if d, err := s.Desired(ctx); err == nil {
		var l []string
		for _, n := range d.Networks {
			if n.Problem != "" {
				l = append(l, "network "+n.Name+": "+n.Problem)
			}
		}
		for _, j := range d.Jails {
			if j.Problem != "" {
				l = append(l, "jail "+j.Name+": "+j.Problem)
			}
		}
		if len(l) > 0 {
			c.add("render", api.DoctorWarn, strings.Join(l, "; "), "")
		} else {
			c.add("render", api.DoctorOK, "all configuration renders", "")
		}
	}
	if o := s.Recovered(); len(o) > 0 {
		var l []string
		for _, r := range o {
			l = append(l, r.Run+": "+r.Action)
		}
		c.add("startup.recovery", api.DoctorWarn, "interrupted operations were handled at start: "+strings.Join(l, "; "), "")
	}
	if s.d.ConfDir != "" {
		if _, err := os.Stat(s.d.ConfDir); err != nil {
			c.add("conf.dir", api.DoctorWarn, err.Error(), "")
		}
	}
}

// bridgeSysctl reports whether the bridge sysctl tree exists, which is how a
// daemon that may not run kldstat can tell that if_bridge is loaded.
func (s *Service) bridgeSysctl(ctx context.Context) bool {
	_, err := s.sysctl(ctx, "net.link.bridge.pfil_member")
	return err == nil
}

// quickPassesBeforeAnchor lists the `pass ... quick` rules, in the loaded ruleset, that stand before the jailops
// anchor and are not tied to an interface that is not a jail's: pf stops at a quick rule, so a jail policy after it
// never sees that traffic. Rules for one named interface (the uplink) are not a problem for traffic of the jails'
// own interfaces and are skipped.
func quickPassesBeforeAnchor(rules string) []string {
	var out []string
	for _, ln := range strings.Split(rules, "\n") {
		ln = strings.TrimSpace(ln)
		if strings.Contains(ln, `anchor "jailops/*"`) {
			break
		}
		if !strings.HasPrefix(ln, "pass ") || !strings.Contains(ln, " quick ") {
			continue
		}
		if strings.Contains(ln, " on ") && !strings.Contains(ln, " on jo") {
			continue
		}
		out = append(out, ln)
		if len(out) == 5 {
			break
		}
	}
	return out
}

// doctorNDPProxy reports the networks in IPv6 proxy mode whose proxy is not answering: the upstream router then cannot
// find their jails, and nothing else says so. A proxy whose capture is still being opened (the reconcile loop started it
// a moment ago) is a warning, not a failure: it opens on its own.
func (s *Service) doctorNDPProxy(c *checkSet) {
	var bad, starting []string
	proxied := 0
	nets, _ := s.netKind.list()
	s.mu.RLock()
	for _, o := range nets {
		if o.Spec.IPv6 == nil || o.Spec.IPv6.Mode != api.IPv6Proxy {
			continue
		}
		proxied++
		answering := false
		reason, msg := "", "no status yet"
		for _, cd := range s.netStat[o.Meta.Name].Conditions {
			if cd.Type == "NDPProxy" {
				answering, reason, msg = cd.Status == api.ConditionTrue, cd.Reason, cd.Message
			}
		}
		switch {
		case answering:
		case reason == "Starting":
			starting = append(starting, o.Meta.Name+": "+msg)
		default:
			bad = append(bad, o.Meta.Name+": "+msg)
		}
	}
	s.mu.RUnlock()
	switch {
	case proxied == 0:
		return
	case len(bad) > 0:
		c.add("network.ndproxy", api.DoctorFail, "the IPv6 proxy is not answering for "+strings.Join(append(bad, starting...), "; "),
			"approve the uplink and the prefix of the jails' addresses with `ndp-proxy = <uplink>=<prefix>` in priv.conf and restart jailops-priv")
	case len(starting) > 0:
		c.add("network.ndproxy", api.DoctorWarn, "the IPv6 proxy is not answering yet for "+strings.Join(starting, "; ")+"; it opens within a few minutes after the daemon starts",
			"run jailops doctor again in a few minutes; see the network's NDPProxy condition")
	default:
		c.add("network.ndproxy", api.DoctorOK, "the IPv6 proxy answers for every proxy-mode network", "")
	}
}

// minHostMajor and minHostMinor are the oldest FreeBSD release JailOps is built and tested for.
const minHostMajor, minHostMinor = 15, 1

// doctorRelease checks the running FreeBSD release. JailOps targets 15.1 and later: it relies on pkgbase, on the
// pf and VNET behaviour of 15.1, and nothing older is tested.
func (s *Service) doctorRelease(ctx context.Context, c *checkSet) {
	r, err := s.sysctl(ctx, "kern.osrelease")
	if err != nil || r == "" {
		c.add("host.release", api.DoctorUnknown, "the FreeBSD release cannot be read here", "")
		return
	}
	var major, minor int
	if _, err := fmt.Sscanf(r, "%d.%d", &major, &minor); err != nil {
		c.add("host.release", api.DoctorUnknown, "the release "+r+" is not one JailOps recognises", "")
		return
	}
	if major < minHostMajor || (major == minHostMajor && minor < minHostMinor) {
		c.add("host.release", api.DoctorFail, "FreeBSD "+r+" is older than the first release JailOps supports, 15.1",
			"upgrade the host to FreeBSD 15.1 or later")
		return
	}
	c.add("host.release", api.DoctorOK, "FreeBSD "+r, "")
}

// doctorAppsLocal checks the host's own app definitions (apps.local_dir)
// when the directory exists: the daemon must be able to read it, or the
// whole catalog listing fails.
func (s *Service) doctorAppsLocal(c *checkSet) {
	dir := s.appsLocalDir()
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return
	}
	es, err := apps.Load(dir)
	if err != nil {
		c.add("apps.local_dir", api.DoctorFail, fmt.Sprintf("%s cannot be read: %v", dir, err),
			"the daemon runs as the user jailops: chmod 0755 "+dir+" and 0644 its files")
		return
	}
	bad := 0
	for _, e := range es {
		if e.Err != nil {
			bad++
		}
	}
	if bad > 0 {
		c.add("apps.local_dir", api.DoctorWarn, fmt.Sprintf("%d of %d definition(s) in %s are invalid", bad, len(es), dir),
			"jailops app list --source local shows the reasons; jailops app validate "+dir+" checks them")
		return
	}
	c.add("apps.local_dir", api.DoctorOK, fmt.Sprintf("%d definition(s) in %s", len(es), dir), "")
}
