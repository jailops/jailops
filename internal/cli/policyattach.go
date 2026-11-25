package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

// policyAttachCmds are `firewall policy attach` and `detach`: they add or take away ONE policy of a jail (or of the
// project's mandatory list) without writing the whole list; `attachments` lists them.
func (a *app) policyAttachCmds() []*cobra.Command {
	return []*cobra.Command{a.policyAttach(true), a.policyAttach(false), a.policyAttachments()}
}

func (a *app) policyAttach(attach bool) *cobra.Command {
	var mandatory bool
	use, short, long, example := "attach POLICY JAIL...", "Attach a firewall policy to jails",
		`Add POLICY to the policies of each jail, after the ones it has: only that name changes, the rest of the list stays as it
is, and a jail that has the policy already is left alone and named. The policies are rendered in the order of the list
(the project's mandatory ones first); 'jailops firewall show JAIL' lists them. With --mandatory POLICY goes into the
mandatory list of the project instead (global admins), and every jail of the project is rendered with it.`,
		"jailops firewall policy attach web-public web\njailops firewall policy attach ssh-office web db mail\njailops firewall policy attach default --mandatory --project shop"
	if !attach {
		use, short = "detach POLICY JAIL...", "Detach a firewall policy from jails"
		long = `Take POLICY out of the policies of each jail; the other policies stay. A jail that does not have it is left alone and
named. With --mandatory POLICY leaves the mandatory list of the project instead (global admins). The policy itself is
not deleted: 'jailops firewall policy delete' does that.`
		example = "jailops firewall policy detach web-public web\njailops firewall policy detach default --mandatory --project shop"
	}
	cmd := &cobra.Command{
		Use: use, Short: short, Long: long, Example: example,
		Args: func(cmd *cobra.Command, args []string) error {
			if mandatory {
				return cobra.ExactArgs(1)(cmd, args)
			}
			return cobra.MinimumNArgs(2)(cmd, args)
		},
		ValidArgsFunction: completeArgs(completeKind(a, policyDesc()), a.completeWith(func(ctx context.Context, c *sdk.Client, args []string) ([]string, error) {
			if len(args) == 0 {
				return nil, nil
			}
			jails, err := collect(c.Jails().List(ctx))
			var out []string
			for _, j := range jails {
				if slices.Contains(j.Spec.Firewall.Policies, args[0]) != attach {
					out = append(out, j.Metadata.Name)
				}
			}
			return out, err
		})),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			if attach {
				// a name that is not a policy of the project is refused here, before any jail is touched
				if _, err := c.Firewall().Policies().Get(cmd.Context(), args[0]); err != nil {
					return err
				}
			}
			if mandatory {
				return a.policyMandatory(cmd.Context(), c, args[0], attach)
			}
			var failed []string
			for _, name := range args[1:] {
				if err := a.policyOnJail(cmd.Context(), c, args[0], name, attach); err != nil {
					fprintf(a.env.Stderr, "jail/%s: %v\n", name, err)
					failed = append(failed, name)
				}
			}
			if len(failed) > 0 {
				return fmt.Errorf("not changed: %s", strings.Join(failed, ", "))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&mandatory, "mandatory", false, "change the mandatory policies of the project (global admins) instead of the policies of jails")
	return cmd
}

// policyOnJail adds or removes one policy of one jail. The server reads the list and writes it under the jail's lock, so
// a change made at the same time is not lost; the read here only decides what is said.
func (a *app) policyOnJail(ctx context.Context, c *sdk.Client, policy, jail string, attach bool) error {
	j, err := c.Jails().Get(ctx, jail)
	if err != nil {
		return err
	}
	has := slices.Contains(j.Spec.Firewall.Policies, policy)
	if has == attach {
		if attach {
			a.say("jail/%s: policy %s is attached already\n", jail, policy)
		} else {
			a.say("jail/%s: policy %s is not attached\n", jail, policy)
		}
		return nil
	}
	ch := api.ConfigChange{}
	verb := "attached"
	if attach {
		ch.Add = map[string]string{"firewall.policies": policy}
	} else {
		ch.Remove = map[string]string{"firewall.policies": policy}
		verb = "detached"
	}
	if ok, err := a.dry(fmt.Sprintf("%s policy %s of jail %s", strings.TrimSuffix(verb, "ed"), policy, jail), ch); ok || err != nil {
		return err
	}
	h, err := handle(c.Jails().ChangeConfig(ctx, jail, ch, false))
	if err != nil {
		return err
	}
	if !a.waiting() {
		return a.finishOp(ctx, h, "jail", jail, verb, nil)
	}
	v, _, err := a.await(ctx, h)
	if err != nil {
		return err
	}
	res, _ := v.(sdk.ConfigResult)
	if a.format != fmtTable {
		out := map[string]any{"jail": jail, "policy": policy, "result": verb, "policies": res.Jail.Spec.Firewall.Policies}
		if w := res.Jail.Status.Warnings; len(w) > 0 {
			out["warnings"] = w
		}
		return a.emit(out, nil, nil, func() []string { return []string{jail} })
	}
	a.say("jail/%s: policy %s %s\n", jail, policy, verb)
	for _, w := range res.Jail.Status.Warnings {
		fprintf(a.env.Stderr, "warning: %s\n", w)
	}
	return nil
}

// policyMandatory adds or removes one policy of the project's mandatory list.
func (a *app) policyMandatory(ctx context.Context, c *sdk.Client, policy string, attach bool) error {
	project := c.ProjectName()
	if project == "" {
		return usagef("name the project: --project NAME")
	}
	var had bool
	change := func(p *api.Project) {
		had = slices.Contains(p.Spec.FirewallPolicies, policy)
		switch {
		case attach && !had:
			p.Spec.FirewallPolicies = append(p.Spec.FirewallPolicies, policy)
		case !attach && had:
			p.Spec.FirewallPolicies = slices.DeleteFunc(p.Spec.FirewallPolicies, func(n string) bool { return n == policy })
		}
	}
	if ok, err := a.dry(fmt.Sprintf("change the mandatory policies of project %s", project), nil); ok || err != nil {
		return err
	}
	if err := sdk.Modify(ctx, c.Projects(), project, change); err != nil {
		return err
	}
	switch {
	case had && attach:
		a.say("project/%s: policy %s is mandatory already\n", project, policy)
	case !had && !attach:
		a.say("project/%s: policy %s is not mandatory\n", project, policy)
	case attach:
		a.say("project/%s: policy %s is mandatory now\n", project, policy)
	default:
		a.say("project/%s: policy %s is not mandatory any more\n", project, policy)
	}
	return nil
}
