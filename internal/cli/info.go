package cli

import (
	"context"
	"fmt"
	"io"
	"net/netip"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

// jailInfo is the report of one jail for a person: the jail, its snapshots,
// its update history and the networks it is attached to, gathered into one
// view. -o json and -o yaml print it as it is.
type jailInfo struct {
	Name          string            `json:"name"`
	Project       string            `json:"project,omitempty"`
	UUID          string            `json:"uuid,omitempty"`
	Hostname      string            `json:"hostname,omitempty"`
	State         api.PowerState    `json:"state"`
	JID           int               `json:"jid,omitempty"`
	OS            api.OS            `json:"os,omitempty"`
	Layout        api.Layout        `json:"layout,omitempty"`
	Image         string            `json:"image"`
	Generation    string            `json:"generation,omitempty"`
	Storage       string            `json:"storage,omitempty"`
	Profiles      []string          `json:"profiles,omitempty"`
	CreatedAt     time.Time         `json:"created_at,omitzero"`
	StartedAt     time.Time         `json:"started_at,omitzero"`
	ProvisionedAt time.Time         `json:"provisioned_at,omitzero"`
	Resources     infoResources     `json:"resources"`
	Interfaces    []infoInterface   `json:"interfaces"`
	Volumes       []api.VolumeMount `json:"volumes,omitempty"`
	Snapshots     infoSnapshots     `json:"snapshots"`
	Update        infoUpdate        `json:"update"`
	Labels        map[string]string `json:"labels,omitempty"`
	// Problems are the conditions that are not True, and the drift.
	Problems []string `json:"problems,omitempty"`
}

// infoResources holds the limits of the jail and what it uses, as the server
// measured it for this report. A part the server could not measure carries
// its reason; a number that is not measured is not printed.
type infoResources struct {
	Limits api.Resources  `json:"limits"`
	Usage  *api.JailUsage `json:"usage,omitempty"`
	// UsageError says why the usage could not be read at all.
	UsageError string `json:"usage_error,omitempty"`
}

// infoInterface is one network interface of the jail with every address on it.
type infoInterface struct {
	Name    string `json:"name"`
	Network string `json:"network"`
	// MAC is the fixed address of the specification; empty when the server derives one.
	MAC string `json:"mac,omitempty"`
	// MTU is the interface's own, else the network's; zero when neither sets one.
	MTU int `json:"mtu,omitempty"`
	// State is "up" while the jail runs and the server reports an address of
	// this interface as configured, else "down".
	State     string        `json:"state"`
	Addresses []infoAddress `json:"addresses"`
}

// infoAddress is one address with what can be told from the address itself.
type infoAddress struct {
	Address string `json:"address"`
	Family  string `json:"family"` // ipv4 or ipv6
	Scope   string `json:"scope"`  // global or link
	// Configured says the server saw the address on the running jail.
	Configured bool `json:"configured"`
}

type infoSnapshots struct {
	Count    int       `json:"count"`
	Newest   string    `json:"newest,omitempty"`
	NewestAt time.Time `json:"newest_at,omitzero"`
	// Error says why the snapshots could not be listed.
	Error string `json:"error,omitempty"`
}

type infoUpdate struct {
	Strategy api.UpdateStrategy `json:"strategy,omitempty"`
	Mode     api.UpdateMode     `json:"mode,omitempty"`
	Schedule string             `json:"schedule,omitempty"`
	Window   string             `json:"window,omitempty"`
	// Last is the newest record of the jail's update history.
	Last *api.UpdateRecord `json:"last,omitempty"`
	// Error says why the history could not be read.
	Error string `json:"error,omitempty"`
}

func (a *app) jailInfo(d *kindDesc[api.Jail]) *cobra.Command {
	return &cobra.Command{
		Use:   "info NAME",
		Short: "Describe one jail for a person",
		Long: `Print a short report on one jail: what it is, whether it runs, its limits
and what it uses (processes, cpu time and memory while it runs, the space of
its datasets always), every interface with all its addresses, its snapshots,
its update policy with the last update, and its labels. The report gathers the
jail, its usage, its snapshots, its update history and its networks; a part
the server cannot give is said to be missing, never guessed.

'show' prints the specification and status as the server holds them; 'info'
is the same jail read out for a person. -o json and -o yaml print the report
as structured data.`,
		Example:           "jailops jail info web\njailops jail info web -o yaml",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKind(a, d),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.connect(ctx)
			if err != nil {
				return err
			}
			j, err := c.Jails().Get(ctx, args[0])
			if err != nil {
				return err
			}
			info := a.gatherJailInfo(ctx, c, j)
			if a.format != fmtTable {
				return a.emit(info, nil, nil, func() []string { return []string{info.Name} })
			}
			a.printJailInfo(a.env.Stdout, info)
			return nil
		},
	}
}

// gatherJailInfo builds the report. The jail itself is already read; the
// extras (snapshots, history, networks) are tried, and a failure is noted in
// the report rather than ending the command.
func (a *app) gatherJailInfo(ctx context.Context, c *sdk.Client, j api.Jail) jailInfo {
	m, s, st := j.Metadata, j.Spec, j.Status
	info := jailInfo{
		Name: m.Name, Project: m.Project, UUID: m.UUID, Hostname: s.Hostname,
		State: st.Power, JID: st.JID, OS: s.OS, Layout: s.Layout, Image: s.Image, Generation: st.Generation,
		Storage: s.Storage, Profiles: s.Profiles,
		CreatedAt: m.CreatedAt, StartedAt: st.StartedAt, ProvisionedAt: st.ProvisionedAt,
		Resources: infoResources{Limits: s.Resources},
		Volumes:   s.Volumes,
		Update:    infoUpdate{Strategy: s.Update.Strategy, Mode: s.Update.Mode, Schedule: s.Update.Schedule, Window: s.Update.Window},
		Labels:    m.Labels,
	}
	if info.State == "" {
		info.State = api.PowerStopped
	}
	configured := map[string]bool{}
	for _, ad := range st.Addresses {
		configured[ad] = true
	}
	mtus := map[string]int{}
	for i, n := range s.Network {
		ifc := infoInterface{Name: n.Name, Network: n.Network, MAC: n.MAC, MTU: n.MTU, State: "down", Addresses: []infoAddress{}}
		if ifc.Name == "" {
			ifc.Name = fmt.Sprintf("eth%d", i)
		}
		if ifc.MTU == 0 {
			if _, seen := mtus[n.Network]; !seen {
				mtus[n.Network] = 0
				if nw, err := c.Networks().Get(ctx, n.Network); err == nil {
					mtus[n.Network] = nw.Spec.MTU
				}
			}
			ifc.MTU = mtus[n.Network]
		}
		for _, raw := range []string{n.IPv4, n.IPv6} {
			ad := fixedAddr(raw)
			if ad == "" {
				continue
			}
			ia := infoAddress{Address: ad, Family: "ipv4", Scope: "global", Configured: configured[ad]}
			if ip, ok := addrOf(ad); ok {
				if ip.Is6() {
					ia.Family = "ipv6"
				}
				if ip.IsLinkLocalUnicast() {
					ia.Scope = "link"
				}
			}
			if ia.Configured && st.Power == api.PowerRunning {
				ifc.State = "up"
			}
			ifc.Addresses = append(ifc.Addresses, ia)
		}
		info.Interfaces = append(info.Interfaces, ifc)
	}
	if info.Interfaces == nil {
		info.Interfaces = []infoInterface{}
	}
	if u, err := c.Jails().Usage(ctx, m.Name); err != nil {
		info.Resources.UsageError = err.Error()
	} else {
		info.Resources.Usage = &u
	}
	snaps, err := collect(c.Jails().Snapshots(m.Name).List(ctx))
	if err != nil {
		info.Snapshots.Error = err.Error()
	} else {
		info.Snapshots.Count = len(snaps)
		if n := len(snaps); n > 0 {
			info.Snapshots.Newest, info.Snapshots.NewestAt = snaps[n-1].Metadata.Name, snaps[n-1].Metadata.CreatedAt
		}
	}
	hist, err := c.Jails().UpdateHistory(ctx, m.Name)
	if err != nil {
		info.Update.Error = err.Error()
	} else if n := len(hist); n > 0 {
		last := hist[n-1]
		info.Update.Last = &last
	}
	for _, cond := range st.Conditions {
		if cond.Status != api.ConditionTrue {
			p := cond.Type
			if cond.Reason != "" {
				p += " " + cond.Reason
			}
			if cond.Message != "" {
				p += ": " + cond.Message
			}
			info.Problems = append(info.Problems, p)
		}
	}
	for _, d := range st.Drift {
		info.Problems = append(info.Problems, "drift: "+d)
	}
	return info
}

// addrOf parses an address with or without a prefix length.
func addrOf(s string) (netip.Addr, bool) {
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Addr(), true
	}
	ip, err := netip.ParseAddr(s)
	return ip, err == nil
}

func (a *app) printJailInfo(w io.Writer, info jailInfo) {
	now := a.env.Now()
	kv := func(k string, v any) { fprintf(w, "  %-13s %v\n", k+":", v) }
	when := func(t time.Time) string {
		if t.IsZero() {
			return "-"
		}
		return t.UTC().Format("2006-01-02 15:04 MST") + " (" + age(now, t) + " ago)"
	}
	section := func(title string) { fprintf(w, "\n%s\n", title) }

	fprintf(w, "Jail %s\n", info.Name)
	kv("Project", dash(info.Project))
	kv("Hostname", dash(info.Hostname))
	state := a.paint(w, stateStyle(0), string(info.State))
	if info.JID > 0 {
		state += fmt.Sprintf(" (jid %d)", info.JID)
	}
	kv("State", state)
	kind := []string{}
	if info.Layout != "" {
		kind = append(kind, string(info.Layout))
	}
	switch info.OS {
	case api.OSFreeBSD:
		kind = append(kind, "FreeBSD")
	case api.OSLinux:
		kind = append(kind, "Linux")
	case "":
	default:
		kind = append(kind, string(info.OS))
	}
	kv("Type", dash(strings.Join(append(kind, "jail"), " ")))
	image := info.Image
	if info.Generation != "" {
		image += ", generation " + info.Generation
	}
	kv("Image", image)
	kv("Storage", dash(info.Storage))
	if len(info.Profiles) > 0 {
		kv("Profiles", strings.Join(info.Profiles, ", "))
	}
	kv("Created", when(info.CreatedAt))
	if info.State == api.PowerRunning {
		kv("Started", when(info.StartedAt))
	}
	if !info.ProvisionedAt.IsZero() {
		kv("Provisioned", when(info.ProvisionedAt))
	}
	kv("UUID", dash(info.UUID))

	section("Resources")
	l := info.Resources.Limits
	var lim []string
	if l.CPU > 0 {
		lim = append(lim, fmt.Sprintf("cpu %d", l.CPU))
	}
	if l.Memory != "" {
		lim = append(lim, "memory "+l.Memory)
	}
	if l.Disk != "" {
		lim = append(lim, "disk "+l.Disk)
	}
	if l.DiskReservation != "" {
		lim = append(lim, "disk reservation "+l.DiskReservation)
	}
	if l.PIDs > 0 {
		lim = append(lim, fmt.Sprintf("pids %d", l.PIDs))
	}
	if len(lim) == 0 {
		lim = []string{"none"}
	}
	kv("Limits", strings.Join(lim, ", "))
	switch u := info.Resources.Usage; {
	case u == nil:
		kv("Usage", "not available: "+info.Resources.UsageError)
	case u.Accounting == nil:
		kv("Usage", "- (the jail is not running)")
	case u.Accounting.Error != "":
		kv("Usage", "- ("+u.Accounting.Error+")")
	default:
		a := u.Accounting
		mem := humanBytes(a.MemoryBytes)
		if l.Memory != "" {
			mem += " of " + l.Memory
		}
		kv("Usage", fmt.Sprintf("processes %d, cpu %s, memory %s", a.Processes, humanSeconds(a.CPUSeconds), mem))
	}
	if u := info.Resources.Usage; u != nil {
		if u.Disk.Error != "" {
			kv("Disk", "not available: "+u.Disk.Error)
		} else {
			kv("Disk", fmt.Sprintf("root %s used (%s referenced), data %s", humanBytes(u.Disk.RootBytes), humanBytes(u.Disk.RootReferencedBytes), humanBytes(u.Disk.DataBytes)))
		}
	}
	for _, v := range info.Volumes {
		mount := v.Mount
		if v.ReadOnly {
			mount += " (read-only)"
		}
		if v.Quota != "" {
			mount += ", quota " + v.Quota
		}
		kv("Volume "+v.Name, mount)
	}

	section("Network")
	if len(info.Interfaces) == 0 {
		fprintln(w, "  no interfaces")
	}
	for _, ifc := range info.Interfaces {
		mtu := "network default"
		if ifc.MTU > 0 {
			mtu = fmt.Sprint(ifc.MTU)
		}
		mac := "derived by the server"
		if ifc.MAC != "" {
			mac = ifc.MAC
		}
		kv(ifc.Name, fmt.Sprintf("network %s, mac %s, mtu %s, %s", ifc.Network, mac, mtu, a.paint(w, stateStyle(0), ifc.State)))
		if len(ifc.Addresses) == 0 {
			fprintln(w, "    no addresses")
		}
		for _, ad := range ifc.Addresses {
			note := ""
			if ad.Configured {
				note = ", configured"
			}
			fprintf(w, "    %-42s %s %s%s\n", ad.Address, ad.Family, ad.Scope, note)
		}
	}

	section("Snapshots")
	switch {
	case info.Snapshots.Error != "":
		kv("Count", "not available: "+info.Snapshots.Error)
	case info.Snapshots.Count == 0:
		kv("Count", "0")
	default:
		kv("Count", info.Snapshots.Count)
		kv("Newest", info.Snapshots.Newest+" ("+age(now, info.Snapshots.NewestAt)+" ago)")
	}

	section("Update policy")
	u := info.Update
	strategy := dash(string(u.Strategy))
	mode := u.Mode
	if mode == "" {
		mode = api.UpdateModeManual // the zero value: nothing happens unless asked
	}
	kv("Strategy", strategy+", mode "+string(mode))
	if u.Schedule != "" || u.Window != "" {
		kv("Schedule", strings.TrimSpace(dash(u.Schedule)+" "+u.Window))
	}
	switch {
	case u.Error != "":
		kv("Last", "not available: "+u.Error)
	case u.Last == nil:
		kv("Last", "no update yet")
	default:
		res := u.Last.Kind + " " + a.paint(w, updateStyle, u.Last.Result)
		if u.Last.RootRolledBack {
			res += " (root rolled back)"
		}
		if u.Last.To != "" {
			res += ", " + dash(u.Last.From) + " -> " + u.Last.To
		}
		res += ", " + age(now, u.Last.Started) + " ago"
		kv("Last", res)
	}

	if len(info.Labels) > 0 {
		section("Labels")
		for _, k := range sortedKeys(info.Labels) {
			kv(k, info.Labels[k])
		}
	}
	if len(info.Problems) > 0 {
		section("Problems")
		for _, p := range info.Problems {
			fprintf(w, "  %s\n", p)
		}
	}
}

// humanSeconds prints a CPU time: 12s, 4m57s, 2h13m, 3d2h.
func humanSeconds(s int64) string {
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm%ds", s/60, s%60)
	case s < 86400:
		return fmt.Sprintf("%dh%dm", s/3600, s%3600/60)
	}
	return fmt.Sprintf("%dd%dh", s/86400, s%86400/3600)
}

// updateStyle colours the result of an update.
func updateStyle(_ int, v string) string {
	switch v {
	case api.UpdateOK:
		return "32"
	case api.UpdateFailed, api.UpdateRolledBack:
		return "31"
	}
	return ""
}
