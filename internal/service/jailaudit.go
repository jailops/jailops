package service

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"strings"
	"time"

	"jailops.net/jailops/internal/jailaudit"
	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/internal/store"
	"jailops.net/jailops/sdk/api"
)

// auditNote is what the last audit of a jail found, for the doctor.
type auditNote struct {
	critical, warnings int
	ids                []string
}

// hostVulnXML is the host's vulnerability database the audit uses (a variable for tests).
var hostVulnXML = jailaudit.HostVulnXML

// JailAudit answers GET /api/v1/jails/{name}/audit: a read-only audit of a FreeBSD jail, running or stopped
// (docs/audit.md). The root helper reads the jail's root on the host and pkg audit reads a copy of its package
// database (lifecycle.AuditRoot); the jail parameters come from the jail.conf JailOps renders, the pending base
// packages from the last update check. Nothing is changed, nothing runs in the jail, and the jail is neither stopped
// nor started; a stopped jail's root is mounted for the read and unmounted after it. suggest adds the suggestions.
// Operator: the helper reads the tenant's files as root of the host. Audited. An audit with a critical finding sends
// the event jail-audit-critical; the doctor names the jails whose last audit found something (jail.audit).
func (s *Service) JailAudit(ctx context.Context, name string, suggest bool) (api.JailAudit, error) {
	p, key, err := s.jailRef(ctx, name)
	if err != nil {
		return api.JailAudit{}, err
	}
	j, err := s.getByKey(ctx, key)
	if err != nil {
		return api.JailAudit{}, err
	}
	if s.jailIsLinux(j) {
		return api.JailAudit{}, invalid("jail %q is a Linux jail; the audit reads a FreeBSD jail", name)
	}
	// the host's vulnerability database is read as it is: the audit never fetches it
	var vulns jailaudit.VulnAudit
	if fi, err := os.Stat(hostVulnXML); err != nil || !fi.Mode().IsRegular() {
		vulns.NotRun = "the host has no vulnerability database (" + hostVulnXML + ")"
	} else {
		vulns.DBDays = int(s.now().Sub(fi.ModTime()) / (24 * time.Hour))
	}
	// a stopped jail's root is mounted for the read: no operation may start or change the jail meanwhile
	unlock, err := s.locks.lockPatient(ctx, "audit "+key, jailLock(key))
	if err != nil {
		return api.JailAudit{}, err
	}
	defer unlock()
	// the helper's read is bounded (MaxHashTotal); this bounds the time the jail is held whatever happens there
	actx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	ra, running, err := s.life.AuditRoot(actx, key, vulns.NotRun == "")
	if err != nil {
		return api.JailAudit{}, err
	}
	if vulns.NotRun == "" && !ra.VulnRan {
		vulns.NotRun = ra.VulnErr
	}
	vulns.Packages = ra.Vulns
	in := jailaudit.Input{Jail: name, Facts: ra.Facts, HasPkgDB: ra.HasPkgDB, Owned: ra.Owned, Packages: ra.Packages, BasePackages: ra.BasePackages,
		UsedBytes: s.jailsDiskUsed(ctx, []string{key})[key], DiskLimit: j.Spec.Resources.Disk, Running: running, Shell: j.Spec.Shell, Prompt: j.Spec.Prompt,
		Vulns: vulns}
	in.Hostname = j.Spec.Hostname
	if in.Hostname == "" {
		in.Hostname = j.Metadata.Name
	}
	for _, n := range j.Spec.Network {
		for _, a := range []string{n.IPv4, n.IPv6} {
			a, _, _ = strings.Cut(a, "/")
			if ad, err := netip.ParseAddr(a); err == nil {
				in.Addresses = append(in.Addresses, ad.String())
			}
		}
	}
	// the jail parameters as JailOps renders them for jail(8)
	if b, err := s.d.Files.ReadFile(ctx, s.confPath(key)); err != nil {
		in.Params.Err = err.Error()
	} else {
		in.Params = jailaudit.ParseJailConf(string(b))
	}
	if devs := render.SpecDevfsDevices(j.Spec); len(devs) > 0 {
		if n, _, err := render.DevfsRuleset(devs); err == nil {
			in.DevfsOwn = n
		}
	}
	// the base packages as the last update check found them: the audit fetches no catalogue
	if rep, ok := s.upd.report(key); ok {
		in.BaseUpdates = jailaudit.BaseUpdates{Checked: true, At: rep.CheckedAt}
		if rep.OS != nil {
			in.BaseUpdates.Pending = rep.OS.To
		} else {
			// the parts of the check's error that concern the base (checkJail): a stopped jail's base is not checked
			var why []string
			for _, e := range strings.Split(rep.Error, "; ") {
				if strings.HasPrefix(e, "base packages") || strings.Contains(e, "is stopped") {
					why = append(why, e)
				}
			}
			in.BaseUpdates.Err = strings.Join(why, "; ")
		}
	}
	out := api.JailAudit{Jail: name, Running: running, Checks: jailaudit.Evaluate(in, suggest)}
	note := auditNote{}
	for _, c := range out.Checks {
		if c.Suggestion {
			continue
		}
		switch c.Result {
		case api.AuditCritical:
			out.Critical++
			note.ids = append(note.ids, c.ID)
		case api.AuditWarn:
			out.Warnings++
			note.ids = append(note.ids, c.ID)
		}
	}
	note.critical, note.warnings = out.Critical, out.Warnings
	s.auditMu.Lock()
	if s.audits == nil {
		s.audits = map[string]auditNote{}
	}
	s.audits[key] = note
	s.auditMu.Unlock()
	a := ActorFrom(ctx)
	s.Audit(ctx, store.AuditEntry{Actor: a.Name, ActorKind: a.Kind, Method: "GET", Route: "/api/v1/jails/{name}/audit",
		Object: "/api/v1/jails/" + name, Project: p.Meta.Name, Result: "ok", Remote: a.Remote,
		Detail: fmt.Sprintf("jail audit: %d critical, %d warning(s)", out.Critical, out.Warnings)})
	if out.Critical > 0 {
		s.emitIn(ctx, p.Meta.Name, "jail-audit-critical", "/api/v1/jails/"+name)
	}
	return out, nil
}

// doctorAudit names the jails whose last audit since the daemon started found something: a critical finding fails, a
// warning warns. Only the default checks count; a suggestion is never a finding. A jail that was not audited is not
// named; the audit itself runs on demand (jailops jail audit).
func (s *Service) doctorAudit(c *checkSet) {
	s.auditMu.Lock()
	notes := make(map[string]auditNote, len(s.audits))
	for k, v := range s.audits {
		notes[k] = v
	}
	s.auditMu.Unlock()
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, k := range sortedKeys(s.jails) {
		n, ok := notes[k]
		if !ok || n.critical+n.warnings == 0 {
			continue
		}
		e := s.jails[k]
		st := api.DoctorWarn
		if n.critical > 0 {
			st = api.DoctorFail
		}
		c.add("jail.audit", st, fmt.Sprintf("jail %s/%s: its last audit found %d critical and %d warning(s) (%s)", e.jail.Metadata.Project, e.jail.Metadata.Name, n.critical, n.warnings, strings.Join(n.ids, ", ")),
			fmt.Sprintf("jailops jail audit %s --project %s shows each finding and its fix", e.jail.Metadata.Name, e.jail.Metadata.Project))
	}
}
