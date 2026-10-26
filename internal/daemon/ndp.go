package daemon

import (
	"context"
	"net/netip"
	"time"

	"jailops.net/jailops/internal/ndproxy"
	"jailops.net/jailops/internal/priv"
	"jailops.net/jailops/internal/reconcile"
	"jailops.net/jailops/internal/sys/ifconfig"
	"jailops.net/jailops/sdk/api"
)

// helperNDP is the neighbor discovery proxy of a daemon that is not root. It captures through a descriptor the
// helper passes (read-only and locked, filtered to neighbor discovery) and answers by asking the helper to send the
// advertisement, which the helper only does for what the operator approved.
type helperNDP struct {
	*ndproxy.Proxy
}

func newHelperNDP(host priv.Host, net ifconfig.Client, uplink string, logf func(format string, args ...any)) (reconcile.NDP, error) {
	h := &helperDevice{host: host, uplink: uplink}
	// Ask the helper once now: if the operator has not approved the uplink, or the capture cannot be made, the
	// reason reaches the network's status and the doctor instead of the proxy silently retrying.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f, err := host.NDPOpen(ctx, uplink, false)
	if err != nil {
		return nil, err
	}
	_ = f.Close()
	// 100 advertisements a second is far more than a link of jails needs and keeps a flood of solicitations from
	// filling the audit log; the helper has a hard limit of its own.
	p, err := ndproxy.New(ndproxy.Config{Uplink: uplink, MaxAnswersPerSecond: 100}, ndproxy.Deps{
		Capture: h, Injector: h, Routes: reconcileRoutes{net: net},
		Joiner: ndproxy.NewMulticastJoiner(), Clock: reconcile.SystemClock{}, Logf: logf,
	})
	if err != nil {
		return nil, err
	}
	return helperNDP{p}, nil
}

// helperDevice is the Capture and Injector of the helper-backed proxy.
type helperDevice struct {
	host   priv.Host
	uplink string
}

// Link implements ndproxy.Capture.
func (h *helperDevice) Link() (ndproxy.LinkInfo, error) { return ndproxy.SystemLink(h.uplink) }

// Open implements ndproxy.Capture.
func (h *helperDevice) Open(_ ndproxy.LinkInfo, promisc bool) (ndproxy.Stream, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f, err := h.host.NDPOpen(ctx, h.uplink, promisc)
	if err != nil {
		return nil, err
	}
	s, err := ndproxy.NewFileStream(f)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return s, nil
}

// Send implements ndproxy.Injector: the frame is an advertisement the proxy built; only what it says (target,
// requester) goes to the helper, which builds and sends its own.
func (h *helperDevice) Send(frame []byte) error {
	pkt, err := ndproxy.Parse(frame)
	if err != nil || pkt.Type != ndproxy.TypeNeighborAdvertisement {
		return api.Errorf(api.CodeInvalid, "the proxy sent something that is not an advertisement")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if pkt.Dst.IsMulticast() {
		return h.host.NDPAnswer(ctx, priv.NDPAnswerReq{Uplink: h.uplink, Target: pkt.Target.String(), Announce: true})
	}
	return h.host.NDPAnswer(ctx, priv.NDPAnswerReq{
		Uplink: h.uplink, Target: pkt.Target.String(), RequesterIP: pkt.Dst.String(), RequesterMAC: pkt.EthDst.String(),
	})
}

// reconcileRoutes leaves the /128 host routes to the reconcile loop, which installs them through the helper as part
// of the jail's network steps; the proxy only needs to know that the uplink is not a bridge member.
type reconcileRoutes struct{ net ifconfig.Client }

func (reconcileRoutes) Add(context.Context, netip.Addr, string) error    { return nil }
func (reconcileRoutes) Remove(context.Context, netip.Addr, string) error { return nil }

// EnsureUplinkNotMember implements ndproxy.Routes.
func (r reconcileRoutes) EnsureUplinkNotMember(ctx context.Context, uplink string) error {
	ifs, err := r.net.List(ctx)
	if err != nil {
		return err
	}
	for _, i := range ifs {
		for _, m := range i.Members {
			if m.Name == uplink {
				return api.Errorf(api.CodeConflict, "uplink %s is a member of bridge %s", uplink, i.Name).
					WithHint("remove the uplink from the bridge; the NDP proxy needs it to be a plain interface")
			}
		}
	}
	return nil
}
