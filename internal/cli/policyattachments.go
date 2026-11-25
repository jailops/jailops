package cli

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

// policyAttachment is one row of `firewall policy attachments`: one policy of one jail, in the position pf renders it.
type policyAttachment struct {
	Project string `json:"project"`
	Jail    string `json:"jail"`
	Policy  string `json:"policy"`
	// Source is "project" (mandatory), "profile" (a profile of the jail gives the list) or "jail" (its own list).
	Source     string `json:"source"`
	Profile    string `json:"profile,omitempty"`
	Position   int    `json:"position"`
	DefaultIn  string `json:"default_in"`
	DefaultOut string `json:"default_out"`
}

var attachmentHead = []string{"JAIL", "POLICY", "SOURCE", "#", "DEFAULT-IN", "DEFAULT-OUT", "PROJECT"}

func (r policyAttachment) cells() []string {
	src := r.Source
	if r.Profile != "" {
		src += ":" + r.Profile
	}
	return []string{r.Jail, r.Policy, src, strconv.Itoa(r.Position), dash(r.DefaultIn), dash(r.DefaultOut), r.Project}
}

// policyAttachments is `firewall policy attachments`: the flat list of which policy is attached to which jail.
func (a *app) policyAttachments() *cobra.Command {
	var policies, jails, labels, columns []string
	var sortBy string
	cmd := &cobra.Command{
		Use:   "attachments",
		Short: "List which firewall policy is attached to which jail",
		Long: `One row per jail and policy, in the order the policies are rendered into the jail's anchor: the mandatory policies of the
project first (SOURCE project), then the policies of the jail itself (SOURCE jail) or, when the jail has none of its own, the
ones its profile gives (SOURCE profile:NAME). # is the position. A policy that is both mandatory and attached to a jail
shows twice, as it is rendered. DEFAULT-IN and DEFAULT-OUT are what the policy states, - when it states nothing.
A jail that is not in the list has no policy at all and is open to the jails of its project. --policy and --jail (each
repeatable) and -l select what is shown; -o wide adds PROJECT; -o name prints the jails.`,
		Example: "jailops firewall policy attachments\njailops firewall policy attachments --policy web-public\njailops firewall policy attachments -c jail,policy,source --sort policy\njailops firewall policy attachments -l env=prod -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := labelOpts(labels)
			if err != nil {
				return err
			}
			var pick []int
			for _, c := range columns {
				i, err := columnIndex(attachmentHead, c)
				if err != nil {
					return err
				}
				pick = append(pick, i)
			}
			sortCol := -1
			if sortBy != "" {
				if sortCol, err = columnIndex(attachmentHead, sortBy); err != nil {
					return err
				}
			}
			c, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			rows, bare, err := a.collectAttachments(cmd.Context(), c, policies, jails, opts, len(labels) > 0)
			if err != nil {
				return err
			}
			if sortCol >= 0 {
				sort.SliceStable(rows, func(i, j int) bool { return cellLess(rows[i].cells()[sortCol], rows[j].cells()[sortCol]) })
			}
			head := attachmentHead
			if len(pick) == 0 && !a.wide {
				head = head[:len(head)-1]
			}
			table := make([][]string, len(rows))
			for i, r := range rows {
				cells := r.cells()
				switch {
				case len(pick) > 0:
					sel := make([]string, len(pick))
					for k, p := range pick {
						sel[k] = cells[p]
					}
					cells = sel
				default:
					cells = cells[:len(head)]
				}
				table[i] = cells
			}
			if len(pick) > 0 {
				head = make([]string, len(pick))
				for k, p := range pick {
					head[k] = attachmentHead[p]
				}
			}
			if len(rows) == 0 {
				if len(policies)+len(jails)+len(labels) > 0 {
					a.empty = "No firewall policy attachments match."
				} else {
					a.empty = "No firewall policy is attached in project " + c.ProjectName() + ". Attach one with: jailops firewall policy attach POLICY JAIL..."
				}
			}
			if err := a.emitList(nonNil(rows), nil, func() ([]string, [][]string) { return head, table }, nil, func() []string {
				var names []string
				for _, r := range rows {
					if !slices.Contains(names, r.Jail) {
						names = append(names, r.Jail)
					}
				}
				return names
			}); err != nil {
				return err
			}
			if a.format == fmtTable && len(rows) > 0 && len(bare) > 0 && len(policies) == 0 {
				fprintf(a.env.Stderr, "No policy (open to their project): %s\n", strings.Join(bare, ", "))
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringArrayVar(&policies, "policy", nil, "only this policy (repeatable)")
	f.StringArrayVar(&jails, "jail", nil, "only this jail (repeatable)")
	f.StringArrayVarP(&labels, "selector", "l", nil, "only jails with these labels, key=value (repeatable)")
	f.StringSliceVarP(&columns, "columns", "c", nil, "columns of the table, comma separated: "+strings.Join(columnNames(attachmentHead), ", "))
	f.StringVar(&sortBy, "sort", "", "order the rows by this column")
	a.completeFlag(cmd, "policy", listNames(func(ctx context.Context, c *sdk.Client) iterNames {
		return namesOf(c.Firewall().Policies().List(ctx), func(p api.FirewallPolicy) string { return p.Metadata.Name })
	}))
	a.completeFlag(cmd, "jail", listNames(func(ctx context.Context, c *sdk.Client) iterNames {
		return namesOf(c.Jails().List(ctx), func(j api.Jail) string { return j.Metadata.Name })
	}))
	_ = cmd.RegisterFlagCompletionFunc("selector", a.completeLabels())
	enumFlag(cmd, "columns", columnNames(attachmentHead)...)
	enumFlag(cmd, "sort", columnNames(attachmentHead)...)
	return cmd
}

// collectAttachments builds the rows from the policies (used_by, mandatory), the jails (own list and profiles), the project's
// mandatory list and, for a jail that gets its list from a profile, the expanded jail. bare are the selected jails without any
// policy. A name in only or jailsOnly that does not exist is an error, unless a selector explains why it is not there.
func (a *app) collectAttachments(ctx context.Context, c *sdk.Client, only, jailsOnly []string, opts []sdk.ListOption, selected bool) (rows []policyAttachment, bare []string, err error) {
	pols, err := collect(c.Firewall().Policies().List(ctx))
	if err != nil {
		return nil, nil, err
	}
	byName := map[string]api.FirewallPolicy{}
	for _, p := range pols {
		byName[p.Metadata.Name] = p
	}
	for _, n := range only {
		if _, ok := byName[n]; !ok {
			return nil, nil, fmt.Errorf("firewall policy %q not found", n)
		}
	}
	js, err := collect(c.Jails().List(ctx, opts...))
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(js, func(i, j int) bool { return js[i].Metadata.Name < js[j].Metadata.Name })
	if !selected {
		for _, n := range jailsOnly {
			if !slices.ContainsFunc(js, func(j api.Jail) bool { return j.Metadata.Name == n }) {
				return nil, nil, fmt.Errorf("jail %q not found", n)
			}
		}
	}
	// the mandatory policies in the order of the project's list; when the project cannot be read, by name
	var mandatory []string
	if pr, err := c.Projects().Get(ctx, c.ProjectName()); err == nil {
		mandatory = pr.Spec.FirewallPolicies
	} else {
		for _, p := range pols {
			if p.Status.Mandatory {
				mandatory = append(mandatory, p.Metadata.Name)
			}
		}
		sort.Strings(mandatory)
	}
	mandatory = slices.DeleteFunc(slices.Clone(mandatory), func(n string) bool { _, ok := byName[n]; return !ok })
	for _, j := range js {
		name := j.Metadata.Name
		if len(jailsOnly) > 0 && !slices.Contains(jailsOnly, name) {
			continue
		}
		var jailRows []policyAttachment
		add := func(policy, source, profile string) {
			p := byName[policy]
			jailRows = append(jailRows, policyAttachment{Project: j.Metadata.Project, Jail: name, Policy: policy, Source: source, Profile: profile,
				Position: len(jailRows) + 1, DefaultIn: p.Spec.DefaultIn, DefaultOut: p.Spec.DefaultOut})
		}
		for _, n := range mandatory {
			add(n, "project", "")
		}
		own := j.Spec.Firewall.Policies
		switch {
		case len(own) > 0:
			for _, n := range own {
				if _, ok := byName[n]; ok {
					add(n, "jail", "")
				}
			}
		case len(j.Spec.Profiles) > 0:
			for _, n := range a.profilePolicies(ctx, c, name, pols) {
				if _, ok := byName[n.policy]; ok {
					add(n.policy, "profile", n.profile)
				}
			}
		}
		if len(jailRows) == 0 {
			bare = append(bare, name)
		}
		for _, r := range jailRows {
			if len(only) == 0 || slices.Contains(only, r.Policy) {
				rows = append(rows, r)
			}
		}
	}
	return rows, bare, nil
}

type profilePolicy struct{ policy, profile string }

// profilePolicies are the policies a jail gets from its profiles, in rendering order, from the expanded jail (the server
// merges the profiles, the host-level ones too). When that cannot be read, the policies that name the jail as a user are
// listed by name and the profile is not named.
func (a *app) profilePolicies(ctx context.Context, c *sdk.Client, jail string, pols []api.FirewallPolicy) []profilePolicy {
	if ex, err := c.Jails().Expanded(ctx, jail); err == nil {
		src := ex.Sources["firewall.policies"]
		if src == "jail" || src == "" {
			return nil
		}
		var out []profilePolicy
		for _, n := range ex.Jail.Spec.Firewall.Policies {
			out = append(out, profilePolicy{n, src})
		}
		return out
	}
	var out []profilePolicy
	for _, p := range pols {
		if !p.Status.Mandatory && slices.Contains(p.Status.UsedBy, jail) {
			out = append(out, profilePolicy{p.Metadata.Name, ""})
		}
	}
	return out
}
