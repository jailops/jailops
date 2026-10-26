package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// labelCmd is 'jailops label JAIL KEY=VALUE... KEY-': labels select jails for backup policies and rollouts.
func (a *app) labelCmd() *cobra.Command {
	d := jailDesc()
	return &cobra.Command{
		Use:   "label JAIL KEY=VALUE... | KEY-",
		Short: "Set or remove labels of a jail",
		Long: `Labels are KEY=VALUE pairs on a jail. A backup policy selects the jails it backs up by
label, and so do rollouts. Write KEY=VALUE to set a label and KEY- to remove it; several may be
given at once and are applied together. With no label, the command lists the labels of the jail.`,
		Example:           "jailops label web backup=yes tier=frontend\njailops label web tier-\njailops label web",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeKind(a, d),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, changes := args[0], args[1:]
			set := map[string]string{}
			var drop []string
			for _, c := range changes {
				if k, ok := strings.CutSuffix(c, "-"); ok && !strings.Contains(c, "=") {
					if k == "" {
						return usagef("label %q: a key is needed before the dash", c)
					}
					drop = append(drop, k)
					continue
				}
				k, v, ok := strings.Cut(c, "=")
				if !ok || k == "" {
					return usagef("label %q must be KEY=VALUE, or KEY- to remove it", c)
				}
				set[k] = v
			}
			if len(changes) > 0 {
				if ok, err := a.dry("change the labels of jail "+name, map[string]any{"set": set, "remove": drop}); ok || err != nil {
					return err
				}
			}
			c, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			j, etag, err := c.Jails().GetETag(cmd.Context(), name)
			if err != nil {
				return err
			}
			if len(changes) > 0 {
				labels := map[string]string{}
				for k, v := range j.Metadata.Labels {
					labels[k] = v
				}
				for k, v := range set {
					labels[k] = v
				}
				for _, k := range drop {
					delete(labels, k)
				}
				j.Metadata.Labels = labels
				if len(labels) == 0 {
					j.Metadata.Labels = nil
				}
				if j, err = c.Jails().Update(cmd.Context(), name, j, etag); err != nil {
					return err
				}
			}
			keys := make([]string, 0, len(j.Metadata.Labels))
			for k := range j.Metadata.Labels {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			if a.format != fmtTable {
				return a.emit(j.Metadata.Labels, nil, nil, func() []string { return []string{name} })
			}
			for _, k := range keys {
				fprintf(a.env.Stdout, "%s=%s\n", k, j.Metadata.Labels[k])
			}
			if len(changes) > 0 {
				fprintf(a.env.Stderr, "%s\n", fmt.Sprintf("jail/%s labels updated", name))
			}
			return nil
		},
	}
}
