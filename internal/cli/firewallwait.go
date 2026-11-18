package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

// firewallWait waits until the kernel has what the policies of a jail say, for scripts that change a policy and then
// depend on the change.
func (a *app) firewallWait() *cobra.Command {
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "wait JAIL...",
		Short: "Wait until pf has the rules the policies of jails say",
		Long: `Changing a policy stores it; the firewall of a jail changes in pf a moment later. wait returns when it has: the
FirewallApplied condition of every jail is true for the rules the policies render now (jailops info shows it), which means
that pf is enabled and evaluates the anchors of JailOps, the anchor holds those rules, its tables hold the
members of the address sets, and the connections of removed rules are ended. A stopped jail, and a jail without a
firewall, has nothing in pf to wait for.

Exit status: 0 applied; 1 the timeout ended first (the line names the reason: a policy that is not loaded yet, pf that does
not apply the anchors, states pf refused to end).`,
		Example: `jailops firewall policy attach web-access web && jailops firewall wait web
jailops firewall wait web db --timeout 2m`,
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeKind(a, a.jailDesc()),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			last := map[string]string{}
			for {
				var open []string
				for _, name := range args {
					state, why, err := firewallState(ctx, c, name)
					if err != nil {
						return err
					}
					if state != "" {
						open = append(open, name)
						last[name] = why
					}
				}
				if len(open) == 0 {
					a.say("firewall applied: %s\n", strings.Join(args, ", "))
					return nil
				}
				select {
				case <-ctx.Done():
					var parts []string
					for _, n := range open {
						parts = append(parts, fmt.Sprintf("jail/%s: %s", n, last[n]))
					}
					return fmt.Errorf("the firewall is not applied after %s: %s", timeout, strings.Join(parts, "; "))
				case <-time.After(500 * time.Millisecond):
				}
			}
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", time.Minute, "how long to wait")
	return cmd
}

// firewallState says whether the kernel has what the policies of a jail say: "" when it has (or when there is nothing
// to have), else a short state and its reason.
func firewallState(ctx context.Context, c *sdk.Client, name string) (state, why string, err error) {
	j, err := c.Jails().Get(ctx, name)
	if err != nil {
		return "", "", err
	}
	if j.Status.Power != api.PowerRunning {
		return "", "", nil
	}
	reach, err := c.Jails().Reach(ctx, name)
	if err != nil {
		return "", "", err
	}
	if reach.Error != "" || reach.RulesID == "" {
		return "", "", nil // no anchor to apply
	}
	why, pending := firewallPending(j.Status.Conditions, reach.RulesID)
	if pending {
		return "pending", why, nil
	}
	return "", "", nil
}

// firewallPending says whether the conditions of a jail confirm that pf has the rules with this id, and if not, why.
func firewallPending(conds []api.Condition, rulesID string) (why string, pending bool) {
	for _, cond := range conds {
		if cond.Type != api.ConditionFirewallApplied {
			continue
		}
		switch {
		case cond.Status != api.ConditionTrue:
			return cond.Reason + ": " + cond.Message, true
		case !strings.HasPrefix(cond.Message, "rules "+rulesID+" "):
			return "the policies render rules " + rulesID + " and pf was last confirmed to have another set", true
		}
		return "", false
	}
	return "the jail has no FirewallApplied condition yet: the next pass of the loop sets it", true
}
