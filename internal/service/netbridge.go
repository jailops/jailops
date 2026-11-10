package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"jailops.net/jailops/internal/ipam"
	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/internal/sys/ifconfig"
	"jailops.net/jailops/internal/validate"
	"jailops.net/jailops/sdk/api"
)

// The bridge of a network. A network's bridge has the network's name when that is a bridge name that is free, or the
// name given with --bridge, or a generated jo<N>; it is chosen once, when the network is created, and stored in the
// spec: renaming the network does not rename it, and it keeps its name until the network is deleted. The helper
// creates it under that name with JailOps's description, and changes only interfaces that carry it
// (docs/security-model.md, "Interfaces"), so a name that is taken on the host is refused here, before anything waits
// for it.

// hostIfaces reads the host's interfaces through the helper; nil when they cannot be read here (the reconcile loop
// reports a taken name later, as the network's ForeignBridge condition).
func (s *Service) hostIfaces(ctx context.Context) map[string]ifconfig.Iface {
	if s.d.Net == nil {
		return nil
	}
	list, err := s.d.Net.List(ctx)
	if err != nil {
		return nil
	}
	m := make(map[string]ifconfig.Iface, len(list))
	for _, i := range list {
		m[i.Name] = i
	}
	return m
}

// bridgeNames are the interfaces a network with bridge b makes: the bridge and the two ends of its gateway pair.
func bridgeNames(b string) []string {
	gm, gl3, err := ipam.GatewayNames(b)
	if err != nil {
		return []string{b}
	}
	return []string{b, gm, gl3}
}

// bridgeTaken says why the interfaces of bridge b cannot be the new network's: another network's bridge or gateway
// pair, or an interface on the host that is not JailOps's for this network. "" when b is free.
func (s *Service) bridgeTaken(network, b string, host map[string]ifconfig.Iface) string {
	objs, _ := s.netKind.list()
	mine := bridgeNames(b)
	for _, o := range objs {
		if o.Meta.Name == network {
			continue
		}
		for _, theirs := range bridgeNames(o.Spec.Bridge) {
			for _, n := range mine {
				if n == theirs {
					return fmt.Sprintf("the interface %s is used by network %q", n, o.Meta.Name)
				}
			}
		}
	}
	for k, n := range mine {
		i, ok := host[n]
		if !ok {
			continue
		}
		want := render.NetBridgeDesc(network)
		if k > 0 {
			want = render.NetGatewayDesc(network, k == 2)
		}
		if render.OwnsIface(i.Description, want) {
			continue // this network's own, left by an earlier network of the same name, or made and not described yet
		}
		if i.Description == "" {
			return fmt.Sprintf("the interface %s already exists on this host without a JailOps description", n)
		}
		return fmt.Sprintf("the interface %s already exists on this host and is not this network's (its description is %q)", n, i.Description)
	}
	return ""
}

// checkBridge refuses a bridge given with the network (--bridge) that cannot be the network's.
func (s *Service) checkBridge(ctx context.Context, network, b string) error {
	if why := s.bridgeTaken(network, b, s.hostIfaces(ctx)); why != "" {
		return api.Errorf(api.CodeConflict, "bridge %s cannot be used: %s", b, why).
			WithHint("choose another name with --bridge, or leave it out to have one chosen").WithDetail("bridge", b)
	}
	return nil
}

// defaultBridge chooses the bridge of a network created without one: the network's name when it can be one, else the
// lowest free jo<N>, with a note that says why.
func (s *Service) defaultBridge(ctx context.Context, network string) (bridge, note string, err error) {
	host := s.hostIfaces(ctx)
	why := ""
	switch {
	case len(network) > ipam.MaxBridgeName:
		why = fmt.Sprintf("the network name is longer than %d characters", ipam.MaxBridgeName)
	case validate.BridgeName(network) != nil:
		var ae *api.Error
		if errors.As(validate.BridgeName(network), &ae) {
			why = "the network name cannot be a bridge name: " + ae.Message
		}
	default:
		why = s.bridgeTaken(network, network, host)
	}
	if why == "" {
		return network, "", nil
	}
	b, err := s.nextBridge(host)
	if err != nil {
		return "", "", err
	}
	return b, fmt.Sprintf("bridge %s (%s); --bridge NAME chooses another", b, why), nil
}

// doctorBridges is the check network.bridges: an interface on the host with the name of a network's bridge or gateway
// pair that is not that network's keeps the network down (its ForeignBridge condition), because the helper changes
// only interfaces JailOps created. The message says what was found and what to do.
func (s *Service) doctorBridges(ctx context.Context, c *checkSet) {
	host := s.hostIfaces(ctx)
	if host == nil {
		c.add("network.bridges", api.DoctorUnknown, "the interfaces cannot be read here", "")
		return
	}
	objs, err := s.netKind.list()
	if err != nil {
		c.add("network.bridges", api.DoctorUnknown, err.Error(), "")
		return
	}
	var bad []string
	for _, o := range objs {
		for k, n := range bridgeNames(o.Spec.Bridge) {
			i, ok := host[n]
			if !ok {
				continue
			}
			want := render.NetBridgeDesc(o.Meta.Name)
			if k > 0 {
				want = render.NetGatewayDesc(o.Meta.Name, k == 2)
			}
			if render.OwnsIface(i.Description, want) {
				continue
			}
			switch {
			case i.Description == "":
				bad = append(bad, fmt.Sprintf("%s (network %s) exists without a JailOps description: JailOps did not create it, or it was interrupted before it described it", n, o.Meta.Name))
			case render.IsJailOpsDesc(i.Description):
				bad = append(bad, fmt.Sprintf("%s (network %s) belongs to another object of JailOps (%q)", n, o.Meta.Name, i.Description))
			default:
				bad = append(bad, fmt.Sprintf("%s (network %s) is someone else's (description %q)", n, o.Meta.Name, i.Description))
			}
		}
	}
	if len(bad) > 0 {
		c.add("network.bridges", api.DoctorFail, "interfaces that block a network: "+strings.Join(bad, "; "),
			"JailOps changes only interfaces it created and never takes one over by its name. Remove a leftover with `ifconfig NAME destroy` (the reconcile loop then creates the network's own), or delete the network and create it with another --bridge")
		return
	}
	c.add("network.bridges", api.DoctorOK, "every network's bridge and gateway pair is JailOps's or not made yet", "")
}
