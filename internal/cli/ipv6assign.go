package cli

import (
	"github.com/spf13/cobra"

	"jailops.net/jailops/sdk/api"
)

// ipv6AssignCmd is `network ipv6-assignment NETWORK [sequential|random]`: how a new jail gets its IPv6 address from the range.
func (a *app) ipv6AssignCmd(d *kindDesc[api.Network]) *cobra.Command {
	return &cobra.Command{
		Use:   "ipv6-assignment NETWORK [sequential|random]",
		Short: "Show or set how new jails get their IPv6 address",
		Long: `A new jail gets its IPv6 address from the network. 'sequential' (the default) takes the lowest free address of the
range, ::2, ::3 and so on, which is easy to read and to write rules for, and which is the first thing a scanner tries.
'random' fills the bits below the prefix at random, anywhere in the prefix of the network (with a /80 the address is one
of 2^48), so a jail is not found by trying the addresses one after the other. IPv4 is always sequential.

Either way the jail keeps its address for good, and any jail can be given an address of its own (--network NET=IPV4,IPV6 on
'create'), which is refused when the address is taken. The setting is kept in the network and applies to the jails made after it is
changed; the jails that exist keep what they have. Without the second argument the command shows the setting.`,
		Example: `jailops network ipv6-assignment web
jailops network ipv6-assignment web random
jailops network ipv6-assignment web sequential`,
		Args: cobra.RangeArgs(1, 2),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 1 {
				return []string{api.AssignSequential, api.AssignRandom}, cobra.ShellCompDirectiveNoFileComp
			}
			return completeKind(a, d)(cmd, args, toComplete)
		},
		RunE: func(cmd *cobra.Command, args []string) error { return a.ipv6Assign(cmd, d, args) },
	}
}

func (a *app) ipv6Assign(cmd *cobra.Command, d *kindDesc[api.Network], args []string) error {
	name := args[0]
	c, err := a.connect(cmd.Context())
	if err != nil {
		return err
	}
	n, etag, err := d.get(cmd.Context(), c, name)
	if err != nil {
		return err
	}
	if n.Spec.IPv6 == nil || n.Spec.IPv6.Subnet == "" {
		return usagef("network %s has no IPv6 prefix (give one with 'jailops network set %s --ipv6 PREFIX')", name, name)
	}
	cur := n.Spec.IPv6.Assignment
	if cur == "" {
		cur = api.AssignSequential
	}
	if len(args) == 1 {
		a.say("network/%s: %s\n", name, cur)
		return nil
	}
	want := args[1]
	if want != api.AssignSequential && want != api.AssignRandom {
		return usagef("assignment is %s or %s, not %q", api.AssignSequential, api.AssignRandom, want)
	}
	if cur == want {
		a.say("network/%s unchanged\n", name)
		return nil
	}
	next := n
	v6 := *n.Spec.IPv6
	v6.Assignment = want
	next.Spec.IPv6 = &v6
	if ok, err := a.dry("set the IPv6 assignment of network "+name+" to "+want, document(d.kind, *d.meta(&next), d.specOf(&next))); ok || err != nil {
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
	a.say("network/%s: new jails get a %s IPv6 address (prefix %s)\n", name, want, out.Spec.IPv6.Subnet)
	return nil
}
