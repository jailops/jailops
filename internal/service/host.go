package service

import (
	"context"
	"slices"
	"strings"
	"time"

	"jailops.net/jailops/sdk/api"
)

// Host management (appliance mode, docs/appliance.md). The daemon changes the host itself only for the modules
// listed in host.manage.modules of jailopsd.yaml, and the helper only for those listed in host-manage of priv.conf:
// both must name a module before anything is written. Reading works always.

// HostSystem is the privileged side of host management: the helper in production. It reads and renders the host
// files itself; the daemon sends validated structured fields, never a file.
type HostSystem interface {
	// HostIdentityRead reads the identity of the host. It changes nothing and needs no admin assertion.
	HostIdentityRead(ctx context.Context) (HostIdentityState, error)
	// HostIdentityApply writes the parts of spec that are given (after a snapshot of the files it changes) or,
	// with dryRun, only says what it would do.
	HostIdentityApply(ctx context.Context, spec api.HostIdentitySpec, dryRun bool) (api.HostChange, error)
	// HostSnapshots lists the snapshots, newest first.
	HostSnapshots(ctx context.Context) ([]api.HostSnapshot, error)
	// HostSnapshotRestore puts a snapshot back (name or api.HostSnapshotLatest).
	HostSnapshotRestore(ctx context.Context, name string, dryRun bool) (api.HostChange, error)
}

// HostIdentityState is what the helper reads from the host.
type HostIdentityState struct {
	// Current is what the host has now: the running host name, /etc/resolv.conf, the server lines of the jailops
	// block of /etc/ntp.conf and ntpd_enable. Resolvers and NTP are never nil.
	Current api.HostIdentitySpec `json:"current"`
	// RCHostname is hostname in /etc/rc.conf.
	RCHostname    string `json:"rc_hostname,omitempty"`
	ResolvManaged bool   `json:"resolv_managed,omitempty"`
	NTPBlock      bool   `json:"ntp_block,omitempty"`
	// Applied is what the helper last applied, nil when nothing was.
	Applied   *api.HostIdentitySpec `json:"applied,omitempty"`
	AppliedAt time.Time             `json:"applied_at,omitzero"`
	// NTPSync is nil when ntpq could not be asked.
	NTPSync *api.HostNTPSync `json:"ntp_sync,omitempty"`
	// Modules are the modules priv.conf lets the helper change.
	Modules []string `json:"modules,omitempty"`
}

// hostManageHint names the two lines that enable a module.
func hostManageHint(module string, config, helper bool) string {
	var steps []string
	if !config {
		steps = append(steps, "add `host: {manage: {modules: ["+module+"]}}` to /usr/local/etc/jailops/jailopsd.yaml")
	}
	if !helper {
		steps = append(steps, "add `host-manage = "+module+"` to /usr/local/etc/jailops/priv.conf")
	}
	return strings.Join(steps, " and ") + ", then restart both services (service jailops_priv restart; service jailopsd restart); see docs/appliance.md"
}

func (s *Service) hostSystem() (HostSystem, error) {
	if s.d.Host == nil {
		return nil, api.Errorf(api.CodeUnsupported, "host management is not available on this daemon").
			WithHint("jailops-priv offers it; run jailopsd with the helper")
	}
	return s.d.Host, nil
}

func (s *Service) hostConfigured(module string) bool { return slices.Contains(s.d.HostModules, module) }

// HostIdentity reads the host's identity and how JailOps manages it. It works whether the module is enabled or not.
func (s *Service) HostIdentity(ctx context.Context) (api.HostIdentity, error) {
	if err := requireGlobal(ctx, "reading the host identity"); err != nil {
		return api.HostIdentity{}, err
	}
	h, err := s.hostSystem()
	if err != nil {
		return api.HostIdentity{}, err
	}
	st, err := h.HostIdentityRead(ctx)
	if err != nil {
		return api.HostIdentity{}, err
	}
	return s.hostIdentityView(st), nil
}

// hostIdentityView turns what the helper read into the API object.
func (s *Service) hostIdentityView(st HostIdentityState) api.HostIdentity {
	out := api.HostIdentity{Kind: api.KindHostIdentity, Spec: st.Current}
	cfg, helper := s.hostConfigured(api.HostModuleIdentity), slices.Contains(st.Modules, api.HostModuleIdentity)
	out.Status = api.HostIdentityStatus{
		Managed: cfg && helper, ConfigEnabled: cfg, HelperEnabled: helper,
		Applied: st.Applied, RCHostname: st.RCHostname, ResolvConfManaged: st.ResolvManaged, NTPBlock: st.NTPBlock, NTPSync: st.NTPSync,
	}
	if st.Applied != nil && !st.AppliedAt.IsZero() {
		t := st.AppliedAt.UTC()
		out.Status.AppliedAt = &t
	}
	out.Status.Drift = HostIdentityDrift(st)
	return out
}

// HostIdentityDrift names the parts of the last applied identity that the host no longer has: someone changed
// them by hand (or another program did, such as a DHCP client rewriting /etc/resolv.conf).
func HostIdentityDrift(st HostIdentityState) []string {
	a := st.Applied
	if a == nil {
		return nil
	}
	var d []string
	cur := st.Current
	if a.Hostname != "" && (!strings.EqualFold(a.Hostname, cur.Hostname) || !strings.EqualFold(a.Hostname, st.RCHostname)) {
		d = append(d, "hostname")
	}
	if a.Resolvers != nil && (cur.Resolvers == nil || !st.ResolvManaged || !slices.Equal(a.Resolvers.Nameservers, cur.Resolvers.Nameservers) ||
		!slices.Equal(a.Resolvers.Search, cur.Resolvers.Search)) {
		d = append(d, "resolvers")
	}
	if a.NTP != nil && (cur.NTP == nil || a.NTP.Enable != cur.NTP.Enable || !slices.Equal(a.NTP.Servers, cur.NTP.Servers) ||
		(len(a.NTP.Servers) > 0 && !st.NTPBlock)) {
		d = append(d, "ntp")
	}
	return d
}

// hostChangeAllowed checks the scope and that the module is enabled here; the helper checks priv.conf itself.
func (s *Service) hostChangeAllowed(ctx context.Context, module, what string) (HostSystem, error) {
	if err := requireGlobal(ctx, what); err != nil {
		return nil, err
	}
	if a := ActorFrom(ctx); a.Perm != api.PermAdmin {
		return nil, api.Errorf(api.CodeForbidden, "%s needs the admin role", what)
	}
	h, err := s.hostSystem()
	if err != nil {
		return nil, err
	}
	if !s.hostConfigured(module) {
		helper := false
		if st, err := h.HostIdentityRead(ctx); err == nil {
			helper = slices.Contains(st.Modules, module)
		}
		return nil, api.Errorf(api.CodeNotManaged, "JailOps does not manage the host's %s", module).
			WithHint(hostManageHint(module, false, helper)).WithDetail("module", module)
	}
	return h, nil
}

// notManagedByHelper turns the helper's refusal of a module priv.conf does not list into not_managed.
func notManagedByHelper(err error, module string) error {
	var ae *api.Error
	if asAPI(err, &ae) && ae.Code == api.CodeForbidden && ae.Detail["module"] == module {
		return api.Errorf(api.CodeNotManaged, "the privileged helper does not manage the host's %s", module).
			WithHint(hostManageHint(module, true, false)).WithDetail("module", module)
	}
	return err
}

// ApplyHostIdentity changes the host name, the resolvers and NTP, or with DryRun says what it would change.
func (s *Service) ApplyHostIdentity(ctx context.Context, in api.HostIdentityApply) (api.HostChange, error) {
	if err := in.Spec.Validate(); err != nil {
		return api.HostChange{}, err
	}
	h, err := s.hostChangeAllowed(ctx, api.HostModuleIdentity, "changing the host identity")
	if err != nil {
		return api.HostChange{}, err
	}
	unlock, err := s.locks.tryLock("host identity", "host/identity")
	if err != nil {
		return api.HostChange{}, err
	}
	defer unlock()
	res, err := h.HostIdentityApply(ctx, in.Spec.Canonical(), in.DryRun)
	if err != nil {
		return api.HostChange{}, notManagedByHelper(err, api.HostModuleIdentity)
	}
	if res.Actions == nil {
		res.Actions = []string{}
	}
	if !in.DryRun && res.Changed {
		s.emit(ctx, "host-identity-updated", "/api/v1/host/identity")
	}
	return res, nil
}

// HostSnapshots lists the copies of host files taken before each change, newest first.
func (s *Service) HostSnapshots(ctx context.Context) ([]api.HostSnapshot, error) {
	if err := requireGlobal(ctx, "listing host snapshots"); err != nil {
		return nil, err
	}
	h, err := s.hostSystem()
	if err != nil {
		return nil, err
	}
	v, err := h.HostSnapshots(ctx)
	if v == nil {
		v = []api.HostSnapshot{}
	}
	return v, err
}

// RestoreHostSnapshot puts the files of a snapshot back (name or "latest").
func (s *Service) RestoreHostSnapshot(ctx context.Context, name string, in api.HostRestore) (api.HostChange, error) {
	if err := api.ValidHostSnapshotName(name); err != nil {
		return api.HostChange{}, err
	}
	h, err := s.hostChangeAllowed(ctx, api.HostModuleIdentity, "restoring host files")
	if err != nil {
		return api.HostChange{}, err
	}
	unlock, err := s.locks.tryLock("host restore", "host/identity")
	if err != nil {
		return api.HostChange{}, err
	}
	defer unlock()
	res, err := h.HostSnapshotRestore(ctx, name, in.DryRun)
	if err != nil {
		return api.HostChange{}, notManagedByHelper(err, api.HostModuleIdentity)
	}
	if res.Actions == nil {
		res.Actions = []string{}
	}
	if !in.DryRun && res.Changed {
		s.emit(ctx, "host-identity-restored", "/api/v1/host/snapshots/"+name)
	}
	return res, nil
}

// doctorHostIdentity reports the identity module (host.identity) and the time service (host.ntp).
func (s *Service) doctorHostIdentity(ctx context.Context, c *checkSet) {
	if s.d.Host == nil {
		return
	}
	st, err := s.d.Host.HostIdentityRead(ctx)
	if err != nil {
		c.add("host.identity", api.DoctorUnknown, "cannot read the host identity: "+err.Error(), "")
		c.add("host.ntp", api.DoctorUnknown, "cannot read the time service settings: "+err.Error(), "")
		return
	}
	v := s.hostIdentityView(st)
	switch {
	case !v.Status.Managed:
		hint := ""
		if v.Status.ConfigEnabled != v.Status.HelperEnabled {
			hint = hostManageHint(api.HostModuleIdentity, v.Status.ConfigEnabled, v.Status.HelperEnabled)
		}
		msg := "not managed by JailOps (host name, resolvers and NTP are the administrator's)"
		if hint != "" {
			msg = "enabled in only one of jailopsd.yaml and priv.conf, so not managed"
			c.add("host.identity", api.DoctorWarn, msg, hint)
			break
		}
		c.add("host.identity", api.DoctorOK, msg, "")
	case len(v.Status.Drift) > 0:
		c.add("host.identity", api.DoctorWarn, "changed outside JailOps since it was applied: "+strings.Join(v.Status.Drift, ", "),
			"see the difference with `jailops host show`; apply again with `jailops host identity set`, or put the last copy back with `jailops host restore`")
	case v.Status.Applied == nil:
		c.add("host.identity", api.DoctorOK, "managed by JailOps; nothing applied yet", "")
	default:
		c.add("host.identity", api.DoctorOK, "managed by JailOps; the host has what was applied", "")
	}
	ntp := st.Current.NTP
	switch {
	case ntp == nil || !ntp.Enable:
		c.add("host.ntp", api.DoctorWarn, "ntpd is not enabled (ntpd_enable is not YES): the clock drifts, and TLS, TOTP and backups depend on it",
			"jailops host identity set --ntp enable, or sysrc ntpd_enable=YES ntpd_sync_on_start=YES && service ntpd start")
	case st.NTPSync == nil:
		c.add("host.ntp", api.DoctorUnknown, "ntpd is enabled; its synchronisation could not be read (ntpq did not answer)", "")
	case !st.NTPSync.Synchronised:
		msg := "ntpd runs but is not synchronised"
		if st.NTPSync.Detail != "" {
			msg += ": " + st.NTPSync.Detail
		}
		c.add("host.ntp", api.DoctorWarn, msg, "check the servers with `ntpq -pn`; a freshly started ntpd needs a few minutes")
	default:
		c.add("host.ntp", api.DoctorOK, "synchronised to "+st.NTPSync.Peer, "")
	}
}
