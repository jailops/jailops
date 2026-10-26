package cli

import (
	"context"
	"fmt"
	"iter"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

func jailDesc() *kindDesc[api.Jail] {
	d := newDesc("jail", "jails", api.KindJail,
		func(j *api.Jail) *api.ObjectMeta { return &j.Metadata },
		func(j *api.Jail) *api.JailSpec { return &j.Spec })
	d.list = func(ctx context.Context, c *sdk.Client, o ...sdk.ListOption) iter.Seq2[api.Jail, error] {
		return c.Jails().List(ctx, o...)
	}
	d.get = func(ctx context.Context, c *sdk.Client, n string) (api.Jail, string, error) {
		return c.Jails().GetETag(ctx, n)
	}
	d.update = func(ctx context.Context, c *sdk.Client, n string, v api.Jail, etag string) (api.Jail, error) {
		return c.Jails().Update(ctx, n, v, etag)
	}
	d.create = func(ctx context.Context, c *sdk.Client, v api.Jail) (opHandle, api.Jail, error) {
		h, err := handle(c.Jails().Create(ctx, v))
		return h, api.Jail{}, err
	}
	d.del = func(ctx context.Context, c *sdk.Client, n string) (opHandle, error) {
		return handle(c.Jails().Delete(ctx, n))
	}
	d.head = []string{"NAME", "STATE", "IMAGE", "GEN", "ADDRESSES", "AGE"}
	d.style = stateStyle(1)
	d.row = func(now time.Time, j api.Jail) []string {
		addrs := slices.Clone(j.Status.Addresses)
		if len(addrs) == 0 {
			for _, n := range j.Spec.Network {
				for _, a := range []string{n.IPv4, n.IPv6} {
					if a != "" && a != "none" && a != "auto" {
						addrs = append(addrs, a)
					}
				}
			}
		}
		return []string{j.Metadata.Name, dash(string(j.Status.Power)), j.Spec.Image, dash(j.Status.Generation),
			joinOrDash(addrs), age(now, j.Metadata.CreatedAt)}
	}
	d.filters = func(cmd *cobra.Command) func(api.Jail) bool {
		var state, image, network string
		cmd.Flags().StringVar(&state, "state", "", "only jails in this state (running, stopped, error, ...)")
		cmd.Flags().StringVar(&image, "image", "", "only jails whose image reference starts with this")
		cmd.Flags().StringVar(&network, "network", "", "only jails attached to this network")
		return func(j api.Jail) bool {
			if state != "" && string(j.Status.Power) != state {
				return false
			}
			if image != "" && !strings.HasPrefix(j.Spec.Image, image) {
				return false
			}
			if network != "" && !slices.ContainsFunc(j.Spec.Network, func(n api.NetworkAttach) bool { return n.Network == network }) {
				return false
			}
			return true
		}
	}
	d.show = func(a *app, j api.Jail) error {
		w := a.env.Stdout
		m := j.Metadata
		fprintf(w, "Name:        %s\n", m.Name)
		fprintf(w, "UUID:        %s\n", dash(m.UUID))
		fprintf(w, "Generation:  %d\n", m.Generation)
		fprintf(w, "Age:         %s\n", age(a.env.Now(), m.CreatedAt))
		switch {
		case !j.Status.ProvisionedAt.IsZero():
			fprintf(w, "Provisioned: %s ago\n", age(a.env.Now(), j.Status.ProvisionedAt))
		case j.Spec.Provision != nil:
			fprintf(w, "Provisioned: no (run 'jailops provision %s')\n", m.Name)
		}
		if len(m.Labels) > 0 {
			var l []string
			for _, k := range sortedKeys(m.Labels) {
				l = append(l, k+"="+m.Labels[k])
			}
			fprintf(w, "Labels:      %s\n", strings.Join(l, ", "))
		}
		fprintln(w)
		spec, err := toYAML(j.Spec)
		if err != nil {
			return err
		}
		status, err := toYAML(j.Status)
		if err != nil {
			return err
		}
		sideBySide(w, "SPEC", string(spec), "STATUS", string(status))
		return nil
	}
	return &d
}

// sideBySide prints two blocks of text in two columns.
func sideBySide(w interface{ Write([]byte) (int, error) }, lh, left, rh, right string) {
	l := strings.Split(strings.TrimRight(left, "\n"), "\n")
	r := strings.Split(strings.TrimRight(right, "\n"), "\n")
	l = append([]string{lh, strings.Repeat("-", len(lh))}, l...)
	r = append([]string{rh, strings.Repeat("-", len(rh))}, r...)
	width := 0
	for _, s := range l {
		width = max(width, len([]rune(s)))
	}
	for i := 0; i < max(len(l), len(r)); i++ {
		var a, b string
		if i < len(l) {
			a = l[i]
		}
		if i < len(r) {
			b = r[i]
		}
		line := a
		if b != "" {
			line = a + strings.Repeat(" ", width-len([]rune(a))+4) + b
		}
		_, _ = fmt.Fprintln(w, strings.TrimRight(line, " "))
	}
}

func (a *app) jailCmd() *cobra.Command {
	d := jailDesc()
	jc := group("jail", "Create, inspect and control jails",
		`A jail is a FreeBSD jail on a ZFS-backed root cloned from an image generation,
with its own VNET network stack. These commands manage the specification
(create, edit, delete) and drive the lifecycle (start, stop, restart,
snapshots, rollback and upgrade).

Use --host to act on a host behind a hub.`,
		`jailops jail list --state running
jailops jail create web --image freebsd-15@stable --network lan --memory 2G
jailops jail show web
jailops jail start web`, "jails")
	crudCommands(a, jc, d, jailCreateLong, jailCreateExample, a.jailFlagBuild)
	jc.AddCommand(a.jailLifecycleSet(d)...)
	jc.AddCommand(a.jailExec(d), a.jailShell(d), a.provisionCmd(), a.jailConsole(d), a.jailSnapshotCmd(d), a.jailRollback(d), a.jailUpgrade(d), a.jailCopy(d), a.jailLogs(d))
	return jc
}

const jailCreateLong = `Create a jail from flags or from a file. A jail needs a project, a network and a
storage, and none of them is guessed: name them, or use a profile that carries them.
The project is --project, JAILOPS_PROJECT or the one chosen with 'project switch'.
The server allocates "auto" addresses from the network and writes them back into
the specification.`

const jailCreateExample = `jailops @P@ create web --project web --image freebsd-15@stable --network lan --storage default
jailops @P@ create db --project web --image freebsd-15@stable --network lan=10.20.0.50 --storage default --volume data:/var/db --memory 4G --autostart
jailops @P@ create nginx --project web --image freebsd-15@stable --profile web
jailops @P@ create -f web.yaml --project web`

func (a *app) jailLifecycleSet(d *kindDesc[api.Jail]) []*cobra.Command {
	return []*cobra.Command{
		a.jailLifecycle("start", "started", "Start jails", func(c *sdk.Client, ctx context.Context, n string, o ...sdk.ActionOption) (opHandle, error) {
			return handle(c.Jails().Start(ctx, n, o...))
		}, d),
		a.jailLifecycle("stop", "stopped", "Stop jails", func(c *sdk.Client, ctx context.Context, n string, o ...sdk.ActionOption) (opHandle, error) {
			return handle(c.Jails().Stop(ctx, n, o...))
		}, d),
		a.jailLifecycle("restart", "restarted", "Restart jails", func(c *sdk.Client, ctx context.Context, n string, o ...sdk.ActionOption) (opHandle, error) {
			return handle(c.Jails().Restart(ctx, n, o...))
		}, d),
	}
}

// addJailShortcuts puts the everyday jail commands at the top level, as in
// 'jailops list' and 'jailops start web'. The 'jail' group keeps them too.
func (a *app) addJailShortcuts(root *cobra.Command) {
	d := jailDesc()
	addList(a, root, d, "")
	addCreate(a, root, d, jailCreateLong, jailCreateExample, a.jailFlagBuild)
	addDelete(a, root, d)
	root.AddCommand(a.jailLifecycleSet(d)...)
	root.AddCommand(a.jailExec(d), a.jailShell(d), a.provisionCmd(), a.labelCmd())
}

func (a *app) jailFlagBuild(cmd *cobra.Command) func(string) (api.Jail, error) {
	var (
		image, layout, hostname, memory, disk, fwPolicy, strategy, window, storage string
		cpu, pids, priority                                                        int
		networks, volumes, hostMounts, envs, labels, depends, tags, profiles       []string
		autostart                                                                  bool
	)
	f := cmd.Flags()
	f.StringVar(&image, "image", "", "image reference: NAME@alias or NAME#gN (required without -f)")
	f.StringVar(&storage, "storage", "", "storage to place the jail on, one granted to the project (required unless a profile supplies it)")
	f.StringArrayVar(&profiles, "profile", nil, "profile to apply, later ones override earlier ones (repeatable)")
	f.StringVar(&layout, "layout", "", "root layout: thin or thick (default depends on the image)")
	f.StringVar(&hostname, "hostname", "", "hostname inside the jail (default: the jail name)")
	f.StringArrayVar(&networks, "network", nil, "attach a network granted to the project (repeatable; required unless a profile supplies one): NET (an address of every family it has), NET=IPV4 or NET=IPV4,IPV6 (an address, auto or none)")
	f.IntVar(&cpu, "cpu", 0, "CPU limit in cores")
	f.StringVar(&memory, "memory", "", "memory limit, for example 2G")
	f.StringVar(&disk, "disk", "", "disk quota, for example 20G")
	f.IntVar(&pids, "pids", 0, "process limit")
	f.StringArrayVar(&volumes, "volume", nil, "data volume NAME:MOUNT[:ro][:QUOTA] (repeatable)")
	f.StringArrayVar(&hostMounts, "host-mount", nil, "mount an existing host directory SOURCE:TARGET[:ro] (repeatable; global administrators only, and the host operator must have approved the directory)")
	f.StringVar(&fwPolicy, "firewall-policy", "", "firewall policy to apply")
	f.StringArrayVar(&tags, "tag", nil, "firewall tag of this jail (repeatable)")
	f.BoolVar(&autostart, "autostart", false, "start the jail when the host boots")
	f.IntVar(&priority, "boot-priority", 0, "boot order, lower starts first")
	f.StringArrayVar(&depends, "depends", nil, "jail that must start first (repeatable)")
	f.StringArrayVar(&envs, "env", nil, "environment variable KEY=VALUE (repeatable)")
	f.StringArrayVar(&labels, "label", nil, "label KEY=VALUE (repeatable)")
	f.StringVar(&strategy, "update-strategy", "", "how the jail follows its image: rebase, inplace or manual")
	f.StringVar(&window, "update-window", "", "update window, for example \"sun 03:00\"")
	a.completeFlag(cmd, "image", listNames(func(ctx context.Context, c *sdk.Client) iterNames { return namesOf(c.Images().List(ctx), imageName2) }))
	a.completeFlag(cmd, "network", listNames(func(ctx context.Context, c *sdk.Client) iterNames { return namesOf(c.Networks().List(ctx), netName) }))
	a.completeFlag(cmd, "storage", listNames(func(ctx context.Context, c *sdk.Client) iterNames { return namesOf(c.Storage().List(ctx), storName) }))
	return func(name string) (api.Jail, error) {
		if image == "" {
			return api.Jail{}, usagef("--image is required (or use -f FILE)")
		}
		spec := api.JailSpec{
			Image: image, Layout: api.Layout(layout), Hostname: hostname,
			Storage: storage, Profiles: profiles,
			Resources: api.Resources{CPU: cpu, Memory: memory, Disk: disk, PIDs: pids},
			Firewall:  api.FirewallAttach{Policy: fwPolicy, Tags: tags},
			Boot:      api.BootPolicy{Autostart: autostart, Priority: priority, Depends: depends},
			Update:    api.UpdatePolicy{Strategy: api.UpdateStrategy(strategy), Window: window},
		}
		for _, n := range networks {
			net, addrs, _ := strings.Cut(n, "=")
			att := api.NetworkAttach{Network: net} // no address given: one of every family the network has
			if addrs != "" {
				v4, v6, _ := strings.Cut(addrs, ",")
				att.IPv4, att.IPv6 = v4, v6
			}
			spec.Network = append(spec.Network, att)
		}
		for _, v := range volumes {
			p := strings.Split(v, ":")
			if len(p) < 2 {
				return api.Jail{}, usagef("volume %q must be NAME:MOUNT[:ro][:QUOTA]", v)
			}
			vm := api.VolumeMount{Name: p[0], Mount: p[1]}
			for _, extra := range p[2:] {
				if extra == "ro" {
					vm.ReadOnly = true
				} else {
					vm.Quota = extra
				}
			}
			spec.Volumes = append(spec.Volumes, vm)
		}
		for _, m := range hostMounts {
			p := strings.Split(m, ":")
			if len(p) < 2 || len(p) > 3 || (len(p) == 3 && p[2] != "ro") {
				return api.Jail{}, usagef("host mount %q must be SOURCE:TARGET[:ro]", m)
			}
			spec.HostMounts = append(spec.HostMounts, api.HostMount{Source: p[0], Target: p[1], ReadOnly: len(p) == 3})
		}
		if len(envs) > 0 {
			spec.Env = map[string]string{}
			for _, e := range envs {
				k, v, ok := strings.Cut(e, "=")
				if !ok || k == "" {
					return api.Jail{}, usagef("env %q must be KEY=VALUE", e)
				}
				spec.Env[k] = v
			}
		}
		j := sdk.NewJail(name, spec)
		if len(labels) > 0 {
			j.Metadata.Labels = map[string]string{}
			for _, l := range labels {
				k, v, ok := strings.Cut(l, "=")
				if !ok || k == "" {
					return api.Jail{}, usagef("label %q must be KEY=VALUE", l)
				}
				j.Metadata.Labels[k] = v
			}
		}
		return j, nil
	}
}

type lifecycleFn func(c *sdk.Client, ctx context.Context, name string, o ...sdk.ActionOption) (opHandle, error)

func (a *app) jailLifecycle(verb, past, short string, fn lifecycleFn, d *kindDesc[api.Jail]) *cobra.Command {
	var force bool
	var grace int
	cmd := &cobra.Command{
		Use:   verb + " NAME...",
		Short: short,
		Long: strings.ToUpper(verb[:1]) + verb[1:] + ` one or more jails and wait for the operation to finish (--no-wait returns at once).
--force skips the graceful shutdown; --grace sets how many seconds the jail gets.`,
		Example:           "jailops jail " + verb + " web\njailops jail " + verb + " web db --no-wait\njailops --host host-b jail " + verb + " web",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeKind(a, d),
		RunE: func(cmd *cobra.Command, args []string) error {
			var opts []sdk.ActionOption
			if force {
				opts = append(opts, sdk.Force())
			}
			if grace > 0 {
				opts = append(opts, sdk.ActionTimeout(grace))
			}
			for _, name := range args {
				if ok, err := a.dry(verb+" jail "+name, nil); ok || err != nil {
					if err != nil {
						return err
					}
					continue
				}
				c, err := a.connect(cmd.Context())
				if err != nil {
					return err
				}
				h, err := fn(c, cmd.Context(), name, opts...)
				if err != nil {
					return err
				}
				if err := a.finishOp(cmd.Context(), h, "jail", name, past, nil); err != nil {
					return err
				}
			}
			return nil
		},
	}
	if verb != "start" {
		cmd.Flags().BoolVar(&force, "force", false, "do not wait for a graceful shutdown")
		cmd.Flags().IntVar(&grace, "grace", 0, "seconds the jail gets to shut down")
	}
	return cmd
}

func (a *app) jailConsole(d *kindDesc[api.Jail]) *cobra.Command {
	var o execOpts
	cmd := &cobra.Command{
		Use:   "console NAME",
		Short: "Open an interactive console in a jail",
		Long: `Open an interactive terminal in a running jail over the console session
protocol that the web console uses: plain HTTPS requests plus an event stream,
no connection upgrade, so it also works through proxies that cannot upgrade.
The local terminal is switched to raw mode for the session and window size
changes are forwarded. The session is audited and ends when the shell exits,
at the idle or maximum time of the server, or when you close it.

It needs the same rights as 'jailops exec'. Prefer 'jailops shell' when the
connection can be upgraded: it carries binary input and one request per
keystroke is avoided. The console carries input as UTF-8 text.

The shell is chosen as for 'jailops shell': --shell, then the jail's "shell"
key, then /bin/sh. When stdin is not a terminal the input is sent as it is and
Ctrl-D (end of input) is sent when it ends.`,
		Example:           "jailops jail console web\njailops jail console web --shell /usr/local/bin/bash",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKind(a, d),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runConsole(cmd.Context(), args[0], o)
		},
	}
	cmd.Flags().StringVar(&o.shellPath, "shell", "", "shell to run, a path inside the jail (default: the jail's shell key, else /bin/sh)")
	cmd.Flags().StringVarP(&o.user, "user", "u", "", "run as this user of the jail")
	return cmd
}

func (a *app) jailSnapshotCmd(d *kindDesc[api.Jail]) *cobra.Command {
	sc := group("snapshot", "Create, list and delete jail snapshots",
		`A snapshot is a consistent ZFS snapshot across all datasets of a jail. It is the
target of 'jailops jail rollback --data' and the source of backups. Snapshots
cost almost nothing until data changes; the written column shows how much has
changed since each one.`,
		`jailops jail snapshot create web before-upgrade -m "before 15.1"
jailops jail snapshot list web
jailops jail snapshot delete web before-upgrade`, "snap", "snapshots")
	var comment string
	create := &cobra.Command{
		Use:               "create JAIL [SNAPSHOT]",
		Short:             "Take a snapshot of a jail",
		Long:              "Take a snapshot of all datasets of a jail. Without SNAPSHOT the server chooses a name.",
		Example:           "jailops jail snapshot create web\njailops jail snapshot create web before-upgrade -m \"before 15.1\"",
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: completeKind(a, d),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) == 2 {
				name = args[1]
			}
			if ok, err := a.dry("snapshot jail "+args[0], map[string]string{"name": name, "comment": comment}); ok || err != nil {
				return err
			}
			c, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			op, err := c.Jails().Snapshots(args[0]).Create(cmd.Context(), name, comment)
			h, err := handle(op, err)
			if err != nil {
				return err
			}
			if !a.waiting() {
				return a.finishOp(cmd.Context(), h, "snapshot", name, "created", nil)
			}
			v, _, err := a.await(cmd.Context(), h)
			if err != nil {
				return err
			}
			sn, _ := v.(api.JailSnapshot)
			return a.emitOne(sn, "snapshot", sn.Metadata.Name, "created")
		},
	}
	create.Flags().StringVarP(&comment, "message", "m", "", "comment stored with the snapshot")
	list := &cobra.Command{
		Use:               "list JAIL",
		Aliases:           []string{"ls"},
		Short:             "List the snapshots of a jail",
		Long:              "List the snapshots of a jail, oldest first, with the bytes written to its data datasets since each one.",
		Example:           "jailops jail snapshot list web\njailops jail snapshot list web -o json",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKind(a, d),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			items, err := collect(c.Jails().Snapshots(args[0]).List(cmd.Context()))
			if err != nil {
				return err
			}
			return a.emit(nonNil(items), nil, func() ([]string, [][]string) {
				var rows [][]string
				for _, s := range items {
					rows = append(rows, []string{s.Metadata.Name, humanBytes(s.Status.Written), age(a.env.Now(), s.Metadata.CreatedAt), dash(s.Spec.Comment)})
				}
				return []string{"NAME", "WRITTEN", "AGE", "COMMENT"}, rows
			}, func() []string {
				var n []string
				for _, s := range items {
					n = append(n, s.Metadata.Name)
				}
				return n
			})
		},
	}
	del := &cobra.Command{
		Use:               "delete JAIL SNAPSHOT...",
		Aliases:           []string{"rm"},
		Short:             "Delete snapshots of a jail",
		Long:              "Delete snapshots of a jail after confirmation (-y skips it). A rollback to a deleted snapshot is no longer possible.",
		Example:           "jailops jail snapshot delete web before-upgrade\njailops jail snapshot delete -y web old1 old2",
		Args:              cobra.MinimumNArgs(2),
		ValidArgsFunction: completeKind(a, d),
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, sn := range args[1:] {
				if ok, err := a.dry("delete snapshot "+sn+" of jail "+args[0], nil); ok || err != nil {
					if err != nil {
						return err
					}
					continue
				}
				if err := a.confirm(fmt.Sprintf("Delete snapshot %q of jail %q?", sn, args[0])); err != nil {
					return err
				}
				c, err := a.connect(cmd.Context())
				if err != nil {
					return err
				}
				h, err := handle(c.Jails().Snapshots(args[0]).Delete(cmd.Context(), sn))
				if err != nil {
					return err
				}
				if err := a.finishOp(cmd.Context(), h, "snapshot", sn, "deleted", nil); err != nil {
					return err
				}
			}
			return nil
		},
	}
	sc.AddCommand(create, list, del)
	return sc
}

// emitOne prints one finished result, as finishOp does for a synchronous reply.
func (a *app) emitOne(v any, noun, name, verb string) error {
	if a.format == fmtTable {
		fprintf(a.env.Stdout, "%s/%s %s\n", noun, name, verb)
		return nil
	}
	return a.emit(v, nil, nil, func() []string { return []string{name} })
}

func (a *app) jailRollback(d *kindDesc[api.Jail]) *cobra.Command {
	var root, data, all, confirmLoss bool
	var snapshot string
	cmd := &cobra.Command{
		Use:   "rollback NAME",
		Short: "Roll a jail back: its root, its data, or both",
		Long: `Roll a jail back to an earlier state.

  --root  (default) returns only the OS root to the previous image generation.
          Application data is untouched.
  --data  rolls the data datasets back to a snapshot. Everything written since
          that snapshot is discarded for good.
  --all   does both.

Before --data or --all the command shows how many bytes were written since the
snapshot, which is what would be lost, and asks you to type the jail name. In a
script, pass --confirm-data-loss once you have decided; -y does not apply to
data loss. Without --snapshot the most recent snapshot is used.`,
		Example: `jailops jail rollback web
jailops jail rollback web --data --snapshot before-upgrade
jailops jail rollback web --all --confirm-data-loss
jailops jail rollback web --data --dry-run`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKind(a, d),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			n := 0
			for _, b := range []bool{root, data, all} {
				if b {
					n++
				}
			}
			if n > 1 {
				return usagef("choose one of --root, --data and --all")
			}
			scope := api.RollbackRoot
			if data {
				scope = api.RollbackData
			} else if all {
				scope = api.RollbackAll
			}
			ctx := cmd.Context()
			c, err := a.connect(ctx)
			if err != nil {
				return err
			}
			rb := api.JailRollback{Scope: scope, Snapshot: snapshot}
			if scope != api.RollbackRoot {
				sn, err := pickSnapshot(ctx, c, name, snapshot)
				if err != nil {
					return err
				}
				rb.Snapshot = sn.Metadata.Name
				fprintf(a.env.Stdout, "Rolling back %s of jail %s to snapshot %s (taken %s ago).\n",
					scope, name, sn.Metadata.Name, age(a.env.Now(), sn.Metadata.CreatedAt))
				fprintf(a.env.Stdout, "%s written since then would be discarded.\n", humanBytes(sn.Status.Written))
				rb.ConfirmDataLoss = true
				if ok, err := a.dry("roll back jail "+name, rb); ok || err != nil {
					return err
				}
				rb.ConfirmDataLoss = false
				warn := fmt.Sprintf("This destroys %s of data written to jail %q since snapshot %q. It cannot be undone.",
					humanBytes(sn.Status.Written), name, sn.Metadata.Name)
				if err := a.confirmTyped(warn, name, confirmLoss, "--confirm-data-loss"); err != nil {
					return err
				}
				rb.ConfirmDataLoss = true
			} else if ok, err := a.dry("roll back the root of jail "+name, rb); ok || err != nil {
				return err
			}
			h, err := handle(c.Jails().Rollback(ctx, name, rb))
			if err != nil {
				return err
			}
			return a.finishOp(ctx, h, "jail", name, "rolled back", nil)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&root, "root", false, "roll back only the OS root to the previous generation (default)")
	f.BoolVar(&data, "data", false, "roll the data datasets back to a snapshot, discarding newer writes")
	f.BoolVar(&all, "all", false, "roll back root and data")
	f.StringVar(&snapshot, "snapshot", "", "snapshot to roll back to (default: the latest)")
	f.BoolVar(&confirmLoss, "confirm-data-loss", false, "confirm that data written since the snapshot may be destroyed")
	return cmd
}

func pickSnapshot(ctx context.Context, c *sdk.Client, jail, name string) (api.JailSnapshot, error) {
	if name != "" {
		return c.Jails().Snapshots(jail).Get(ctx, name)
	}
	items, err := collect(c.Jails().Snapshots(jail).List(ctx))
	if err != nil {
		return api.JailSnapshot{}, err
	}
	if len(items) == 0 {
		return api.JailSnapshot{}, api.Errorf(api.CodeNotFound, "jail %q has no snapshots", jail).
			WithHint("take one first with 'jailops jail snapshot create " + jail + "'")
	}
	return items[len(items)-1], nil
}

func imageBase(ref string) string {
	if i := strings.IndexAny(ref, "@#"); i >= 0 {
		return ref[:i]
	}
	return ref
}

func (a *app) jailUpgrade(d *kindDesc[api.Jail]) *cobra.Command {
	var gen string
	cmd := &cobra.Command{
		Use:   "upgrade NAME",
		Short: "Move a jail to a newer image generation",
		Long: `Move a jail to a newer generation of its image. For a thin jail the root is
replaced and application data is kept; /etc is merged. The jail is restarted
by the server when needed.

With --dry-run the server checks the move and nothing changes; the command
prints the generations involved. Without --generation the newest generation of
the jail's image is used.`,
		Example:           "jailops jail upgrade web --dry-run\njailops jail upgrade web\njailops jail upgrade web --generation g4",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKind(a, d),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, name := cmd.Context(), args[0]
			c, err := a.connect(ctx)
			if err != nil {
				return err
			}
			j, err := c.Jails().Get(ctx, name)
			if err != nil {
				return err
			}
			target := gen
			if target == "" {
				gens, err := collect(c.Images().Generations(ctx, imageBase(j.Spec.Image)))
				if err == nil && len(gens) > 0 {
					target = fmt.Sprintf("g%d", gens[len(gens)-1].Number)
				}
			}
			up := api.JailUpgrade{Generation: gen, DryRun: a.dryRun}
			h, err := handle(c.Jails().Upgrade(ctx, name, up))
			if err != nil {
				return err
			}
			if a.dryRun {
				if _, _, err := a.await(ctx, h); err != nil {
					return err
				}
				return a.preview(fmt.Sprintf("upgrade jail %s from %s to %s", name, dash(j.Status.Generation), dash(target)), up)
			}
			return a.finishOp(ctx, h, "jail", name, "upgraded to "+dash(target), nil)
		},
	}
	cmd.Flags().StringVar(&gen, "generation", "", "target generation, for example g4 (default: the newest)")
	return cmd
}

// preview prints a dry-run result the server has already checked.
func (a *app) preview(action string, request any) error {
	if a.format == fmtJSON || a.format == fmtYAML {
		return a.emit(map[string]any{"dry_run": true, "action": action, "request": request}, nil, nil, nil)
	}
	fprintf(a.env.Stdout, "dry-run: would %s\n", action)
	return nil
}

func (a *app) jailCopy(d *kindDesc[api.Jail]) *cobra.Command {
	var keepIP bool
	cmd := &cobra.Command{
		Use:   "copy SOURCE NAME",
		Short: "Create a jail with the specification of another",
		Long: `Create a new jail from the specification and labels of an existing one. Fixed
addresses are replaced by "auto" so the copy does not collide with the source
(--keep-ip keeps them, which only works when the source is on another host).

Only the specification is copied, not the data in the source's datasets. To
copy data, snapshot or back up the source and restore it under a new name with
'jailops backup restore --as'.`,
		Example:           "jailops jail copy web web2\njailops --host host-b jail copy web web-b",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeKind(a, d),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.connect(ctx)
			if err != nil {
				return err
			}
			src, err := c.Jails().Get(ctx, args[0])
			if err != nil {
				return err
			}
			nj := sdk.NewJail(args[1], src.Spec)
			nj.Metadata.Labels = src.Metadata.Labels
			nj.Spec.Network = slices.Clone(src.Spec.Network)
			for i := range nj.Spec.Network {
				n := &nj.Spec.Network[i]
				n.MAC = ""
				if !keepIP {
					for _, p := range []*string{&n.IPv4, &n.IPv6} {
						if *p != "" && *p != "none" {
							*p = "auto"
						}
					}
				}
			}
			if ok, err := a.dry("copy jail "+args[0]+" to "+args[1], document(api.KindJail, nj.Metadata, nj.Spec)); ok || err != nil {
				return err
			}
			h, err := handle(c.Jails().Create(ctx, nj))
			if err != nil {
				return err
			}
			return a.finishOp(ctx, h, "jail", args[1], "created", nil)
		},
	}
	cmd.Flags().BoolVar(&keepIP, "keep-ip", false, "keep fixed addresses instead of allocating new ones")
	return cmd
}

func (a *app) jailLogs(d *kindDesc[api.Jail]) *cobra.Command {
	var count int
	var since string
	cmd := &cobra.Command{
		Use:   "logs NAME",
		Short: "Follow the log events of a jail",
		Long: `Stream the log events the server publishes for a jail until interrupted.

The server keeps no log history for this command: it shows events from now on,
or from an event id with --since-id when the server still retains them.`,
		Example:           "jailops jail logs web\njailops jail logs web --since-id 120 --count 50",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKind(a, d),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.streamEvents(cmd.Context(), []api.EventType{api.EventLogging}, args[0], since, count)
		},
	}
	cmd.Flags().IntVar(&count, "count", 0, "stop after this many events")
	cmd.Flags().StringVar(&since, "since-id", "", "resume after this event id")
	return cmd
}
