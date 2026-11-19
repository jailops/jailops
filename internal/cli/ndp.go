package cli

import (
	"fmt"
	"net/netip"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"jailops.net/jailops/sdk/api"
)

// ndpView is what `network ndp-proxy status` prints for one network, and its -o json form.
type ndpView struct {
	Network      string           `json:"network"`
	Enabled      bool             `json:"enabled"`
	Uplink       string           `json:"uplink,omitempty"`
	Prefix       string           `json:"prefix,omitempty"`
	Range        string           `json:"range,omitempty"`
	Answers      int              `json:"answers,omitempty"`
	BeyondLimit  bool             `json:"beyond_limit,omitempty"`
	CaptureOpen  bool             `json:"capture_open"`
	CaptureError string           `json:"capture_error,omitempty"`
	Seen         uint64           `json:"seen"`
	Answered     uint64           `json:"answered"`
	Dropped      uint64           `json:"dropped"`
	LastSeen     *time.Time       `json:"last_seen,omitempty"`
	LastAnswer   *time.Time       `json:"last_answer,omitempty"`
	LastAnnounce *time.Time       `json:"last_announce,omitempty"`
	Addrs        []api.NDPAddress `json:"addrs,omitempty"`
}

var toggleLong = map[bool]string{
	true: `Switch the NDP proxy of a network on for good: the host answers the router's
"who has this address" for the range of the network on its uplink from now on and again after every reboot. Use it when the
provider puts the prefix on the same segment as the host. Running it again changes nothing.`,
	false: `Switch the NDP proxy of a network off for good: the host stops answering
neighbor discovery for its range. Use it when the provider routes the prefix to this host. Running it again changes nothing.`,
}

func (a *app) ndpProxyCmd(d *kindDesc[api.Network]) *cobra.Command {
	root := &cobra.Command{
		Use:   "ndp-proxy",
		Short: "Answer neighbor discovery for a network on a shared segment",
		Long: `A network whose IPv6 prefix sits on a shared segment of your provider needs the host to answer the router's
"who has this address" for the addresses of its jails. The NDP proxy of the network does that: it answers for the first
1024 addresses of the network's IPv6 range (the whole range by default: 240 addresses) whether a jail runs or not, and
for the address of every running jail. A network whose prefix the provider routes to this host needs no proxy.

'enable' and 'disable' switch it for good: the setting is kept in the network and the proxy runs again after a reboot.
'status' shows what the proxy answered; 'watch' prints each question and answer as it happens.`,
		Example: `jailops network ndp-proxy enable web
jailops network ndp-proxy status web
jailops network ndp-proxy watch web`,
	}
	toggle := func(verb string, on bool) *cobra.Command {
		return &cobra.Command{
			Use:               verb + " NETWORK",
			Short:             map[bool]string{true: "Answer neighbor discovery for the network", false: "Stop answering neighbor discovery for the network"}[on],
			Long:              toggleLong[on],
			Example:           "jailops network ndp-proxy " + verb + " web",
			Args:              cobra.ExactArgs(1),
			ValidArgsFunction: completeKind(a, d),
			RunE:              func(cmd *cobra.Command, args []string) error { return a.ndpToggle(cmd, d, args[0], verb, on) },
		}
	}
	var count int
	status := &cobra.Command{
		Long: `Show whether the NDP proxy of the network is on, the addresses it answers for, how many questions it got and
answered, when the last question, answer and announcement were, and a table of the addresses that were asked for or have
a running jail: how often each was asked, answered and dropped, and what happened to the last question.

A jail without IPv6 outside is the first thing to look at here. An address with questions seen and none answered shows
why in the last column; an address the router never asked for does not appear.`,
		Use:               "status NETWORK",
		Short:             "Show what the proxy of a network answered",
		Example:           "jailops network ndp-proxy status web\njailops network ndp-proxy status web -o json",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKind(a, d),
		RunE:              func(cmd *cobra.Command, args []string) error { return a.ndpStatus(cmd, d, args[0]) },
	}
	watch := &cobra.Command{
		Use:               "watch NETWORK",
		Short:             "Print each question the proxy gets and its answer, live",
		Long:              "Print each neighbor solicitation the proxy of the network gets, who asked for which address and what the proxy did with it, and each unsolicited announcement. It stays until Ctrl-C, or until --limit events were printed.",
		Example:           "jailops network ndp-proxy watch web\njailops network ndp-proxy watch web --limit 5 -o json",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKind(a, d),
		RunE:              func(cmd *cobra.Command, args []string) error { return a.ndpWatch(cmd, d, args[0], count) },
	}
	watch.Flags().IntVar(&count, "limit", 0, "stop after this many events")
	root.AddCommand(toggle("enable", true), toggle("disable", false), status, watch)
	return root
}

// ndpMark is the marker of the IPv6 column of the network list: the network has the NDP proxy switched on.
func ndpMark(v6 *api.IPv6Net) string {
	if v6 != nil && v6.NDPProxy {
		return " (ndp)"
	}
	return ""
}

func ndpSubnet(n api.Network) (netip.Prefix, error) {
	if n.Spec.IPv6 == nil || n.Spec.IPv6.Subnet == "" {
		return netip.Prefix{}, usagef("network %s has no IPv6 prefix: the NDP proxy answers for IPv6 addresses (give one with 'jailops network set %s --ipv6 PREFIX')", n.Metadata.Name, n.Metadata.Name)
	}
	return netip.ParsePrefix(n.Spec.IPv6.Subnet)
}

func (a *app) ndpToggle(cmd *cobra.Command, d *kindDesc[api.Network], name, verb string, on bool) error {
	c, err := a.connect(cmd.Context())
	if err != nil {
		return err
	}
	n, etag, err := d.get(cmd.Context(), c, name)
	if err != nil {
		return err
	}
	if _, err := ndpSubnet(n); err != nil {
		return err
	}
	if on && n.Spec.Uplink == "" {
		return usagef("network %s has no uplink: the NDP proxy answers on the uplink's segment (set one with 'jailops network set %s --uplink IFACE')", name, name)
	}
	if n.Spec.IPv6.NDPProxy == on {
		a.say("network/%s unchanged\n", name)
		return nil
	}
	next := n
	v6 := *n.Spec.IPv6
	v6.NDPProxy = on
	next.Spec.IPv6 = &v6
	if ok, err := a.dry(verb+" ndp-proxy of network "+name, document(d.kind, *d.meta(&next), d.specOf(&next))); ok || err != nil {
		return err
	}
	out, err := d.update(cmd.Context(), c, name, next, etag)
	if err != nil {
		return err
	}
	if err := a.finishOp(cmd.Context(), nil, d.noun, name, "updated", out); err != nil {
		return err
	}
	if d.updated != nil {
		d.updated(a, out)
	}
	if on {
		a.say("network/%s: the NDP proxy answers for %s on %s\n", name, out.Spec.IPv6.Range, out.Spec.Uplink)
	} else {
		a.say("network/%s: the NDP proxy is off; the prefix %s must be routed to this host by the provider\n", name, out.Spec.IPv6.Subnet)
	}
	return nil
}

func (a *app) ndpStatus(cmd *cobra.Command, d *kindDesc[api.Network], name string) error {
	c, err := a.connect(cmd.Context())
	if err != nil {
		return err
	}
	n, _, err := d.get(cmd.Context(), c, name)
	if err != nil {
		return err
	}
	pfx, err := ndpSubnet(n)
	if err != nil {
		return err
	}
	v := ndpView{Network: name, Enabled: n.Spec.IPv6.NDPProxy, Uplink: n.Spec.Uplink, Prefix: pfx.String(), Range: n.Spec.IPv6.Range}
	if v.Enabled {
		st, err := c.NDPStatus(cmd.Context())
		if err != nil {
			return err
		}
		for _, nn := range st.Networks {
			if nn.Name == name {
				v.Answers, v.BeyondLimit = nn.Answers, nn.Beyond
			}
		}
		for _, u := range st.Uplinks {
			if u.Uplink != n.Spec.Uplink {
				continue
			}
			v.CaptureOpen, v.CaptureError = u.Open, u.Error
			for _, ad := range u.Addrs {
				ip, err := netip.ParseAddr(ad.Address)
				if err != nil || !pfx.Contains(ip) {
					continue
				}
				v.Seen += ad.Seen
				v.Answered += ad.Answered
				v.Dropped += ad.Dropped
				v.LastSeen = laterOf(v.LastSeen, ad.LastSeen)
				v.LastAnswer = laterOf(v.LastAnswer, ad.LastAnswer)
				v.LastAnnounce = laterOf(v.LastAnnounce, ad.LastAnnounce)
				if ad.Seen > 0 || ad.Interface != "" || ad.LastError != "" {
					v.Addrs = append(v.Addrs, ad)
				}
			}
		}
		sort.Slice(v.Addrs, func(i, j int) bool { return v.Addrs[i].Address < v.Addrs[j].Address })
	}
	switch a.format {
	case fmtJSON, fmtYAML:
		return a.emit(v, nil, nil, nil)
	case fmtCSV, fmtName:
		return usagef("-o csv and -o name are not available for this command")
	}
	w := a.env.Stdout
	now := a.env.Now()
	ago := func(t *time.Time) string {
		if t == nil {
			return "never"
		}
		return age(now, *t) + " ago"
	}
	fprintf(w, "Network:      %s\n", v.Network)
	if !v.Enabled {
		fprintf(w, "NDP proxy:    disabled ('jailops network ndp-proxy enable %s' turns it on)\n", name)
		return nil
	}
	state := "enabled, answering"
	switch {
	case !v.CaptureOpen && v.CaptureError != "":
		state = "enabled, the capture of " + v.Uplink + " cannot be opened: " + v.CaptureError
	case !v.CaptureOpen:
		state = "enabled, the capture of " + v.Uplink + " is being opened"
	}
	fprintf(w, "NDP proxy:    %s\n", state)
	fprintf(w, "Uplink:       %s\n", v.Uplink)
	fprintf(w, "Prefix:       %s\n", v.Prefix)
	more := ""
	if v.BeyondLimit {
		more = fmt.Sprintf("; the range is larger than the %d addresses the proxy answers for, the ones above are answered while their jail runs", api.AnswerLimit6)
	}
	fprintf(w, "Answers for:  %d addresses of %s%s\n", v.Answers, v.Range, more)
	fprintf(w, "Questions:    %d seen, %d answered, %d dropped\n", v.Seen, v.Answered, v.Dropped)
	fprintf(w, "Last question %s, last answer %s, last announcement %s\n", ago(v.LastSeen), ago(v.LastAnswer), ago(v.LastAnnounce))
	if len(v.Addrs) == 0 {
		fprintln(w, "\nNo address has been asked for yet.")
		return nil
	}
	rows := [][]string{}
	for _, ad := range v.Addrs {
		last := "-"
		if ad.LastSeen != nil {
			last = age(now, *ad.LastSeen) + " ago from " + ad.LastFrom + ": " + ad.LastVerdict
		}
		if ad.LastError != "" {
			last += " [send: " + ad.LastError + "]"
		}
		rows = append(rows, []string{ad.Address, dash(ad.Interface), fmt.Sprint(ad.Seen), fmt.Sprint(ad.Answered), fmt.Sprint(ad.Dropped), last})
	}
	fprintln(w)
	return a.emit(nil, nil, func() ([]string, [][]string) {
		return []string{"ADDRESS", "JAIL LINK", "SEEN", "ANSWERED", "DROPPED", "LAST QUESTION"}, rows
	}, nil)
}

func laterOf(a, b *time.Time) *time.Time {
	if b == nil || a != nil && !b.After(*a) {
		return a
	}
	return b
}

func (a *app) ndpWatch(cmd *cobra.Command, d *kindDesc[api.Network], name string, count int) error {
	c, err := a.connect(cmd.Context())
	if err != nil {
		return err
	}
	n, _, err := d.get(cmd.Context(), c, name)
	if err != nil {
		return err
	}
	pfx, err := ndpSubnet(n)
	if err != nil {
		return err
	}
	if !n.Spec.IPv6.NDPProxy {
		return usagef("the NDP proxy of network %s is off ('jailops network ndp-proxy enable %s' turns it on)", name, name)
	}
	w := a.env.Stdout
	var after uint64
	first := true
	printed := 0
	for {
		ev, err := c.NDPEvents(cmd.Context(), after)
		if err != nil {
			return err
		}
		if ev.Last < after {
			// the daemon restarted and numbers its events from the start again: show what it has now
			after = 0
			continue
		}
		evs := ev.Events
		if first && count == 0 && len(evs) > 20 {
			evs = evs[len(evs)-20:]
		}
		for _, e := range evs {
			ip, err := netip.ParseAddr(e.Target)
			if err != nil || !pfx.Contains(ip) {
				continue
			}
			if a.format == fmtJSON {
				if err := a.emit(e, nil, nil, nil); err != nil {
					return err
				}
			} else {
				who := ""
				if e.From != "" {
					who = " from " + e.From
				}
				what := "NS who-has"
				if e.Kind == "announce" {
					what = "NA announce"
				}
				fprintln(w, fmt.Sprintf("%s %s %s%s: %s", e.Time.Local().Format("15:04:05"), what, e.Target, who, e.Verdict))
			}
			printed++
			if count > 0 && printed >= count {
				return nil
			}
		}
		if ev.Last > after {
			after = ev.Last
		}
		first = false
		select {
		case <-cmd.Context().Done():
			return nil
		case <-time.After(500 * time.Millisecond):
		}
	}
}
