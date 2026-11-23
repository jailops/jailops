package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"jailops.net/jailops/internal/lifecycle"
	"jailops.net/jailops/internal/schedule"
	"jailops.net/jailops/internal/sys/jailos"
	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/internal/validate"
	"jailops.net/jailops/sdk/api"
)

// Managed updates (design 8.4). The pipeline for one jail is always: policy,
// atomic ZFS snapshot, update, health check, result and history. A failed OS
// update is swapped back by the root-swap plan itself (automatic root
// rollback, application data untouched). A failed package update is
// reported with its snapshot: undoing it is a data rollback (scope all), which
// discards what the jail wrote since, so JailOps never does it on its own.
// UNVERIFIED on FreeBSD: the pkg and apk invocations and the parsing of their
// dry-run output.

// updateState is the in-memory part of the update feature: the latest check
// per jail and when the schedule last ran a jail.
type updateState struct {
	mu      sync.Mutex
	reports map[string]api.UpdateReport
	last    map[string]time.Time
}

func (u *updateState) report(key string) (api.UpdateReport, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	r, ok := u.reports[key]
	return r, ok
}

func (u *updateState) setReport(key string, r api.UpdateReport) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.reports == nil {
		u.reports = map[string]api.UpdateReport{}
	}
	u.reports[key] = r
}

func (u *updateState) forget(key string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.reports, key)
	delete(u.last, key)
}

// ---- history ----

const maxHistory = 100

func (s *Service) historyPath(key string) string {
	return filepath.Join(s.d.StateDir, "updates", key+".json")
}

var histMu sync.Mutex

func (s *Service) readHistory(key string) []api.UpdateRecord {
	if s.d.StateDir == "" {
		s.histMem.mu.Lock()
		defer s.histMem.mu.Unlock()
		return append([]api.UpdateRecord(nil), s.histMem.m[key]...)
	}
	b, err := os.ReadFile(s.historyPath(key))
	if err != nil {
		return nil
	}
	var out []api.UpdateRecord
	_ = json.Unmarshal(b, &out)
	return out
}

func (s *Service) appendHistory(rec api.UpdateRecord) {
	key, _ := validate.QualifiedName(rec.Project, rec.Jail)
	histMu.Lock()
	defer histMu.Unlock()
	h := append(s.readHistory(key), rec)
	if len(h) > maxHistory {
		h = h[len(h)-maxHistory:]
	}
	if s.d.StateDir == "" {
		s.histMem.mu.Lock()
		if s.histMem.m == nil {
			s.histMem.m = map[string][]api.UpdateRecord{}
		}
		s.histMem.m[key] = h
		s.histMem.mu.Unlock()
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.historyPath(key)), 0o750); err != nil {
		s.log.Warn("update history cannot be saved", "jail", key, "err", err)
		return
	}
	b, _ := json.Marshal(h)
	tmp := s.historyPath(key) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o640); err == nil {
		err = os.Rename(tmp, s.historyPath(key))
		if err != nil {
			s.log.Warn("update history cannot be saved", "jail", key, "err", err)
		}
	}
}

type histMemory struct {
	mu sync.Mutex
	m  map[string][]api.UpdateRecord
}

// UpdateHistory returns a jail's update history, newest first.
func (s *Service) UpdateHistory(ctx context.Context, name string) ([]api.UpdateRecord, error) {
	_, key, err := s.jailRef(ctx, name)
	if err != nil {
		return nil, err
	}
	if _, err := s.getByKey(ctx, key); err != nil {
		return nil, err
	}
	h := s.readHistory(key)
	out := make([]api.UpdateRecord, 0, len(h))
	for i := len(h) - 1; i >= 0; i-- {
		out = append(out, h[i])
	}
	return out, nil
}

// ---- parsing ----

var (
	pkgUpgradeRE = regexp.MustCompile(`^\s+(\S+?):\s+(\S+)\s+->\s+(\S+)`)
	apkUpgradeRE = regexp.MustCompile(`Upgrading\s+(\S+)\s+\((\S+)\s+->\s+(\S+)\)`)
)

// parsePackageUpdates reads the dry run of pkg(8) ("pkg upgrade -n") or apk(8)
// ("apk upgrade --simulate"). UNVERIFIED: the exact output formats.
func parsePackageUpdates(out []byte, alpine bool) []api.PackageUpdate {
	var res []api.PackageUpdate
	section := ""
	for _, l := range strings.Split(string(out), "\n") {
		if alpine {
			if m := apkUpgradeRE.FindStringSubmatch(l); m != nil {
				res = append(res, api.PackageUpdate{Name: m[1], From: m[2], To: m[3]})
			}
			continue
		}
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(t, "Installed packages to be UPGRADED"):
			section = "up"
		case strings.HasPrefix(t, "New packages to be INSTALLED"), strings.HasPrefix(t, "Installed packages to be REINSTALLED"),
			strings.HasPrefix(t, "Installed packages to be REMOVED"), strings.HasPrefix(t, "Installed packages to be DOWNGRADED"):
			section = "other"
		case t == "":
			// blank lines separate sections but sections end only at the next header
		case section == "up":
			if m := pkgUpgradeRE.FindStringSubmatch(l); m != nil {
				res = append(res, api.PackageUpdate{Name: m[1], From: m[2], To: m[3]})
			}
		}
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Name < res[j].Name })
	return res
}

// ---- check ----

func (s *Service) jailIsLinux(j api.Jail) bool { return j.Spec.OS == api.OSLinux }

// managesPackages says whether JailOps drives the package tool of a jail: pkg for FreeBSD jails and apk for Alpine.
// A Debian jail (or any other Linux jail) keeps its own packages, for instance with unattended-upgrades, and JailOps
// never runs apt in it; the same goes for what a person runs in the jail on his own.
func (s *Service) managesPackages(j api.Jail) bool {
	if !s.jailIsLinux(j) {
		return true
	}
	ref, err := validate.ParseImageRef(j.Spec.Image)
	if err != nil {
		return false
	}
	spec, err := s.imageSpec(context.Background(), ref.Name)
	return err == nil && spec.Source.Type == "minirootfs"
}

func (s *Service) runInJail(ctx context.Context, key string, timeout time.Duration, argv ...string) ([]byte, error) {
	if s.d.Jails == nil {
		return nil, api.Errorf(api.CodeUnsupported, "this daemon cannot run commands in jails")
	}
	res, err := s.d.Jails.Exec(ctx, key, jailos.ExecOpts{Argv: argv, Timeout: timeout})
	out := append(append([]byte(nil), res.Stdout...), res.Stderr...)
	if err != nil {
		return out, fmt.Errorf("%s: %w: %s", strings.Join(argv, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// pkgExec runs a package command for a running jail and returns its output. A Linux jail runs apk inside itself. A
// FreeBSD jail need not have a package tool of its own (a pkgbase userland without pkg), so the host's pkg runs
// with -j: it enters the jail first, so what it writes and the scripts it runs stay in the jail, and it reads the
// repositories the jail is configured for. argv is the command as it would be typed inside the jail ("pkg",
// "upgrade", "-y") and -j JAIL is put after "pkg".
func (s *Service) pkgExec(ctx context.Context, key string, linux bool, timeout time.Duration, argv ...string) ([]byte, error) {
	if linux {
		return s.runInJail(ctx, key, timeout, argv...)
	}
	host := append([]string{argv[0], "-j", key}, argv[1:]...)
	res, err := s.d.Run.Run(ctx, run.Cmd{Name: host[0], Args: host[1:], Timeout: timeout})
	out := append(append([]byte(nil), res.Stdout...), res.Stderr...)
	if err != nil {
		return out, fmt.Errorf("%s: %w: %s", strings.Join(host, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// pkgUpgradeChunk is how many package names one pkg command takes (the helper allows 64 arguments).
const pkgUpgradeChunk = 50

// pkgUpgrade lists (apply false) or runs (apply true) the upgrade of the packages of a jail. A Linux jail upgrades
// everything with apk. For a FreeBSD jail the host names the installed packages that are not part of the base
// system: the base (FreeBSD-*) comes from the image generation and only moves with it, and a bare `pkg upgrade` on
// the host would also take the base packages from the host's FreeBSD-base repository into the jail's root.
// pkgUpgradeCheck is pkg upgrade -n of the ports packages of a FreeBSD jail; with noUpdate the catalogue is not
// refreshed (-U): what the jail has is read, nothing is written.
func (s *Service) pkgUpgradeCheck(ctx context.Context, key string, noUpdate bool) ([]byte, error) {
	if !noUpdate {
		return s.pkgUpgrade(ctx, key, false, 10*time.Minute, false)
	}
	return s.pkgUpgradeFlags(ctx, key, 10*time.Minute, "-n", "-U")
}

func (s *Service) pkgUpgrade(ctx context.Context, key string, linux bool, timeout time.Duration, apply bool) ([]byte, error) {
	if linux {
		if apply {
			return s.pkgExec(ctx, key, true, timeout, "apk", "upgrade", "--no-interactive")
		}
		return s.pkgExec(ctx, key, true, timeout, "apk", "upgrade", "--simulate", "--no-interactive")
	}
	flag := "-n"
	if apply {
		flag = "-y"
	}
	return s.pkgUpgradeFlags(ctx, key, timeout, flag)
}

// pkgUpgradeFlags runs pkg upgrade with the flags over the ports packages of a FreeBSD jail (every installed package but
// the base ones and pkg itself), in chunks.
func (s *Service) pkgUpgradeFlags(ctx context.Context, key string, timeout time.Duration, flags ...string) ([]byte, error) {
	names, out, err := s.portsPackages(ctx, key)
	if err != nil {
		return out, err
	}
	var all []byte
	for len(names) > 0 {
		n := min(len(names), pkgUpgradeChunk)
		o, err := s.pkgExec(ctx, key, false, timeout, append(append([]string{"pkg", "upgrade"}, flags...), names[:n]...)...)
		all = append(all, o...)
		if err != nil {
			return all, err
		}
		names = names[n:]
	}
	return all, nil
}

// portsPackages names the ports packages installed in a running FreeBSD jail: every package in its package database
// but the base ones (FreeBSD-*) and pkg itself. It reads the database only: no catalogue is fetched.
func (s *Service) portsPackages(ctx context.Context, key string) ([]string, []byte, error) {
	out, err := s.pkgExec(ctx, key, false, 2*time.Minute, "pkg", "query", "%n")
	if err != nil {
		return nil, out, err
	}
	return portsNames(out), out, nil
}

// portsNames picks the ports packages out of `pkg query %n`.
func portsNames(out []byte) []string {
	var names []string
	for _, n := range strings.Fields(string(out)) {
		if !strings.HasPrefix(n, "FreeBSD-") && n != "pkg" {
			names = append(names, n)
		}
	}
	return names
}

func (s *Service) jailRunning(ctx context.Context, key string) bool {
	if s.d.Jails == nil {
		return false
	}
	_, up, err := s.d.Jails.Get(ctx, key)
	return err == nil && up
}

// checkJail asks one jail what could be updated. It changes the package
// catalog inside the jail (pkg update, apk update) and nothing else.
//
// checkJail checks one jail for updates. locked says that the caller holds the jail's lock (the scheduled run); a check
// on request takes it for the base part of a jail, or leaves that part out while an operation has the jail.
func (s *Service) checkJail(ctx context.Context, key string, locked bool) api.UpdateReport {
	j, err := s.jailSnapshot(key)
	rep := api.UpdateReport{Jail: j.Metadata.Name, Project: j.Metadata.Project, Mode: j.Spec.Update.Mode, CheckedAt: s.now().UTC()}
	if err != nil {
		rep.Error = err.Error()
		return rep
	}
	var errs []string
	var held error // a package operation that held the jail when the check asked
	if !s.managesPackages(j) {
		// nothing to check: the jail updates its own packages
	} else if s.jailRunning(ctx, key) {
		alpine := s.jailIsLinux(j)
		upd := []string{"pkg", "update", "-q"}
		if alpine {
			upd = []string{"apk", "update", "--no-interactive"}
		}
		release := func() {}
		if !alpine {
			release, held = s.holdJailFor(key, "a package update", false)
		}
		if held != nil {
			release = func() {} // a refused hold gives no release function
		}
		var none bool // a FreeBSD jail without ports packages: there is nothing to check and no catalogue to fetch
		if held == nil && !alpine {
			if names, _, err := s.portsPackages(ctx, key); err == nil && len(names) == 0 {
				none = true
			}
		}
		if held != nil {
			errs = append(errs, "package catalog: "+held.Error())
		} else if none {
			// no ports packages: the ports catalogue (tens of MiB in the root) is not fetched for nothing
		} else if _, err := s.pkgExec(ctx, key, alpine, 10*time.Minute, upd...); err != nil {
			errs = append(errs, "package catalog: "+err.Error())
		} else if out, err := s.pkgUpgrade(ctx, key, alpine, 10*time.Minute, false); err != nil {
			errs = append(errs, "package check: "+err.Error())
		} else {
			rep.Packages = parsePackageUpdates(out, alpine)
		}
		// the hold ends with the package check: the base check below does not keep an upgrade from the jail
		release()
	} else {
		errs = append(errs, "the jail is stopped; its packages cannot be checked")
	}
	switch {
	case s.upgradesInPlace(j) && held != nil:
		// a package operation holds the jail (an upgrade among them): the base is not checked under it
		errs = append(errs, "base packages: not checked while "+tailText(held.Error(), 200))
	case s.upgradesInPlace(j) && !locked:
		// the dry run of the base reads the root (and mounts a stopped one): it does not run beside an operation on
		// the jail, and it never waits for one (the nightly upgrade is not kept from the jail by a check)
		if unlock, err := s.locks.tryLock("update check of "+key, jailLock(key)); err != nil {
			errs = append(errs, "base packages: not checked: "+tailText(err.Error(), 200))
		} else {
			s.checkBase(ctx, key, j, &rep, &errs)
			unlock()
		}
	default:
		s.checkBase(ctx, key, j, &rep, &errs)
	}
	rep.Available = len(rep.Packages) > 0 || rep.OS != nil
	rep.Error = strings.Join(errs, "; ")
	return rep
}

// checkBase is the OS part of an update check: the newer base packages of a FreeBSD jail.
func (s *Service) checkBase(ctx context.Context, key string, j api.Jail, rep *api.UpdateReport, errs *[]string) {
	if os, err := s.osUpdate(ctx, key, j); err != nil {
		*errs = append(*errs, "base packages: "+err.Error())
	} else {
		rep.OS = os
	}
}

// osUpdate reports the base packages pkg finds newer for a running FreeBSD jail in the repository of its installed
// release (the read-only check of jail upgrade --dry-run, on a copy of the jail's package database). A stopped jail is
// not checked.
func (s *Service) osUpdate(ctx context.Context, key string, j api.Jail) (*api.OSUpdate, error) {
	if s.life == nil || s.jailIsLinux(j) {
		return nil, nil
	}
	if !s.jailRunning(ctx, key) {
		return nil, nil
	}
	up := api.JailUpgrade{DryRun: true, NoPackages: true}
	_, res, err := s.life.PlanUpgrade(ctx, key, lifecycle.UpgradeRequest{JailUpgrade: up, Stamp: s.stamp()})
	if err != nil {
		return nil, err
	}
	if res == nil || len(res.BasePending) == 0 {
		return nil, nil
	}
	return &api.OSUpdate{From: res.Installed, To: fmt.Sprintf("%d newer base packages (jailops jail upgrade %s)", len(res.BasePending), j.Metadata.Name)}, nil
}

// updateTargets resolves the jails of a request to keys, in the request's
// project (a jail the caller cannot see is not_found).
func (s *Service) updateTargets(ctx context.Context, names []string, all bool) (project string, keys []string, err error) {
	p, err := s.access(ctx)
	if err != nil {
		return "", nil, err
	}
	project = p.Meta.Name
	if all {
		s.mu.RLock()
		for _, k := range sortedKeys(s.jails) {
			if s.jails[k].jail.Metadata.Project == project {
				keys = append(keys, k)
			}
		}
		s.mu.RUnlock()
		return project, keys, nil
	}
	if len(names) == 0 {
		return project, nil, invalid("name the jails, or ask for all of them")
	}
	for _, n := range names {
		key, err := validate.QualifiedName(project, n)
		if err != nil {
			return "", nil, notFound("jail", n)
		}
		if _, err := s.getByKey(ctx, key); err != nil {
			return "", nil, err
		}
		keys = append(keys, key)
	}
	return project, keys, nil
}

// CheckUpdates checks jails for updates as an operation; its result is the
// list of reports. Nothing is applied.
func (s *Service) CheckUpdates(ctx context.Context, req api.UpdateCheckRequest) (api.Operation, error) {
	if err := s.requireHost("updates"); err != nil {
		return api.Operation{}, err
	}
	project, keys, err := s.updateTargets(ctx, req.Jails, req.All)
	if err != nil {
		return api.Operation{}, err
	}
	return s.Submit(ctx, OpSpec{Action: api.OpUpdateCheck, Description: "Check for updates in project " + project, Project: project,
		Run: func(ctx context.Context, h *OpHandle) (any, error) {
			out := make([]api.UpdateReport, 0, len(keys))
			for i, k := range keys {
				rep := s.checkJail(ctx, k, false)
				s.upd.setReport(k, rep)
				out = append(out, rep)
				h.Progress((i + 1) * 100 / len(keys))
			}
			s.emitIn(ctx, project, "updates-checked", "/api/v1/updates")
			return out, nil
		}})
}

// ListUpdates returns the latest check result of every visible jail that has
// one, narrowed by the request's project.
func (s *Service) ListUpdates(ctx context.Context) ([]api.UpdateReport, error) {
	all, projects, err := s.visibleProjects(ctx)
	if err != nil {
		return nil, err
	}
	s.upd.mu.Lock()
	defer s.upd.mu.Unlock()
	out := []api.UpdateReport{}
	for _, k := range sortedMapKeys(s.upd.reports) {
		r := s.upd.reports[k]
		if all || containsStr(projects, r.Project) {
			out = append(out, r)
		}
	}
	return out, nil
}

func sortedMapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func containsStr(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// ---- apply ----

type applyWhat struct{ packages, os bool }

// ApplyUpdates applies updates as an operation; its result is the list of
// history records. Jails with nothing to apply are skipped.
func (s *Service) ApplyUpdates(ctx context.Context, req api.UpdateApplyRequest) (api.Operation, error) {
	if err := s.requireHost("updates"); err != nil {
		return api.Operation{}, err
	}
	project, keys, err := s.updateTargets(ctx, req.Jails, req.All)
	if err != nil {
		return api.Operation{}, err
	}
	var locks []string
	for _, k := range keys {
		locks = append(locks, jailLock(k), anchorLock(k))
	}
	return s.Submit(ctx, OpSpec{Action: api.OpUpdateApply, Description: "Apply updates in project " + project, Project: project, Locks: locks,
		Run: func(ctx context.Context, h *OpHandle) (any, error) {
			var out []api.UpdateRecord
			for _, k := range keys {
				out = append(out, s.applyJail(ctx, h, k, applyWhat{req.Packages, req.OS}, false)...)
			}
			if out == nil {
				out = []api.UpdateRecord{}
			}
			return out, nil
		}})
}

// wanted decides what to apply to a jail: what the request restricts to, else
// what its policy allows (a manual policy allows both: the user asked).
func wanted(j api.Jail, w applyWhat, scheduled bool) applyWhat {
	if w.packages || w.os {
		return w
	}
	u := j.Spec.Update
	if scheduled || u.Mode != "" && u.Mode != api.UpdateModeManual {
		return applyWhat{u.Packages, u.OS}
	}
	return applyWhat{true, true}
}

// applyJail runs the pipeline for one jail and returns its history records.
// The caller holds the jail and anchor locks.
func (s *Service) applyJail(ctx context.Context, h *OpHandle, key string, w applyWhat, scheduled bool) []api.UpdateRecord {
	j, err := s.jailSnapshot(key)
	if err != nil || s.hasUnsupportedLayout(key) {
		return nil // a jail with an unsupported root layout is not updated (refuseUnsupported)
	}
	w = wanted(j, w, scheduled)
	project, name := j.Metadata.Project, j.Metadata.Name
	var recs []api.UpdateRecord
	finish := func(r api.UpdateRecord) {
		r.FinishedAt = s.now().UTC()
		r.Jail, r.Project, r.Scheduled = name, project, scheduled
		if r.ID == "" {
			r.ID = "upd-" + s.stamp() + "-" + randHex(3)
		}
		s.appendHistory(r)
		recs = append(recs, r)
		if r.Result == api.UpdateSuccess {
			s.emitIn(ctx, project, "update.applied", "/api/v1/jails/"+name+"/updates")
		} else {
			s.emitIn(ctx, project, "update.failed", "/api/v1/jails/"+name+"/updates")
		}
	}
	// OS first, then the packages.
	if w.os && s.upgradesInPlace(j) && !scheduled && !isGlobalAdmin(ActorFrom(ctx)) {
		// on demand, a jail's base is upgraded in place by a global administrator (as `jail upgrade`); on its schedule,
		// by the jail's own update policy (update.mode managed with update.os)
		if !w.packages {
			finish(api.UpdateRecord{Kind: api.UpdateKindOS, StartedAt: s.now().UTC(), Result: api.UpdateFailure,
				Error: "the base of a jail is upgraded on demand by a global administrator; set update.mode=managed with update.os=true to have it upgraded on the jail's schedule"})
			return recs
		}
		w.os = false
	}
	if w.os && s.upgradesInPlace(j) {
		// a jail is upgraded in place when pkg says a base package is newer (its ports packages are the package
		// update's); nothing newer is no stop, no snapshot and no record
		rec := api.UpdateRecord{Kind: api.UpdateKindOS, StartedAt: s.now().UTC()}
		up := api.JailUpgrade{NoPackages: true}
		_, res, err := s.upgradeRun(ctx, h, project, name, key, up, lifecycle.UpgradeRequest{JailUpgrade: up, Stamp: s.stamp()})
		switch {
		case err != nil && (res == nil || !res.Changed):
			rec.Result, rec.Error, rec.Note = api.UpdateFailure, err.Error(), "the base packages of the jail could not be checked, so nothing was updated"
			finish(rec)
			return recs
		case err != nil:
			rec.From, rec.To, rec.Snapshot = res.From, res.To, res.Snapshot
			rec.Error = err.Error()
			rec.Result, rec.Note = upgradeOutcome(err, res.From)
			finish(rec)
			return recs
		case res != nil && res.Changed:
			rec.From, rec.To, rec.Snapshot, rec.Result = res.From, res.To, res.Snapshot, api.UpdateSuccess
			finish(rec)
		}
		w.os = false
	}
	if w.packages {
		finish(s.applyPackages(ctx, key, j, scheduled))
	}
	return recs
}

// applyPackages updates the packages of a running jail: snapshot, upgrade,
// health check. It never rolls data back on its own.
func (s *Service) applyPackages(ctx context.Context, key string, j api.Jail, scheduled bool) api.UpdateRecord {
	rec := api.UpdateRecord{Kind: api.UpdateKindPackages, StartedAt: s.now().UTC()}
	if !s.managesPackages(j) {
		rec.Result, rec.Note = api.UpdateSuccess, "this jail keeps its own packages (JailOps drives pkg and apk only); nothing was run"
		return rec
	}
	if !s.jailRunning(ctx, key) {
		rec.Result, rec.Error = api.UpdateFailure, "the jail is not running; packages are updated inside it"
		return rec
	}
	alpine := s.jailIsLinux(j)
	// No session may enter the jail while its packages change.
	if !alpine {
		release, err := s.holdJailFor(key, "a package update", false)
		if err != nil {
			rec.Result, rec.Error = api.UpdateFailure, "package catalog: "+err.Error()
			return rec
		}
		defer release()
	}
	if !alpine {
		if names, _, err := s.portsPackages(ctx, key); err == nil && len(names) == 0 {
			rec.Result, rec.Note = api.UpdateSuccess, "no ports packages: nothing to update"
			return rec
		}
	}
	// The packages that will change, from a dry run.
	upd := []string{"pkg", "update", "-q"}
	if alpine {
		upd = []string{"apk", "update", "--no-interactive"}
	}
	if _, err := s.pkgExec(ctx, key, alpine, 10*time.Minute, upd...); err != nil {
		rec.Result, rec.Error = api.UpdateFailure, "package catalog: "+err.Error()
		return rec
	}
	if out, err := s.pkgUpgrade(ctx, key, alpine, 10*time.Minute, false); err == nil {
		rec.Packages = parsePackageUpdates(out, alpine)
		if len(rec.Packages) == 0 {
			rec.Result, rec.Note = api.UpdateSuccess, "nothing to update"
			return rec
		}
	}
	snap, err := s.life.SnapshotJail(ctx, key, "update-"+s.stamp()+"-"+randHex(2), "before package update")
	if err != nil {
		rec.Result, rec.Error = api.UpdateFailure, "snapshot: "+err.Error()
		return rec
	}
	rec.Snapshot = snap.Name
	if _, err := s.pkgUpgrade(ctx, key, alpine, 60*time.Minute, true); err != nil {
		rec.Result, rec.Error = api.UpdateFailure, err.Error()
		rec.Note = "restore the snapshot with `jailops jail rollback " + j.Metadata.Name + " --scope all --snapshot " + snap.Name + "` to undo it; that discards what the jail wrote since"
		return rec
	}
	if !alpine {
		// a program would not find a new library before the library cache is rebuilt
		_, _ = s.runInJail(ctx, key, 5*time.Minute, "/sbin/ldconfig", "-m", "/usr/local/lib")
	}
	if err := s.healthCheck(ctx, key, j); err != nil {
		rec.Result, rec.Error = api.UpdateFailure, err.Error()
		rec.Note = "the packages stay updated: package files live in the jail's own datasets, and a root rollback does not undo them. " +
			"`jailops jail rollback " + j.Metadata.Name + " --scope all --snapshot " + snap.Name + "` does, and discards what the jail wrote since"
		return rec
	}
	rec.Result = api.UpdateSuccess
	// JailOps never stops or restarts a running jail for an update, whatever update.reboot says (auto reports, as
	// manual does): the processes that still run replaced files are named, and the administrator restarts them.
	if pending := s.life.RestartPending(ctx, key); len(pending) > 0 {
		rec.Note = "restart pending: " + strings.Join(pending, ", ") + "; JailOps does not restart the jail: restart these services (or the jail) when you choose"
	} else {
		rec.Note = "no process runs a replaced file; JailOps does not restart the jail"
	}
	return rec
}

func (s *Service) healthCheck(ctx context.Context, key string, j api.Jail) error {
	if !s.jailRunning(ctx, key) {
		return fmt.Errorf("the jail is not running after the update")
	}
	hc := strings.Fields(j.Spec.Update.Healthcheck)
	if len(hc) == 0 {
		return nil
	}
	if out, err := s.runInJail(ctx, key, 5*time.Minute, hc...); err != nil {
		return fmt.Errorf("health check %q failed: %v", strings.Join(hc, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

// ---- schedule ----

// updateLoop runs the update schedules: a jail with mode notify is checked,
// one with managed is checked and updated, at the times of its cron
// expression. A jail is never run twice for the same time, and a busy jail is
// tried again at the next tick.
func (s *Service) updateLoop(ctx context.Context) {
	tick := s.d.UpdateTick
	if tick <= 0 {
		tick = time.Minute
	}
	start := s.now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(tick):
		}
		s.runUpdateSchedules(ctx, start)
	}
}

func (s *Service) runUpdateSchedules(ctx context.Context, start time.Time) {
	now := s.now()
	s.mu.RLock()
	keys := sortedKeys(s.jails)
	jails := make([]api.Jail, len(keys))
	for i, k := range keys {
		jails[i] = cloneJail(s.jails[k].jail)
	}
	s.mu.RUnlock()
	for i, k := range keys {
		j := jails[i]
		u := j.Spec.Update
		if u.Mode != api.UpdateModeNotify && u.Mode != api.UpdateModeManaged || u.Schedule == "" {
			continue
		}
		spec, err := schedule.Parse(u.Schedule)
		if err != nil {
			continue
		}
		s.upd.mu.Lock()
		if s.upd.last == nil {
			s.upd.last = map[string]time.Time{}
		}
		base, ok := s.upd.last[k]
		if !ok {
			base = start
		}
		s.upd.mu.Unlock()
		next, ok := spec.NextWithJitter(base, time.UTC, k, 10*time.Minute)
		if !ok || now.Before(next) {
			continue
		}
		unlock, err := s.locks.tryLock("scheduled update "+k, jailLock(k), anchorLock(k))
		if err != nil {
			continue // busy: the next tick tries again
		}
		s.upd.mu.Lock()
		s.upd.last[k] = now
		s.upd.mu.Unlock()
		s.runScheduled(ctx, k, j, u.Mode == api.UpdateModeManaged)
		unlock()
	}
}

// runScheduled checks a jail and, for managed jails, applies what its policy
// allows. The locks are held; it runs as an operation so that a crash during
// an update is recovered like any other.
func (s *Service) runScheduled(ctx context.Context, key string, j api.Jail, apply bool) {
	rep := s.checkJail(ctx, key, true)
	s.upd.setReport(key, rep)
	if !apply || !rep.Available {
		if rep.Available {
			s.emitIn(ctx, j.Metadata.Project, "update.available", "/api/v1/jails/"+j.Metadata.Name+"/updates")
		}
		return
	}
	op, err := s.Submit(WithActor(ctx, Actor{Name: "updates", Kind: "system", Perm: api.PermAdmin, Global: true}), OpSpec{
		Action: api.OpUpdateScheduled, Description: "Scheduled update of jail " + j.Metadata.Name, Project: j.Metadata.Project,
		Run: func(ctx context.Context, h *OpHandle) (any, error) {
			return s.applyJail(ctx, h, key, applyWhat{}, true), nil
		}})
	if err != nil {
		s.log.Warn("scheduled update could not start", "jail", key, "err", err)
		return
	}
	// the caller releases the jail's locks when this returns, and applyJail works under them: wait for the end, however
	// long (the jails' schedules run one after the other meanwhile)
	if _, err := s.WaitOperation(ctx, op.ID, time.Duration(math.MaxInt64)); err != nil {
		s.log.Warn("scheduled update", "jail", key, "err", err)
	}
}
