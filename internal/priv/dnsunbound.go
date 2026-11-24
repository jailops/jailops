package priv

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"jailops.net/jailops/internal/dnsprobe"
	"jailops.net/jailops/internal/ipam"
	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/internal/sys/ifconfig"
	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/sdk/api"
)

// DNSUnboundReq is the request of dns.unbound: Apply writes the file and reloads unbound when the file is not what the
// stored objects render; without it the operation only reports.
type DNSUnboundReq struct {
	Apply bool `json:"apply,omitempty"`
}

// UnboundRes says where the names of the jails stand. Enabled is false when priv.conf has no unbound-conf: the
// integration is off and nothing else is set.
type UnboundRes struct {
	Enabled  bool   `json:"enabled"`
	Reason   string `json:"reason,omitempty"` // why it is not enabled although priv.conf allows it: what is missing, with the fix
	Base     bool   `json:"base,omitempty"`   // the file is the default one: the base system's local-unbound answers
	Path     string `json:"path,omitempty"`
	Suffix   string `json:"suffix,omitempty"`   // the zone suffix of the names (dns-suffix)
	Current  bool   `json:"current"`            // the file holds what the stored objects render
	Changed  bool   `json:"changed,omitempty"`  // this call wrote it
	Reloaded bool   `json:"reloaded,omitempty"` // and the resolver read it again
	Control  string `json:"control,omitempty"`  // why the control program did not answer; empty: it did
	Warn     string `json:"warn,omitempty"`     // an address that should answer does not, and did not before the change either
	Names    int    `json:"names"`              // the distinct names in the file
	Jails    int    `json:"jails,omitempty"`    // the running jails with an address they are for
	Projects int    `json:"projects,omitempty"` // and the projects of those
	// Forward are the upstream resolvers the host's unbound sends every other name to; Recurse says it resolves them
	// itself from the root servers (no forward zone). Both are empty while the control program does not answer.
	Forward  []string `json:"forward,omitempty"`
	Recurse  bool     `json:"recurse,omitempty"`
	Gateways []string `json:"gateways,omitempty"` // the addresses of the host on the networks the resolver has to listen on
}

var dnsMu sync.Mutex

// DefaultUnboundConf is the file of the base system's local-unbound: local-unbound-setup includes /var/unbound/conf.d/*.conf.
const DefaultUnboundConf = "/var/unbound/conf.d/jailops.conf"

// DefaultDNSSuffix is the zone suffix of the names of the jails: NAME.PROJECT.internal.
const DefaultDNSSuffix = "internal"

// DNSUnbound renders the names of the running jails for the host's resolver and, with Apply, writes them to the one
// file root named (unbound-conf in priv.conf; by default the conf.d of local-unbound) and has the resolver read it
// again. The text is made here, from the stored objects of the jails and networks: the daemon chooses no byte of it,
// so a compromised daemon cannot make the resolver answer for names of its own choosing beyond what a jail's own name
// and addresses say. It is available when the directory of the file exists, belongs to root and is closed to others,
// and the control program of the resolver exists; otherwise Enabled is false and Reason says what to do.
func (l *Local) DNSUnbound(ctx context.Context, apply bool) (UnboundRes, error) {
	res := UnboundRes{}
	path := l.cfg.UnboundConf
	if path == "" {
		if apply && isAdmin(ctx) {
			dnsMu.Lock()
			defer dnsMu.Unlock()
			res.Control = l.removeOwnDefaultConf(ctx)
			l.DropStaleResolverAddrs(ctx, false) // no sync of the names runs: the addresses of deleted networks come off here
		}
		return res, nil
	}
	res.Path, res.Base, res.Suffix = path, path == DefaultUnboundConf, l.cfg.DNSSuffix
	defer func() {
		// the names cannot be written: the pass below that keeps the addresses in step does not run, so at least the
		// addresses of networks that are gone come off
		if apply && !res.Enabled && isAdmin(ctx) {
			l.DropStaleResolverAddrs(ctx, false)
		}
	}()
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		res.Reason = filepath.Dir(path) + " does not exist (the host resolver is not set up: local-unbound-setup)"
		return res, nil
	}
	if err := rootOwned(filepath.Dir(path)); err != nil {
		d := filepath.Dir(path)
		res.Reason = d + " is not closed to others (" + err.Error() + "): chown root:wheel " + d + " && chmod 755 " + d
		return res, nil
	}
	if _, ok := controlBinary(res.Base); !ok {
		res.Reason = "the control program of the resolver (" + strings.Join(controlNames(res.Base), " or ") + ") is not installed"
		return res, nil
	}
	res.Enabled = true
	if apply && !isAdmin(ctx) {
		return res, forbidden("dns.unbound with apply changes the host's resolver: it needs the admin assertion")
	}
	dnsMu.Lock()
	defer dnsMu.Unlock()
	jails, nets := l.dnsInputs(ctx)
	text := render.UnboundConf(l.cfg.DNSSuffix, jails, nets, listeningAddrs(path))
	res.Names = countNames(text)
	projects := map[string]bool{}
	for _, j := range jails {
		res.Jails++
		projects[j.Project] = true
	}
	for _, n := range nets {
		for _, a := range []netip.Addr{n.Gateway, n.Resolver6} {
			if a.IsValid() && !slices.Contains(res.Gateways, a.String()) {
				res.Gateways = append(res.Gateways, a.String())
			}
		}
	}
	res.Projects = len(projects)
	slices.Sort(res.Gateways)
	cur, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return res, fmt.Errorf("unbound-conf %s: %w", path, err)
	}
	existed := err == nil
	res.Current = existed && bytes.Equal(cur, []byte(text))
	if apply {
		l.syncResolverAddrs(ctx, nets)
	}
	var out string
	var cerr error
	if apply && !res.Current {
		// A text that failed is not tried again at every pass: each try writes the file and restarts the resolver.
		if why := l.dnsHeldBack(text); why != "" {
			res.Control = why
			return res, nil
		}
		// What answers now has to answer after the change: the host's own resolver above all (pkg and everything else
		// ask it), and the gateways the jails ask.
		addrs := l.probeAddrs(ctx, res.Gateways)
		before := l.answering(ctx, addrs, 2)
		if err := l.writeUnboundConf(path, text); err != nil {
			return res, err
		}
		// The resolver checks its configuration, with the new file in place, before it reads it: a text it refuses
		// (an interface the administrator has named too) puts the old file back and nothing is reloaded.
		if why := l.checkResolver(ctx, res.Base); why != "" {
			l.restoreUnboundConf(path, cur, existed)
			res.Control = "the resolver refused the names, the old file is back: " + why
			l.dnsHoldBack(text, res.Control)
			return res, nil
		}
		res.Changed, res.Current = true, true
		// An interface line that is new needs the resolver to start again (a reload does not bind a new address): a
		// new network does. Anything else, a jail that starts or stops, is read by a reload.
		if res.Base && interfaces(string(cur)) != interfaces(text) {
			out, cerr = l.restartResolver(ctx)
		} else {
			out, cerr = l.unboundControl(ctx, res.Base, "reload")
		}
		res.Reloaded = cerr == nil
		if cerr != nil {
			// the resolver would not read it: the old file goes back and the resolver reads that. What the rollback
			// itself did comes first: a resolver that is stopped is the worse news.
			rb := l.rollbackResolver(ctx, res.Base, path, cur, existed)
			res.Changed, res.Current, res.Reloaded = false, false, false
			res.Control = joinWhy(rb, describeControl(out, cerr))
			l.dnsHoldBack(text, res.Control)
			return res, nil
		}
		// The check that the restart exited 0 is not enough: a resolver that starts without the loopback exits 0 too.
		after := l.answering(ctx, addrs, 3)
		if lost := stoppedAnswering(before, after); len(lost) > 0 {
			rb := l.rollbackResolver(ctx, res.Base, path, cur, existed)
			res.Changed, res.Current, res.Reloaded = false, false, false
			msg := "the host's resolver stopped answering at " + strings.Join(lost, ", ") + " after the names were written: the old file is back"
			if rb != "" {
				msg = rb + "; " + msg
			}
			if again := stoppedAnswering(before, l.answering(ctx, addrs, 3)); len(again) > 0 {
				msg += "; it still does not answer at " + strings.Join(again, ", ") + " (look at: sockstat -46l | grep :53; service local_unbound status)"
			}
			res.Control = msg
			l.dnsHoldBack(text, msg)
			return res, errors.New(msg)
		}
		res.Warn = stillSilent(before, after)
	} else {
		out, cerr = l.unboundControl(ctx, res.Base, "status")
	}
	if cerr != nil {
		res.Control = describeControl(out, cerr)
	} else if fw, err := l.unboundControl(ctx, res.Base, "list_forwards"); err == nil {
		res.Forward, res.Recurse = parseForwards(fw)
	}
	return res, nil
}

// dnsRetryAfter is how long a text of the names that failed is not tried again.
const dnsRetryAfter = 10 * time.Minute

// dnsFailure is the last text the resolver would not take: its hash, why, and until when it is held back.
type dnsFailure struct {
	hash  string
	why   string
	until time.Time
}

func dnsHash(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])
}

// dnsHeldBack says why the text is not tried: the same text failed a short while ago ("" when it may be tried).
func (l *Local) dnsHeldBack(text string) string {
	f := l.dnsFail
	if f.hash == "" || f.hash != dnsHash(text) || !l.now().Before(f.until) {
		return ""
	}
	return fmt.Sprintf("held back until %s after a failure of the same text: %s", f.until.Format("15:04"), f.why)
}

func (l *Local) dnsHoldBack(text, why string) {
	l.dnsFail = dnsFailure{hash: dnsHash(text), why: why, until: l.now().Add(dnsRetryAfter)}
}

// joinWhy joins the reasons, the first one first, leaving out an empty one.
func joinWhy(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "; ")
}

// rollbackResolver puts the old file back and has the resolver read it. A restart that fails is tried once more: a
// resolver left stopped takes the host's names with it. It returns what went wrong ("" when nothing did).
func (l *Local) rollbackResolver(ctx context.Context, base bool, path string, old []byte, existed bool) string {
	l.restoreUnboundConf(path, old, existed)
	if !base {
		if out, err := l.unboundControl(ctx, base, "reload"); err != nil {
			return "the reload after the rollback failed: " + describeControl(out, err)
		}
		return ""
	}
	out, err := l.restartResolver(ctx)
	if err == nil {
		return ""
	}
	out2, err2 := l.restartResolver(ctx)
	if err2 == nil {
		return "the restart after the rollback failed once (" + describeControl(out, err) + ") and worked the second time"
	}
	return "the restart after the rollback failed (" + describeControl(out, err) + ") and again (" + describeControl(out2, err2) + "): local_unbound may be stopped, run service local_unbound onestart"
}

// hostLoopbackNameservers are the loopback nameservers of the host's resolv.conf, the ones the host asks itself: an
// unspecified address is the loopback, and a file with no nameserver line (or none) is the loopback too.
func (l *Local) hostLoopbackNameservers() []string {
	b, err := os.ReadFile(l.etcPath("resolv.conf"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil
	}
	var out []string
	seen := 0
	for _, ln := range strings.Split(string(b), "\n") {
		f := strings.Fields(ln)
		if len(f) < 2 || f[0] != "nameserver" {
			continue
		}
		seen++
		a, err := netip.ParseAddr(f[1])
		switch {
		case err != nil:
		case a.IsUnspecified():
			if a.Is6() {
				out = append(out, "::1")
			} else {
				out = append(out, "127.0.0.1")
			}
		case a.IsLoopback():
			out = append(out, a.Unmap().String())
		}
	}
	if seen == 0 {
		out = append(out, "127.0.0.1")
	}
	return slices.Compact(out)
}

// probeAddrs are the addresses to ask after a change: the host's own loopback nameservers, and the gateways of the
// networks that the host holds on an interface now (an IPv4 gateway exists only while a jail runs on its network).
func (l *Local) probeAddrs(ctx context.Context, gateways []string) []string {
	out := l.hostLoopbackNameservers()
	if len(gateways) == 0 {
		return out
	}
	held := map[string]bool{}
	if list, err := l.Net().List(ctx); err == nil {
		for _, i := range list {
			for _, p := range append(slices.Clone(i.Inet), i.Inet6...) {
				held[p.Addr().WithZone("").String()] = true
			}
		}
	}
	for _, g := range gateways {
		if held[g] && !slices.Contains(out, g) {
			out = append(out, g)
		}
	}
	return out
}

// answering asks each address for `localhost`, which unbound answers from its own built-in zone (no upstream, no
// cache), and says which answer in a way that serves a client.
func (l *Local) answering(ctx context.Context, addrs []string, tries int) map[string]bool {
	f := l.d.DNSProbe
	if f == nil {
		f = dnsprobe.Query
	}
	up := map[string]bool{}
	for _, a := range addrs {
		up[a] = dnsprobe.Wait(ctx, f, a, "localhost.", dnsprobe.TypeA, tries, 500*time.Millisecond).Up()
	}
	return up
}

// stoppedAnswering are the addresses that answered before and do not now, sorted.
func stoppedAnswering(before, after map[string]bool) []string {
	var out []string
	for a, was := range before {
		if was && !after[a] {
			out = append(out, a)
		}
	}
	slices.Sort(out)
	return out
}

// stillSilent says which addresses did not answer before and do not now: not a loss of the change, but doctor's news.
func stillSilent(before, after map[string]bool) string {
	var out []string
	for a, was := range before {
		if !was && !after[a] {
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return ""
	}
	slices.Sort(out)
	return "does not answer at " + strings.Join(out, ", ")
}

// removeOwnDefaultConf is what the helper does while the names are off (unbound-conf is empty): the file it wrote
// earlier stays in effect, with interface lines that took the loopback from the host's resolver, so it goes. Only a
// file that carries the header of the helper is touched; a restart follows once. It returns a note when that failed.
func (l *Local) removeOwnDefaultConf(ctx context.Context) string {
	b, err := os.ReadFile(DefaultUnboundConf)
	if err != nil || !bytes.HasPrefix(b, []byte(render.Header)) {
		return ""
	}
	if err := rootOwned(filepath.Dir(DefaultUnboundConf)); err != nil {
		return ""
	}
	if err := os.Remove(DefaultUnboundConf); err != nil {
		return "the file of the names could not be removed: " + err.Error()
	}
	if out, err := l.restartResolver(ctx); err != nil {
		return "the file of the names is removed, but the restart of the resolver failed: " + describeControl(out, err)
	}
	return ""
}

// checkResolver runs the configuration check of the resolver (local-unbound-checkconf for the base system's, else
// unbound-checkconf) on its own configuration, which includes the file as it is now. It returns the first line of
// what the check says when it refuses; "" when it accepts or when there is no check program to ask.
func (l *Local) checkResolver(ctx context.Context, base bool) string {
	names := []string{"unbound-checkconf", "local-unbound-checkconf"}
	if base {
		names = []string{"local-unbound-checkconf"}
	}
	for _, n := range names {
		bin, err := exec.LookPath(n)
		if err != nil {
			continue
		}
		res, err := l.d.Runner.Run(ctx, run.Cmd{Name: bin, Timeout: 20 * time.Second})
		if err == nil {
			return ""
		}
		out := strings.TrimSpace(string(res.Stdout) + "\n" + string(res.Stderr))
		for _, ln := range strings.Split(out, "\n") {
			if ln = strings.TrimSpace(ln); ln != "" {
				if len(ln) > 200 {
					ln = ln[:200]
				}
				return ln
			}
		}
		return err.Error()
	}
	return ""
}

// restoreUnboundConf puts the old text of the file back (or removes the file when there was none).
func (l *Local) restoreUnboundConf(path string, old []byte, existed bool) {
	if existed {
		_ = l.writeUnboundConf(path, string(old))
		return
	}
	_ = os.Remove(path)
}

// interfaces is the interface lines of a file, in a fixed order, to tell whether a reload is enough.
func interfaces(text string) string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if f := strings.Fields(l); len(f) == 2 && f[0] == "interface:" {
			out = append(out, f[1])
		}
	}
	slices.Sort(out)
	return strings.Join(out, " ")
}

// restartResolver restarts the base system's local-unbound (a few hundred milliseconds in which the jails get no
// answer): the one case a reload does not do.
func (l *Local) restartResolver(ctx context.Context) (string, error) {
	res, err := l.d.Runner.Run(ctx, run.Cmd{Name: "service", Args: []string{"local_unbound", "restart"}, Timeout: 30 * time.Second})
	return strings.TrimSpace(string(res.Stdout) + " " + string(res.Stderr)), err
}

// dnsInputs reads the running jails and the networks they use from the stored objects. A jail or a network that cannot
// be read is left out: a name that is missing is better than a refusal that stops the others.
func (l *Local) dnsInputs(ctx context.Context) ([]render.DNSJail, []render.DNSNet) {
	list, err := l.d.Jails.List(ctx)
	if err != nil {
		return nil, nil
	}
	var jails []render.DNSJail
	var nets []render.DNSNet
	seen := map[string]bool{}
	for _, j := range list {
		if j.Dying {
			continue
		}
		jn, err := ParseJailName(j.Name)
		if err != nil {
			continue
		}
		rec, err := l.loadRecord(ctx, jn)
		if err != nil {
			continue
		}
		dj := render.DNSJail{Project: jn.Project, Name: jn.Name}
		for _, att := range rec.Spec.Network {
			nw, err := l.loadNetwork(ctx, att.Network)
			if err != nil {
				continue
			}
			for _, raw := range []string{att.IPv4, att.IPv6} {
				if a, err := netip.ParseAddr(raw); err == nil {
					dj.Addrs = append(dj.Addrs, a.Unmap())
				}
			}
			if key := jn.Project + "/" + att.Network; !seen[key] {
				seen[key] = true
				dn := render.DNSNet{Project: jn.Project}
				if v4 := nw.Spec.IPv4; v4 != nil {
					if p, err := netip.ParsePrefix(v4.Subnet); err == nil {
						dn.Subnets = append(dn.Subnets, p)
					}
					if g, err := netip.ParseAddr(v4.Gateway); err == nil {
						dn.Gateway = g
					}
				}
				if v6 := nw.Spec.IPv6; v6 != nil {
					if p, err := netip.ParsePrefix(v6.Subnet); err == nil {
						dn.Subnets = append(dn.Subnets, p)
						dn.Resolver6 = ipam.Resolver6(p)
					}
				}
				nets = append(nets, dn)
			}
		}
		if len(dj.Addrs) > 0 {
			jails = append(jails, dj)
		}
	}
	nets = l.addStoredNetworks(ctx, nets, seen)
	return jails, nets
}

// addStoredNetworks adds every network that a project has, running jail or not: the resolver listens on the host's
// address of each of them and admits their subnets, so that it is there when the first jail of a network starts and
// is restarted when the networks change, never when a jail does.
func (l *Local) addStoredNetworks(ctx context.Context, nets []render.DNSNet, seen map[string]bool) []render.DNSNet {
	owner := map[string]string{} // network name -> project
	pdir := filepath.Join(l.cfg.StateDir, "objects", string(api.KindProject))
	if names, err := l.listState(pdir); err == nil {
		for _, n := range names {
			if !strings.HasSuffix(n, ".json") {
				continue
			}
			b, err := l.readState(filepath.Join(pdir, n))
			if err != nil {
				continue
			}
			var d struct {
				Metadata api.ObjectMeta  `json:"metadata"`
				Spec     api.ProjectSpec `json:"spec"`
			}
			if json.Unmarshal(b, &d) != nil {
				continue
			}
			for _, pn := range d.Spec.Networks {
				owner[pn.Network] = d.Metadata.Name
			}
		}
	}
	ndir := filepath.Join(l.cfg.StateDir, "objects", string(api.KindNetwork))
	names, err := l.listState(ndir)
	if err != nil {
		return nets
	}
	for _, n := range names {
		if !strings.HasSuffix(n, ".json") {
			continue
		}
		b, err := l.readState(filepath.Join(ndir, n))
		if err != nil {
			continue
		}
		var nw networkDoc
		if json.Unmarshal(b, &nw) != nil {
			continue
		}
		project := owner[nw.Metadata.Name]
		if project == "" || seen[project+"/"+nw.Metadata.Name] {
			continue
		}
		seen[project+"/"+nw.Metadata.Name] = true
		dn := render.DNSNet{Project: project}
		if v4 := nw.Spec.IPv4; v4 != nil {
			if p, err := netip.ParsePrefix(v4.Subnet); err == nil {
				dn.Subnets = append(dn.Subnets, p)
			}
			if g, err := netip.ParseAddr(v4.Gateway); err == nil {
				dn.Gateway = g
			}
		}
		if v6 := nw.Spec.IPv6; v6 != nil {
			if p, err := netip.ParsePrefix(v6.Subnet); err == nil {
				dn.Subnets = append(dn.Subnets, p)
				dn.Resolver6 = ipam.Resolver6(p)
			}
		}
		nets = append(nets, dn)
	}
	return nets
}

// writeUnboundConf replaces the file with the text: written beside it, then renamed, so unbound never reads half of it.
// The directory has to be root's and closed to others; a symbolic link or a file of someone else is refused.
func (l *Local) writeUnboundConf(path, text string) error {
	dir := filepath.Dir(path)
	if err := rootOwned(dir); err != nil {
		return fmt.Errorf("unbound-conf: %w", err)
	}
	if _, err := os.Lstat(path); err == nil {
		if err := rootOwned(path); err != nil {
			return fmt.Errorf("unbound-conf: %w", err)
		}
	}
	tmp := filepath.Join(dir, "."+filepath.Base(path)+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			_ = os.Remove(tmp)
			f, err = os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|os.O_EXCL, 0o644)
		}
		if err != nil {
			return fmt.Errorf("unbound-conf: %w", err)
		}
	}
	_, werr := f.WriteString(text)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("unbound-conf: %w", werr)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("unbound-conf: %w", err)
	}
	return nil
}

func controlNames(base bool) []string {
	if base {
		return []string{"local-unbound-control"}
	}
	return []string{"unbound-control", "local-unbound-control"}
}

// controlBinary finds the control program: the one of local-unbound for the default file, else the one of the
// unbound package, else the base system's.
func controlBinary(base bool) (string, bool) {
	for _, n := range controlNames(base) {
		if p, err := exec.LookPath(n); err == nil {
			return p, true
		}
	}
	return "", false
}

// unboundControl runs the control program with one fixed word (reload or status).
func (l *Local) unboundControl(ctx context.Context, base bool, word string) (string, error) {
	var out string
	var err error
	for _, bin := range controlNames(base) {
		var res run.Result
		res, err = l.d.Runner.Run(ctx, run.Cmd{Name: bin, Args: []string{word}, Timeout: 10 * time.Second})
		out = strings.TrimSpace(string(res.Stdout) + " " + string(res.Stderr))
		var ee *run.ExitError
		if err == nil || errors.As(err, &ee) {
			return out, err // it ran: its answer is the answer
		}
	}
	return out, err
}

func describeControl(out string, err error) string {
	s := strings.TrimSpace(out)
	if s == "" {
		s = err.Error()
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// listeningAddrs are the addresses the resolver's own configuration (the .conf files next to and below the file the
// helper writes, except that file) already has `interface:` lines for: unbound refuses an address named twice, so the
// generated file leaves them out. "0.0.0.0" stands for every IPv4 address.
func listeningAddrs(own string) []string {
	dir := filepath.Dir(own)
	var files []string
	for _, pat := range []string{filepath.Join(filepath.Dir(dir), "*.conf"), filepath.Join(dir, "*.conf")} {
		m, _ := filepath.Glob(pat)
		files = append(files, m...)
	}
	var out []string
	for _, f := range files {
		if f == own {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, l := range strings.Split(string(b), "\n") {
			l = strings.TrimSpace(l)
			if !strings.HasPrefix(l, "interface:") {
				continue
			}
			if i := strings.IndexByte(l, '#'); i >= 0 { // a trailing comment
				l = strings.TrimSpace(l[:i])
			}
			a := strings.Trim(strings.TrimSpace(strings.TrimPrefix(l, "interface:")), `"`)
			if i := strings.IndexByte(a, '@'); i >= 0 {
				a = a[:i]
			}
			if p, err := netip.ParseAddr(a); err == nil {
				out = append(out, p.String())
			}
		}
	}
	return out
}

// countNames is the number of distinct names the generated text answers.
func countNames(text string) int {
	seen := map[string]bool{}
	for _, l := range strings.Split(text, "\n") {
		if f := strings.Fields(l); len(f) > 1 && f[0] == "local-data:" {
			seen[strings.Trim(f[1], `"`)] = true
		}
	}
	return len(seen)
}

// parseForwards reads the output of `unbound-control list_forwards` (one line per forward zone: `. IN forward ADDR
// ...`): the addresses the root zone is forwarded to, or recurse when it is not forwarded at all.
func parseForwards(out string) ([]string, bool) {
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) >= 4 && f[0] == "." && f[2] == "forward" {
			var addrs []string
			for _, a := range f[3:] {
				if p, err := netip.ParseAddr(strings.SplitN(a, "@", 2)[0]); err == nil {
					addrs = append(addrs, p.String())
				}
			}
			return addrs, false
		}
	}
	return nil, true
}

// resolverAddrsFile records the addresses the helper put on the loopback for the resolver (one JSON list), so that the
// address of a network that is gone is taken off again. It is the helper's own, in its host directory.
func (l *Local) resolverAddrsFile() string {
	return filepath.Join(l.cfg.HostDir, "dns-resolver-addrs.json")
}

// syncResolverAddrs makes the host hold the resolver address (ipam.Resolver6) of every network with IPv6 on its
// loopback, as a /128: the jails ask it through their default gateway and unbound listens on it. The addresses
// come from the stored networks, never from the daemon's request, and the helper takes off only what it put there
// itself (the record). A failure is logged: the resolver still starts (it binds the address before it exists) and
// doctor says the lookup at the address fails.
func (l *Local) syncResolverAddrs(ctx context.Context, nets []render.DNSNet) {
	want := map[netip.Addr]bool{}
	for _, n := range nets {
		if n.Project != "" && n.Resolver6.IsValid() {
			want[n.Resolver6] = true
		}
	}
	var had []string
	if b, err := os.ReadFile(l.resolverAddrsFile()); err == nil {
		_ = json.Unmarshal(b, &had)
	}
	onLo := map[netip.Addr]bool{}
	if list, err := l.Net().List(ctx); err == nil {
		for _, i := range list {
			if i.Name == "lo0" {
				for _, p := range i.Inet6 {
					onLo[p.Addr()] = true
				}
			}
		}
	}
	ifc := func(a netip.Addr, verb string) error {
		_, err := l.d.Runner.Run(ctx, run.Cmd{Name: "/sbin/ifconfig", Args: append([]string{"lo0"}, ifconfig.AddrWords(netip.PrefixFrom(a, 128), verb)...), Timeout: 10 * time.Second})
		return err
	}
	var keep []string
	for a := range want {
		if !onLo[a] {
			if err := ifc(a, "alias"); err != nil {
				l.note("the resolver address could not be put on the loopback", "address", a.String(), "err", err.Error())
				continue
			}
		}
		keep = append(keep, a.String())
	}
	for _, h := range had {
		a, err := netip.ParseAddr(h)
		if err != nil || want[a] || !onLo[a] {
			continue
		}
		if err := ifc(a, "-alias"); err != nil {
			l.note("the resolver address of a removed network could not be taken off the loopback", "address", h, "err", err.Error())
			keep = append(keep, h) // try again at the next pass
		}
	}
	l.writeResolverAddrs(keep)
}

func (l *Local) writeResolverAddrs(keep []string) {
	slices.Sort(keep)
	if keep == nil {
		keep = []string{}
	}
	b, _ := json.Marshal(keep)
	if cur, err := os.ReadFile(l.resolverAddrsFile()); err == nil && bytes.Equal(bytes.TrimSpace(cur), b) {
		return
	}
	if err := os.MkdirAll(l.cfg.HostDir, 0o700); err != nil {
		return
	}
	_ = os.WriteFile(l.resolverAddrsFile(), append(b, '\n'), 0o600)
}

// DropStaleResolverAddrs takes off the loopback the resolver addresses the helper put there (its record) that no stored
// network accounts for any more, or every one of them with all set (the names of the jails are off: nothing listens
// there). The helper calls it when it starts and when the names are off or cannot be written: the pass that takes
// off the address of a deleted network otherwise runs only at the next sync of the names, and an address left on lo0
// keeps a later network with the same subnet from being created.
func (l *Local) DropStaleResolverAddrs(ctx context.Context, all bool) {
	var had []string
	if b, err := os.ReadFile(l.resolverAddrsFile()); err != nil || json.Unmarshal(b, &had) != nil || len(had) == 0 {
		return
	}
	known := map[netip.Addr]bool{}
	if !all {
		ndir := filepath.Join(l.cfg.StateDir, "objects", string(api.KindNetwork))
		names, err := l.listState(ndir)
		if err != nil {
			if _, serr := os.Stat(ndir); !errors.Is(serr, os.ErrNotExist) {
				return // the networks cannot be read: take nothing off
			}
		}
		for _, n := range names {
			if !strings.HasSuffix(n, ".json") {
				continue
			}
			b, err := l.readState(filepath.Join(ndir, n))
			if err != nil {
				return
			}
			var nw networkDoc
			if json.Unmarshal(b, &nw) != nil {
				return
			}
			if v6 := nw.Spec.IPv6; v6 != nil {
				if p, err := netip.ParsePrefix(v6.Subnet); err == nil {
					known[ipam.Resolver6(p)] = true
				}
			}
		}
	}
	onLo := map[netip.Addr]bool{}
	if list, err := l.Net().List(ctx); err == nil {
		for _, i := range list {
			if i.Name == "lo0" {
				for _, p := range i.Inet6 {
					onLo[p.Addr()] = true
				}
			}
		}
	} else {
		return
	}
	var keep []string
	for _, h := range had {
		a, err := netip.ParseAddr(h)
		if err != nil {
			continue
		}
		if known[a] {
			keep = append(keep, h)
			continue
		}
		if !onLo[a] {
			continue
		}
		if _, err := l.d.Runner.Run(ctx, run.Cmd{Name: "/sbin/ifconfig", Args: append([]string{"lo0"}, ifconfig.AddrWords(netip.PrefixFrom(a, 128), "-alias")...), Timeout: 10 * time.Second}); err != nil {
			l.note("the resolver address of a removed network could not be taken off the loopback", "address", h, "err", err.Error())
			keep = append(keep, h)
			continue
		}
		l.note("the resolver address of a removed network was taken off the loopback", "address", h)
	}
	l.writeResolverAddrs(keep)
}
