package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

// stopAllCmd is what a host shutdown runs (the rc script jailops_shutdown): every running jail of every project, the
// jails that others depend on last.
func (a *app) stopAllCmd() *cobra.Command {
	var force bool
	var grace string
	cmd := &cobra.Command{
		Use:   "stop-all",
		Short: "Stop every running jail of every project you may use, dependents first",
		Long: `Stop every running jail of every project you may use, one after the other, each
with its own graceful shutdown (--grace sets how long a jail gets, --force skips it).
No project needs to be chosen.

The order is the reverse of the boot order ('jailops boot'): a jail stops before the
jails in its boot.depends, and among independent jails the one with the higher
boot.priority stops first. A jail that cannot be stopped is reported and the rest
are stopped anyway; the exit status says whether all stopped.

The host does this itself when it shuts down (the rc script jailops_shutdown, enabled
with jailopsd_enable), so that databases and other services get their shutdown and the
poststop hooks run, instead of every process of the jails being killed at once.`,
		Example: "jailops stop-all\njailops stop-all --grace 60s\njailops stop-all --dry-run",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var opts []sdk.ActionOption
			if force {
				opts = append(opts, sdk.Force())
			}
			if grace != "" {
				secs, err := parseGrace(grace)
				if err != nil {
					return err
				}
				opts = append(opts, sdk.ActionTimeout(secs))
			}
			c, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			type target struct{ project, name string }
			targets := map[string]target{}
			var nodes []api.BootNode
			for p, err := range c.Projects().List(cmd.Context()) {
				if err != nil {
					return err
				}
				proj := p.Metadata.Name
				for j, err := range c.Project(proj).Jails().List(cmd.Context()) {
					if err != nil {
						return err
					}
					if j.Status.Power != api.PowerRunning {
						continue
					}
					key := proj + "/" + j.Metadata.Name
					node := api.BootNode{Key: key, Boot: api.BootPolicy{Priority: j.Spec.Boot.Priority}}
					for _, d := range j.Spec.Boot.Depends {
						node.Boot.Depends = append(node.Boot.Depends, proj+"/"+d)
					}
					nodes = append(nodes, node)
					targets[key] = target{proj, j.Metadata.Name}
				}
			}
			order := api.StopOrder(nodes)
			if len(order) == 0 {
				fprintln(a.env.Stderr, "No jail is running.")
				return nil
			}
			failed := 0
			for _, key := range order {
				t := targets[key]
				if ok, err := a.dry("stop jail "+key, nil); ok || err != nil {
					if err != nil {
						return err
					}
					continue
				}
				h, err := handle(c.Project(t.project).Jails().Stop(cmd.Context(), t.name, opts...))
				if err == nil {
					err = a.finishOp(cmd.Context(), h, "jail", key, "stopped", nil)
				}
				if err != nil {
					failed++
					fprintln(a.env.Stderr, fmt.Sprintf("jail %s: %v", key, err))
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d of %d jails could not be stopped", failed, len(order))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "do not wait for a graceful shutdown")
	cmd.Flags().StringVar(&grace, "grace", "", "time each jail gets to shut down: seconds, or a duration such as 30s")
	return cmd
}
