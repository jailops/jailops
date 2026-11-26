package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

func (a *app) jailDesc() *kindDesc[api.Jail] {
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
	d.rename = func(ctx context.Context, c *sdk.Client, n string, req api.Rename, etag string) (*sdk.Operation[api.RenameResult], error) {
		return c.Jails().Rename(ctx, n, req, etag)
	}
	d.renameLong = renameLong("jail", `its datasets (snapshots go with them) and their
mountpoints, its records, its update history, the mark of its shell setup, and the boot
order of the jails that depend on it. The jail must be stopped: the kernel knows a running jail by its
name, and its mounts, interfaces and firewall anchor are set up under it. Its hostname does not change`)
	d.create = func(ctx context.Context, c *sdk.Client, v api.Jail) (opHandle, api.Jail, error) {
		h, err := handle(c.Jails().Create(ctx, v))
		return h, api.Jail{}, err
	}
	d.del = func(ctx context.Context, c *sdk.Client, n string) (opHandle, error) {
		return handle(c.Jails().Delete(ctx, n))
	}
	d.head = []string{"NAME", "STATE", "OS", "IMAGE", "GEN", "ADDRESSES", "EXPOSED", "AUTOSTART", "USED", "AGE"}
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
		return []string{j.Metadata.Name, dash(string(j.Status.Power)), dash(string(j.Spec.OS)), j.Spec.Image, dash(j.Status.ImageGeneration),
			joinOrDash(addrs), exposedCell(j), autostartCell(j), usedCell(j), age(now, j.Metadata.CreatedAt)}
	}
	// On a terminal: one address per line with its network, the generation
	// next to the image, and no column a long IPv6 address would crowd out.
	// A narrow terminal loses AGE, USED, IMAGE, OS, IPV6, IPV4, EXPOSED and AUTOSTART, in that order.
	d.brief = &listView[api.Jail]{
		head:  []string{"NAME", "STATE", "OS", "IPV4", "IPV6", "IMAGE", "EXPOSED", "AUTOSTART", "USED", "AGE"},
		style: stateStyle(1),
		drop:  []int{9, 8, 5, 2, 4, 3, 6, 7},
		row: func(now time.Time, j api.Jail) []string {
			var v4, v6 []string
			for _, n := range j.Spec.Network {
				if a := fixedAddr(n.IPv4); a != "" {
					v4 = append(v4, a+" ("+n.Network+")")
				}
				if a := fixedAddr(n.IPv6); a != "" {
					v6 = append(v6, a+" ("+n.Network+")")
				}
			}
			return []string{j.Metadata.Name, dash(string(j.Status.Power)), dash(string(j.Spec.OS)), linesOrDash(v4), linesOrDash(v6),
				imageWithGen(j.Spec.Image, j.Status.ImageGeneration, j.Status.FreeBSDVersion), exposedCell(j), autostartCell(j), usedCell(j), age(now, j.Metadata.CreatedAt)}
		},
	}
	// -v: the release installed in the jail, how long it has run and since when. STARTED goes first on a narrow terminal.
	d.more = &listView[api.Jail]{
		head: []string{"VERSION", "UPTIME", "STARTED"},
		drop: []int{2},
		row: func(now time.Time, j api.Jail) []string {
			up, started := "-", "-"
			if j.Status.Power == api.PowerRunning && !j.Status.StartedAt.IsZero() {
				up, started = uptime(now.Sub(j.Status.StartedAt)), j.Status.StartedAt.Local().Format("2006-01-02 15:04")
			}
			return []string{versionCell(j), up, started}
		},
	}
	d.moreHelp = `

-v (--verbose) adds VERSION, UPTIME and STARTED. VERSION is the release installed in a FreeBSD jail (status.freebsd_version)
and the image of a Linux jail; UPTIME and STARTED say how long a running jail has run and since when (local time), "-"
for a stopped jail or one the daemon has not seen start. -o wide shows them with every other column.`
	d.filters = func(cmd *cobra.Command) func() (func(api.Jail) bool, error) {
		var state, image, network string
		cmd.Flags().StringVar(&state, "state", "", "only jails in this state: "+strings.Join(powerStates, ", "))
		cmd.Flags().StringVar(&image, "image", "", "only jails whose image reference starts with this")
		cmd.Flags().StringVar(&network, "network", "", "only jails attached to this network")
		enumFlag(cmd, "state", powerStates...)
		a.completeFlag(cmd, "image", listNames(func(ctx context.Context, c *sdk.Client) iterNames { return namesOf(c.Images().List(ctx), imageName2) }))
		a.completeFlag(cmd, "network", listNames(func(ctx context.Context, c *sdk.Client) iterNames { return namesOf(c.Networks().List(ctx), netName) }))
		return func() (func(api.Jail) bool, error) {
			if state != "" && !slices.Contains(powerStates, state) {
				return nil, usagef("unknown state %q (use %s)", state, strings.Join(powerStates, ", "))
			}
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
			}, nil
		}
	}
	d.show = func(a *app, j api.Jail) error {
		var extra []string
		extra = append(extra, "Securelevel: "+securelevelText(j.Spec))
		if n := notesSummary(j.Spec.Notes, "jailops jail notes "+j.Metadata.Name); n != "" {
			extra = append(extra, "Notes:       "+n)
		}
		spec := j.Spec
		spec.Notes = "" // the summary above; the whole text is jailops jail notes
		return a.showObject(j.Metadata, extra, spec, j.Status)
	}
	d.afterCreate = a.jailStartFlag
	d.beforeDelete = a.jailForceFlag
	d.targets = a.jailTargets
	d.loss = jailLoss
	d.emptyHint = "jailops jail create NAME --image IMAGE --network NETWORK --storage STORAGE"
	d.prepare = a.jailDefaults
	return &d
}

// securelevelText is the securelevel a jail runs at, and where it comes from.
func securelevelText(s api.JailSpec) string {
	lvl, ok := api.EffectiveSecureLevel(s.OS, s.SecureLevel)
	switch {
	case !ok && s.SecureLevel == nil:
		return "the host's (the default of a Linux jail)"
	case !ok:
		return "the host's (securelevel -1)"
	case s.SecureLevel == nil:
		return fmt.Sprintf("%d (the default)", lvl)
	}
	return strconv.Itoa(lvl)
}

// jailLoss says what deleting a jail destroys: the sizes of its root and data, its volumes and the number of its
// snapshots, as the server reports them now. A running jail without --stop is refused here, before any question;
// a dry run reports it instead.
func jailLoss(cmd *cobra.Command, ctx context.Context, c *sdk.Client, name string) (string, error) {
	j, err := c.Jails().Get(ctx, name)
	if err != nil {
		return "", err
	}
	force, _ := cmd.Flags().GetBool("stop")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	running := j.Status.Power == api.PowerRunning
	// a dry run always shows the summary: the running state is part of it, not a reason to fail
	if running && !force && !dryRun {
		return "", api.Errorf(api.CodePreconditionFailed, "jail %q is running", name).
			WithHint("stop it first ('jailops stop " + name + "'), or pass --stop to stop it before the delete")
	}
	var parts []string
	u, err := c.Jails().Usage(ctx, name)
	switch {
	case err != nil:
		parts = append(parts, "sizes unknown (the server does not report them)")
	case u.Disk.Error != "":
		parts = append(parts, "sizes unknown ("+u.Disk.Error+")")
	default:
		parts = append(parts, "root "+humanBytes(u.Disk.RootBytes), "data "+humanBytes(u.Disk.DataBytes))
	}
	if snaps, err := collect(c.Jails().Snapshots(name).List(ctx)); err != nil {
		parts = append(parts, "snapshots unknown")
	} else {
		parts = append(parts, count(len(snaps), "snapshot", "snapshots"))
	}
	line := fmt.Sprintf("%s (%s): %s", name, dash(string(j.Status.Power)), strings.Join(parts, ", "))
	if len(j.Spec.Volumes) > 0 {
		var vols []string
		for _, v := range j.Spec.Volumes {
			vols = append(vols, v.Name)
		}
		line += "; its volumes stay: " + strings.Join(vols, ", ")
	}
	switch {
	case running && force:
		line += "; --stop stops it first"
	case running:
		line += "; the delete needs --stop to stop it first"
	}
	return line, nil
}

// jailDefaults gives a jail without a network or a storage (and without a profile, which may carry them) the
// project's only grant of that kind and says which it took.
func (a *app) jailDefaults(ctx context.Context, c *sdk.Client, j *api.Jail) error {
	if j.Spec.Image == "" {
		if err := a.defaultImage(ctx, c, j); err != nil {
			return err
		}
	}
	if len(j.Spec.Profiles) > 0 || (len(j.Spec.Network) > 0 && j.Spec.Storage != "") {
		return nil
	}
	project := a.conn.CurrentProject()
	p, err := c.Projects().Get(ctx, project)
	if err != nil {
		return err
	}
	if len(j.Spec.Network) == 0 {
		if n := p.Spec.SoleNetwork(); n != "" {
			j.Spec.Network = []api.NetworkAttach{{Network: n}}
			fprintf(a.env.Stderr, "network %s: the only network of project %s\n", n, project)
		}
	}
	if j.Spec.Storage == "" {
		if s := p.Spec.SoleStorage(); s != "" {
			j.Spec.Storage = s
			fprintf(a.env.Stderr, "storage %s: the only storage of project %s\n", s, project)
		}
	}
	return nil
}

// defaultImage gives a jail without an image the host's only image and says so, like the network and the storage; with
// none or several images it asks for one and lists them.
func (a *app) defaultImage(ctx context.Context, c *sdk.Client, j *api.Jail) error {
	images, err := collect(c.Images().List(ctx))
	if err != nil {
		return err
	}
	switch len(images) {
	case 0:
		return usagef("--image is required (or use -f FILE); this host has no image yet: jailops image create NAME --release RELEASE")
	case 1:
		j.Spec.Image = images[0].Metadata.Name
		fprintf(a.env.Stderr, "image %s: the only image\n", j.Spec.Image)
		return nil
	}
	var names []string
	for _, im := range images {
		names = append(names, im.Metadata.Name)
	}
	return usagef("--image is required (or use -f FILE): this host has %s", strings.Join(names, ", "))
}

// count is "1 snapshot", "3 snapshots".
func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// powerStates are the states 'list --state' accepts.
var powerStates = []string{string(api.PowerStopped), string(api.PowerStarting), string(api.PowerRunning), string(api.PowerStopping), string(api.PowerError), string(api.PowerLocked)}

// jailStartFlag registers --start on create and starts the new jail when it
// was given.
func (a *app) jailStartFlag(cmd *cobra.Command) func(ctx context.Context, name string) error {
	var start bool
	cmd.Flags().BoolVar(&start, "start", false, "start the jail once it is created")
	return func(ctx context.Context, name string) error {
		if !start {
			return nil
		}
		if ok, err := a.dry("start jail "+name, nil); ok || err != nil {
			return err
		}
		c, err := a.connect(ctx)
		if err != nil {
			return err
		}
		h, err := handle(c.Jails().Start(ctx, name))
		if err != nil {
			return err
		}
		return a.finishOp(ctx, h, "jail", name, "started", nil)
	}
}

// jailForceFlag registers --stop on delete and stops a running jail before
// it is deleted when the flag was given.
func (a *app) jailForceFlag(cmd *cobra.Command) func(ctx context.Context, name string) error {
	var force bool
	cmd.Flags().BoolVar(&force, "stop", false, "stop the jail first when it is running (the data-loss confirmation is still needed)")
	cmd.Long += "\nA running jail is refused unless --stop is given, which stops it first; --stop does not\nconfirm the data loss."
	cmd.Example += "\njailops @P@ delete --stop --confirm-data-loss web"
	return func(ctx context.Context, name string) error {
		if !force {
			return nil
		}
		if ok, err := a.dry("stop jail "+name+" when it is running", nil); ok || err != nil {
			return err
		}
		c, err := a.connect(ctx)
		if err != nil {
			return err
		}
		j, err := c.Jails().Get(ctx, name)
		if err != nil {
			return err
		}
		if j.Status.Power == api.PowerStopped {
			return nil
		}
		h, err := handle(c.Jails().Stop(ctx, name))
		if err != nil {
			return err
		}
		if _, _, err := a.await(ctx, h); err != nil {
			return err
		}
		a.say("jail/%s stopped\n", name)
		return nil
	}
}

// jailTargets registers -l/--selector and --all on a command that acts on
// several jails and returns the resolver of the names: the arguments, or the
// jails the selector matches (a bulk action).
func (a *app) jailTargets(cmd *cobra.Command) func(ctx context.Context, args []string) ([]string, bool, error) {
	targets := a.jailTargetsOS(cmd)
	return func(ctx context.Context, args []string) ([]string, bool, error) {
		names, _, bulk, err := targets(ctx, args)
		return names, bulk, err
	}
}

// jailTargetsOS is jailTargets that also returns the OS of the selected jails, from the one list a selector reads
// (nil for named jails, which are not looked up).
func (a *app) jailTargetsOS(cmd *cobra.Command) func(ctx context.Context, args []string) ([]string, map[string]api.OS, bool, error) {
	var selectors []string
	var all bool
	cmd.Flags().StringArrayVarP(&selectors, "selector", "l", nil, "act on the jails carrying these labels, key=value (comma separated or repeated), instead of naming them (--label is the same)")
	cmd.Flags().BoolVar(&all, "all", false, "act on every jail of the project")
	return func(ctx context.Context, args []string) ([]string, map[string]api.OS, bool, error) {
		switch {
		case !all && len(selectors) == 0:
			if len(args) == 0 {
				return nil, nil, false, usagef("name the jails, or select them with -l key=value or --all")
			}
			return args, nil, false, nil
		case len(args) > 0:
			return nil, nil, false, usagef("name jails or select them with -l or --all, not both")
		case all && len(selectors) > 0:
			return nil, nil, false, usagef("use -l or --all, not both")
		}
		var labels []string
		for _, s := range selectors {
			labels = append(labels, strings.Split(s, ",")...)
		}
		opts, err := labelOpts(labels)
		if err != nil {
			return nil, nil, false, err
		}
		c, err := a.connect(ctx)
		if err != nil {
			return nil, nil, false, err
		}
		jails, err := collect(c.Jails().List(ctx, opts...))
		if err != nil {
			return nil, nil, false, err
		}
		var names []string
		oses := map[string]api.OS{}
		for _, j := range jails {
			names = append(names, j.Metadata.Name)
			oses[j.Metadata.Name] = j.Spec.OS
		}
		return names, oses, true, nil
	}
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
		fprintln(w, strings.TrimRight(line, " "))
	}
}

func (a *app) jailCmd() *cobra.Command {
	d := a.jailDesc()
	jc := group("jail", "Create, inspect and control jails",
		`A jail is a FreeBSD jail on a ZFS root of its own, copied from an image generation,
with its own VNET network stack. These commands manage the specification
(create, edit, delete) and drive the lifecycle (start, stop, restart,
snapshots, rollback and upgrade).

Use --host to act on a host behind a hub.`,
		`jailops jail list --state running
jailops jail create web --image freebsd-15-1 --network lan --memory 2G
jailops jail show web
jailops jail start web`, "jails")
	crudCommands(a, jc, d, jailCreateLong, jailCreateExample, a.jailFlagBuild)
	addNotes(a, jc, d, func(j *api.Jail) *string { return &j.Spec.Notes })
	jc.AddCommand(a.jailInfo(d))
	jc.AddCommand(a.jailLifecycleSet(d)...)
	jc.AddCommand(a.jailExec(d), a.pkgCmd(), a.cpCmd(), a.jailShell(d), a.labelCmd(), a.jailConsole(d), a.jailSnapshotCmd(d), a.jailRollback(d), a.jailUpgrade(d), a.jailBase(d), a.jailAudit(d), a.jailCopy(d), a.jailLogs(d), a.jailExport(d), a.jailImport())
	jc.AddCommand(a.jailShellSetup(d), a.jailApply(d))
	return jc
}

// fixedAddr returns an address of a network attachment, or "" when there is
// none ("none") or the server has not allocated one yet ("auto").
func fixedAddr(s string) string {
	if s == "none" || s == "auto" {
		return ""
	}
	return s
}

const jailCreateLong = `Create a jail from flags or from a file. A jail needs a project, a network and a
storage. Name them, or use a profile that carries them; without either, a project
that is granted exactly one network (or one storage) gives the jail that one, and
the command says so on standard error. Several grants are never guessed from.
The project is --project, JAILOPS_PROJECT or the one chosen with 'project switch'.
The server allocates "auto" addresses from the network and writes them back into
the specification.

Volumes are attached afterwards: 'jailops volume create' makes one and 'jailops volume attach JAIL
VOLUME PATH' puts it into a jail, running or not. Deleting the jail never deletes a volume.

A jail starts when the host boots only with --autostart (boot.autostart); a jail that
was running before a reboot does not come back on its own.

A new jail name has at most 29 characters. Sizes (--disk) are stored as bytes;
4G, 4GiB and 4294967296 are the same size.`

const jailCreateExample = `jailops @P@ create web --project web --image freebsd-15-1 --network lan --storage default --start --autostart
jailops @P@ create db --project web --image freebsd-15-1 --network lan=10.20.0.50 --storage default --memory 4G --autostart
jailops @P@ create mail --project web --image freebsd-15-1 --network lan --storage default
jailops volume attach mail maildir /var/mail/vhosts --project web
jailops @P@ create nginx --project web --image freebsd-15-1 --profile web
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
	d := a.jailDesc()
	addList(a, root, d, "")
	addCreate(a, root, d, jailCreateLong, jailCreateExample, a.jailFlagBuild)
	addDelete(a, root, d)
	addRename(a, root, d, d.renameLong)
	root.AddCommand(a.jailInfo(d))
	root.AddCommand(a.jailLifecycleSet(d)...)
	root.AddCommand(a.jailExec(d), a.pkgCmd(), a.cpCmd(), a.jailShell(d), a.labelCmd(), a.logsCmd(d), a.exposeCmd(), a.unexposeCmd())
}

func (a *app) jailFlagBuild(cmd *cobra.Command) func(string) (api.Jail, error) {
	var (
		image, hostname, memory, disk, storage, comment             string
		shell, prompt, cpuset                                       string
		cpu, pids, priority, stopTimeout, securelevel               int
		networks, envs, labels, depends, tags, profiles, fwPolicies []string
		autostart, zfsDelegation                                    bool
	)
	f := cmd.Flags()
	f.StringVar(&comment, "comment", "", "what the jail is for")
	f.StringVar(&image, "image", "", "image reference: NAME (the newest generation) or NAME#gN (default: the host's only image)")
	f.StringVar(&storage, "storage", "", "storage to place the jail on, one granted to the project (default: the project's only storage, unless a profile supplies it)")
	f.StringArrayVar(&profiles, "profile", nil, "profile to apply, later ones override earlier ones (repeatable)")
	f.StringVar(&hostname, "hostname", "", "hostname inside the jail (default: the jail name)")
	f.StringArrayVar(&networks, "network", nil, "attach a network granted to the project (repeatable; default: the project's only network, unless a profile supplies one): NET (an address of every family it has), NET=IPV4 or NET=IPV4,IPV6 (an address, auto or none)")
	f.IntVar(&cpu, "cpu", 0, "CPU cores: the jail is slowed down to that share (rctl pcpu; needs kern.racct.enable=1)")
	f.StringVar(&cpuset, "cpuset", "", "pin the jail to CPUs of the host, for example 2,3 or 0-3,6 (cpuset(1); needs no racct; a CPU the host does not have stops the jail from starting)")
	f.StringVar(&memory, "memory", "", "limit on the anonymous (swap-backed) memory the jail may reserve, for example 2G; an allocation beyond it fails and nothing is killed. jailops info JAIL shows it as \"reserved\", next to the resident memory (rctl swapuse; needs kern.racct.enable=1)")
	f.StringVar(&disk, "disk", "", "ZFS quota of the whole jail (root, data, volumes and snapshots), for example 20G")
	f.IntVar(&pids, "pids", 0, "processes the jail may have (rctl maxproc; needs kern.racct.enable=1)")
	f.StringArrayVar(&fwPolicies, "firewall-policy", nil, "firewall policy of the jail (repeatable; they are rendered in the order given, after the project's). On set the policies given REPLACE the whole list of the jail; to add one name all of them")
	f.StringArrayVar(&tags, "tag", nil, "firewall tag of this jail (repeatable)")
	f.BoolVar(&autostart, "autostart", false, "start the jail when the host boots")
	f.BoolVar(&zfsDelegation, "zfs-delegation", false, "global administrators only: hand the jail a ZFS dataset of its own, <container>/delegated, which root in the jail manages (the host's priv.conf must allow it: admin-zfs-delegation)")
	f.IntVar(&priority, "boot-priority", 0, "boot order, lower starts first")
	f.IntVar(&stopTimeout, "stop-timeout", 0, "seconds the jail has to shut down by itself before it is killed (default: thirty)")
	f.IntVar(&securelevel, "securelevel", api.DefaultSecureLevel, "the jail's kern.securelevel, -1 (the host's) to 3; a Linux jail without it has the host's")
	f.StringArrayVar(&depends, "depends", nil, "jail that must start first (repeatable)")
	f.StringArrayVar(&envs, "env", nil, "environment variable KEY=VALUE (repeatable)")
	f.StringArrayVar(&labels, "label", nil, "label KEY=VALUE (repeatable)")
	f.StringVar(&shell, "shell", "", "shell of jailops shell and the console: bash, sh or a path inside the jail (default: the project's shell, else the host's jails.shell, sh)")
	f.StringVar(&prompt, "prompt", "", "bash prompt of the jail: default, short, none or a PS1 text that may hold {jail}, {project} and {host} (default: the host's jails.prompt)")
	a.completeFlag(cmd, "image", imageRefs)
	a.completeFlag(cmd, "network", listNames(func(ctx context.Context, c *sdk.Client) iterNames { return namesOf(c.Networks().List(ctx), netName) }))
	a.completeFlag(cmd, "storage", listNames(func(ctx context.Context, c *sdk.Client) iterNames { return namesOf(c.Storage().List(ctx), storName) }))
	a.completeFlag(cmd, "volume", listNames(func(ctx context.Context, c *sdk.Client) iterNames {
		return namesOf(c.Volumes().List(ctx), func(v api.Volume) string { return v.Metadata.Name + ":" })
	}))
	a.completeFlag(cmd, "profile", listNames(func(ctx context.Context, c *sdk.Client) iterNames {
		return namesOf(c.Profiles().List(ctx), func(p api.Profile) string { return p.Metadata.Name })
	}))
	a.completeFlag(cmd, "firewall-policy", listNames(func(ctx context.Context, c *sdk.Client) iterNames {
		return namesOf(c.Firewall().Policies().List(ctx), func(p api.FirewallPolicy) string { return p.Metadata.Name })
	}))
	a.completeFlag(cmd, "depends", listNames(func(ctx context.Context, c *sdk.Client) iterNames {
		return namesOf(c.Jails().List(ctx), func(j api.Jail) string { return j.Metadata.Name })
	}))
	return func(name string) (api.Jail, error) {
		if cpuset != "" {
			c, err := api.NormalizeCPUSet(cpuset)
			if err != nil {
				return api.Jail{}, usagef("--cpuset: %v", err)
			}
			cpuset = c
		}
		spec := api.JailSpec{
			Comment: comment, Image: image, Hostname: hostname,
			Storage: storage, Profiles: dropEmpty(profiles),
			Resources: api.Resources{CPU: cpu, CPUSet: cpuset, Memory: memory, Disk: disk, PIDs: pids},
			Firewall:  api.FirewallAttach{Policies: dropEmpty(fwPolicies), Tags: dropEmpty(tags)},
			Boot:      api.BootPolicy{Autostart: autostart, Priority: priority, Depends: dropEmpty(depends), StopTimeoutSeconds: stopTimeout},
			Shell:     api.ShellPath(shell), Prompt: prompt,
			Delegate: zfsDelegation,
		}
		if cmd.Flags().Changed("securelevel") {
			if securelevel < api.MinSecureLevel || securelevel > api.MaxSecureLevel {
				return api.Jail{}, usagef("--securelevel: %d is not between %d and %d", securelevel, api.MinSecureLevel, api.MaxSecureLevel)
			}
			spec.SecureLevel = &securelevel
		}
		if prompt != "" {
			if err := api.CheckPrompt(prompt); err != nil {
				return api.Jail{}, usagef("--prompt: %v", err)
			}
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
	var grace string
	cmd := &cobra.Command{
		Use:   verb + " NAME... | -l KEY=VALUE | --all",
		Short: short,
		Long: strings.ToUpper(verb[:1]) + verb[1:] + ` one or more jails and wait for the operation to finish (--no-wait returns at once).
The jails are named, or selected by label with -l (the same key=value labels as
'list -l') or --all for every jail of the project.`,
		Example:           "jailops jail " + verb + " web\njailops jail " + verb + " web db --no-wait\njailops jail " + verb + " -l env=prod\njailops --host host-b jail " + verb + " web",
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: completeKind(a, d),
	}
	resolve := a.jailTargets(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
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
		names, bulk, err := resolve(cmd.Context(), args)
		if err != nil {
			return err
		}
		if bulk && len(names) == 0 {
			fprintln(a.env.Stderr, "No jails match.")
			return nil
		}
		for _, name := range names {
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
	}
	if verb != "start" {
		cmd.Long += "\n--force skips the graceful shutdown; --timeout sets how long the jail gets to shut down."
		cmd.Flags().BoolVar(&force, "force", false, "do not wait for a graceful shutdown")
		cmd.Flags().StringVar(&grace, "timeout", "", "time the jail gets to shut down: seconds, or a duration such as 30s")
	}
	return cmd
}

// parseGrace reads --timeout: whole seconds, or a duration such as 1m30s.
func parseGrace(s string) (int, error) {
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n, nil
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return seconds(d), nil
	}
	return 0, usagef("--timeout must be a number of seconds or a duration such as 30s, not %q", s)
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
when you close it, or at the idle or maximum time of the server if the
administrator set one (by default there is none).

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
	sc := group("snapshot", "Create, list, show and delete jail snapshots",
		`A snapshot is a consistent ZFS snapshot across all datasets of a jail. It is the
target of 'jailops jail rollback --scope data' and the source of backups. Snapshots
cost almost nothing until data changes; the written column shows how much has
changed since each one.`,
		`jailops jail snapshot create web before-upgrade -m "before 15.1"
jailops jail snapshot list web
jailops jail snapshot show web before-upgrade
jailops jail snapshot delete web before-upgrade`, "snap", "snapshots")
	jailThenSnapshot := completeArgs(completeKind(a, d), a.completeSnapshots())
	var comment string
	create := &cobra.Command{
		Use:               "create JAIL [SNAPSHOT]",
		Short:             "Take a snapshot of a jail",
		Long:              "Take a snapshot of all datasets of a jail. Without SNAPSHOT the server chooses a name.\nA snapshot name has at most 64 lower case letters, digits, '.', '_' and '-'; names starting with jo- are reserved.",
		Example:           "jailops jail snapshot create web\njailops jail snapshot create web before-upgrade -m \"before 15.1\"",
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: completeArgs(completeKind(a, d), completeNone),
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
	show := &cobra.Command{
		Use:               "show JAIL SNAPSHOT",
		Aliases:           []string{"get"},
		Short:             "Show one snapshot of a jail",
		Long:              "Show one snapshot of a jail: when it was taken, its comment and the bytes written to the data datasets since.\n-o json and -o yaml print the complete object.",
		Example:           "jailops jail snapshot show web before-upgrade\njailops jail snapshot show web before-upgrade -o yaml",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: jailThenSnapshot,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			sn, err := c.Jails().Snapshots(args[0]).Get(cmd.Context(), args[1])
			if err != nil {
				return err
			}
			if a.format == fmtTable {
				return a.showObject(sn.Metadata, []string{"Written:     " + humanBytes(sn.Status.Written)}, sn.Spec, sn.Status)
			}
			return a.emit(sn, nil, nil, func() []string { return []string{sn.Metadata.Name} })
		},
	}
	del := &cobra.Command{
		Use:               "delete JAIL SNAPSHOT...",
		Aliases:           []string{"rm"},
		Short:             "Delete snapshots of a jail",
		Long:              "Delete snapshots of a jail after confirmation (-y skips it). A rollback to a deleted snapshot is no longer possible.",
		Example:           "jailops jail snapshot delete web before-upgrade\njailops jail snapshot delete -y web old1 old2",
		Args:              cobra.MinimumNArgs(2),
		ValidArgsFunction: jailThenSnapshot,
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
	sc.AddCommand(create, list, show, del)
	return sc
}

// completeSnapshots completes a snapshot name of the jail named first.
func (a *app) completeSnapshots() completer {
	return a.completeWith(func(ctx context.Context, c *sdk.Client, args []string) ([]string, error) {
		if len(args) == 0 {
			return nil, nil
		}
		items, err := collect(c.Jails().Snapshots(args[0]).List(ctx))
		var n []string
		for _, s := range items {
			n = append(n, s.Metadata.Name)
		}
		return n, err
	})
}

// emitOne prints one finished result, as finishOp does for a synchronous reply.
func (a *app) emitOne(v any, noun, name, verb string) error {
	if a.format == fmtTable {
		a.say("%s/%s %s\n", noun, name, verb)
		return nil
	}
	return a.emit(v, nil, nil, func() []string { return []string{name} })
}

func (a *app) jailRollback(d *kindDesc[api.Jail]) *cobra.Command {
	var confirmLoss, destroyNewer bool
	var snapshot, scopeFlag string
	cmd := &cobra.Command{
		Use:   "rollback NAME",
		Short: "Roll a jail back: its root, its data, or both",
		Long: `Roll a jail back to an earlier state.

  --scope root  (default) returns the root of a stopped jail to the snapshot its last
                in-place upgrade (or 'jail base add') took, kept 14 days. The root holds
                the base, the ports packages, their database and the data of the
                services, so all of it goes back; everything written to the root since
                that snapshot is discarded for good. A volume attached to the jail is
                not rolled back.
  --scope data  rolls the dataset the jail manages itself (zfs_delegation) back to a
                snapshot. Everything written to it since is discarded for good.
  --scope all   rolls the root and that dataset back to a snapshot.

Every scope refuses a running jail: stop it first (JailOps does not stop it for you).
A rollback destroys the snapshots taken after its target (by a backup, a replication or
by hand), so it refuses while there are any and names them; --destroy-newer destroys
them with it, and asks for the same confirmation as the data loss.
Before a data or all rollback the command shows how many bytes were written since the
snapshot, which is what would be lost, and asks you to type the jail name. In a
script, pass --confirm-data-loss once you have decided; -y does not apply to
data loss. Without --snapshot the most recent snapshot is used.`,
		Example: `jailops jail rollback web
jailops jail rollback web --scope data --snapshot before-upgrade
jailops jail rollback web --scope all --confirm-data-loss
jailops jail rollback web --destroy-newer
jailops jail rollback web --scope data --dry-run`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKind(a, d),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			scope := api.RollbackScope(scopeFlag)
			switch scope {
			case api.RollbackRoot, api.RollbackData, api.RollbackAll:
			default:
				return usagef("--scope must be root, data or all, not %q", scopeFlag)
			}
			ctx := cmd.Context()
			c, err := a.connect(ctx)
			if err != nil {
				return err
			}
			rb := api.JailRollback{Scope: scope, Snapshot: snapshot, DestroyNewer: destroyNewer}
			if scope != api.RollbackRoot {
				sn, err := pickSnapshot(ctx, c, name, snapshot)
				if err != nil {
					return err
				}
				items, err := collect(c.Jails().Snapshots(name).List(ctx))
				if err != nil {
					return err
				}
				newer, err := newerThan(items, sn, destroyNewer)
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
				warn := fmt.Sprintf("This destroys %s of data written to jail %q since snapshot %q%s. It cannot be undone.",
					humanBytes(sn.Status.Written), name, sn.Metadata.Name, newerNote(newer))
				if err := a.confirmTyped(warn, name, confirmLoss, "--confirm-data-loss"); err != nil {
					return err
				}
				rb.ConfirmDataLoss = true
			} else if j, err := c.Jails().Get(ctx, name); err == nil && j.Spec.OS != api.OSLinux {
				// the root of a jail holds its data: the rollback discards what was written since the upgrade
				items, err := collect(c.Jails().Snapshots(name).List(ctx))
				if err != nil {
					return err
				}
				var sn *api.JailSnapshot
				for i := range items {
					if strings.HasPrefix(items[i].Metadata.Name, "jo-pre-upgrade-") {
						sn = &items[i]
					}
				}
				if sn == nil {
					return api.Errorf(api.CodeNotFound, "jail %q has no snapshot from an in-place upgrade to go back to", name).
						WithHint("the snapshot of an upgrade is kept for 14 days and a successful rollback uses it up")
				}
				newer, err := newerThan(items, *sn, destroyNewer)
				if err != nil {
					return err
				}
				fprintf(a.env.Stdout, "Rolling back the root of jail %s to snapshot %s (taken %s ago).\n",
					name, sn.Metadata.Name, age(a.env.Now(), sn.Metadata.CreatedAt))
				rb.ConfirmDataLoss = true
				if ok, err := a.dry("roll back the root of jail "+name+" to the snapshot of its last upgrade", rb); ok || err != nil {
					return err
				}
				warn := fmt.Sprintf("This discards everything written to the root of jail %q since snapshot %q%s. It cannot be undone.",
					name, sn.Metadata.Name, newerNote(newer))
				if err := a.confirmTyped(warn, name, confirmLoss, "--confirm-data-loss"); err != nil {
					return err
				}
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
	f.StringVar(&scopeFlag, "scope", "root", "what to roll back: root (to the snapshot of the last in-place upgrade), data (the delegated dataset, to a snapshot) or all (root and delegated dataset, to a snapshot)")
	enumFlag(cmd, "scope", string(api.RollbackRoot), string(api.RollbackData), string(api.RollbackAll))
	f.StringVar(&snapshot, "snapshot", "", "snapshot to roll back to (default: the latest)")
	_ = cmd.RegisterFlagCompletionFunc("snapshot", a.completeSnapshots())
	f.BoolVar(&confirmLoss, "confirm-data-loss", false, "confirm that data written since the snapshot may be destroyed")
	f.BoolVar(&destroyNewer, "destroy-newer", false, "also destroy the snapshots taken after the target, which a rollback otherwise refuses over (asks for the same confirmation)")
	return cmd
}

// newerThan lists the snapshots of a jail taken after sn. Without --destroy-newer any such snapshot is a refusal that
// names them, before the question: a rollback would destroy them, and they are someone's (a backup, a replication, a
// snapshot by hand).
func newerThan(items []api.JailSnapshot, sn api.JailSnapshot, destroy bool) ([]string, error) {
	var newer []string
	for _, it := range items {
		if it.Metadata.Name != sn.Metadata.Name && it.Metadata.CreatedAt.After(sn.Metadata.CreatedAt) {
			newer = append(newer, it.Metadata.Name)
		}
	}
	if len(newer) > 0 && !destroy {
		return nil, api.Errorf(api.CodePreconditionFailed, "snapshots taken after %q exist: %s; a rollback to %q destroys them", sn.Metadata.Name, strings.Join(newer, ", "), sn.Metadata.Name).
			WithHint("keep what you need from them, then add --destroy-newer")
	}
	return newer, nil
}

// newerNote is the part of the rollback's question that names the snapshots it destroys.
func newerNote(newer []string) string {
	if len(newer) == 0 {
		return ""
	}
	return ", and destroys the snapshots taken after it (" + strings.Join(newer, ", ") + ")"
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
	var release string
	var noPackages, stopOnError, force bool
	cmd := &cobra.Command{
		Use:   "upgrade NAME... | -l KEY=VALUE | --all",
		Short: "Upgrade jails in place, while they run",
		Long: `Upgrade jails. A FreeBSD jail is an independent system: it is upgraded in place while it runs, never stopped,
started or restarted. The base is upgraded from the host with pkg -r on the jail's root, as the FreeBSD Handbook
upgrades a pkgbase jail, and the ports packages inside the jail. First the command finds out, without touching the jail,
whether a base package of the release installed in the jail has a newer version in its repository; if nothing is newer
it says "up to date" and does nothing at all. Otherwise the packages are fetched, the root is snapshotted
(jo-pre-upgrade-<time>, kept 14 days, for a manual rollback), pkg upgrades the base on the running root, what the
scripts of the base packages do runs inside the jail, the ports packages are upgraded and one health check runs. The
services keep running their old files until they are restarted: the result lists them ("restart pending"). A failure
is reported and left as it is (the jail runs on; running the upgrade again completes it); nothing is rolled back on its
own. 'jailops jail rollback NAME' returns the root to the snapshot, with the jail stopped by its administrator.
--no-packages leaves the ports packages alone. A stopped jail is not upgraded. These commands are for FreeBSD jails;
a Linux jail is not part of a selection and manages its own packages.

Before pkg works on a jail's root, JailOps checks it for what only an attack on host-side pkg plants there (a link out of
the root, a link among pkg's own files, a pkg configuration with plugins). A jail with a finding is not upgraded: it is
reported as suspected_compromised (the update history, jailops doctor, an event) and the other jails go on.

--release RELEASE moves a jail to a new release (15.2-RELEASE reads base_release_2; 15.2-STABLE, 16.0-CURRENT,
15.2-PRERELEASE, 15.2-BETA1 and 15.2-RC1 read base_latest). It is not older than the release installed in the jail and
not newer than the host's kernel. Without it the release never changes.

Several jails are named, selected by label with -l, or all of the project with --all; each is upgraded on its own, one
after the other, a failure is reported for that jail and the run goes on (--stop-on-error stops it). A summary table
follows (upgraded, up to date, failed, skipped) and the exit code is non-zero if any jail failed. With --dry-run
every jail is checked and nothing changes.

The order of the steps for the host and its jails, the checks to run after an upgrade and the way back are in docs/lifecycle.md.`,
		Example: `jailops jail upgrade web --dry-run
jailops jail upgrade web
jailops jail upgrade web db ns1
jailops jail upgrade --all --project demo
jailops jail upgrade web --release 15.2-RELEASE
jailops jail upgrade web --no-packages`,
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: completeKind(a, d),
	}
	resolve := a.jailTargetsOS(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		names, oses, bulk, err := resolve(ctx, args)
		if err != nil {
			return err
		}
		if bulk || len(names) > 1 {
			// a Linux jail is not part of a selection: it manages its own packages (one named jail is the server's to refuse)
			if names, err = a.freebsdJails(ctx, names, oses, true); err != nil {
				return err
			}
			bulk = true
		}
		if bulk && len(names) == 0 {
			fprintln(a.env.Stderr, "No jails match.")
			return nil
		}
		c, err := a.connect(ctx)
		if err != nil {
			return err
		}
		many := len(names) > 1 || bulk
		var results []upgradeResult
		failed := 0
		for _, name := range names {
			r, err := a.upgradeInPlace(ctx, c, name, release, noPackages, force, many)
			if !many {
				return err
			}
			if err != nil {
				r = upgradeFailure(name, err)
			}
			if r.Result == "failed" {
				failed++
			}
			results = append(results, r)
			if err != nil && stopOnError {
				break
			}
		}
		// the jails --stop-on-error left out are in the summary too, as not tried
		for _, name := range names[len(results):] {
			results = append(results, upgradeResult{Jail: name, Result: "not tried", Reason: "--stop-on-error stopped the run"})
		}
		if err := a.emitUpgradeSummary(results); err != nil {
			return err
		}
		if failed > 0 {
			tried := 0
			for _, r := range results {
				if r.Result != "not tried" {
					tried++
				}
			}
			fprintf(a.env.Stderr, "%d of the %d jails tried failed to upgrade; the others were not touched by that\n", failed, tried)
			return &exitError{code: ExitFailure}
		}
		return nil
	}
	cmd.Flags().StringVar(&release, "release", "", "move the jail to this FreeBSD release, for example 15.2-RELEASE or 16.0-CURRENT (default: stay on the installed release)")
	cmd.Flags().BoolVar(&noPackages, "no-packages", false, "upgrade the base only, leave the ports packages alone")
	cmd.Flags().BoolVar(&force, "force", false, "upgrade although the jail's health check fails before the upgrade (nothing else is overridden: releases, space and every other check still apply)")
	cmd.Flags().BoolVar(&stopOnError, "stop-on-error", false, "with several jails: stop at the first jail that fails (default: report it and go on)")
	return cmd
}

// upgradeResult is what became of one jail of an upgrade run.
type upgradeResult struct {
	Jail   string `json:"jail"`
	Result string `json:"result"` // upgraded, up to date, failed, skipped, started (--no-wait), not tried
	Reason string `json:"reason,omitempty"`
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
}

// upgradeFailure classifies the error of one jail: a request that does not apply to the jail (skipped), or a failure.
func upgradeFailure(name string, err error) upgradeResult {
	r := upgradeResult{Jail: name, Result: "failed", Reason: err.Error()}
	var ae *api.Error
	switch {
	case errors.As(err, &ae) && ae.Code == api.CodeUnsupported:
		r.Result, r.Reason = "skipped", ae.Message
	}
	if i := strings.Index(r.Reason, "\n"); i > 0 {
		r.Reason = r.Reason[:i]
	}
	return r
}

func (a *app) emitUpgradeSummary(results []upgradeResult) error {
	return a.emit(results, func(col int, v string) string {
		switch v {
		case "upgraded":
			return "32"
		case "rolled back", "failed":
			return "31"
		}
		return ""
	}, func() ([]string, [][]string) {
		rows := make([][]string, len(results))
		for i, r := range results {
			change := ""
			switch {
			case r.Result == "up to date" && r.From != "":
				change = "FreeBSD " + r.From
			case strings.HasPrefix(r.To, "newer "):
				change = strings.TrimPrefix(r.To, "newer ") + " (" + r.From + ")"
			case r.From != "" || r.To != "":
				change = r.From + " -> " + r.To
			}
			rows[i] = []string{r.Jail, r.Result, change, r.Reason}
		}
		return []string{"JAIL", "RESULT", "CHANGE", "REASON"}, rows
	}, func() []string {
		n := make([]string, len(results))
		for i, r := range results {
			n[i] = r.Jail
		}
		return n
	})
}

// upgradeInPlace upgrades a jail in place and says what the server did.
func (a *app) upgradeInPlace(ctx context.Context, c *sdk.Client, name, release string, noPackages, force, inTable bool) (upgradeResult, error) {
	res := upgradeResult{Jail: name}
	up := api.JailUpgrade{DryRun: a.dryRun, Release: release, NoPackages: noPackages, Force: force}
	h, err := handle(c.Jails().Upgrade(ctx, name, up))
	if err != nil {
		return res, err
	}
	if !a.waiting() && !a.dryRun {
		// --no-wait: the operation has only started
		res.Result = "started"
		return res, a.finishOp(ctx, h, "jail", name, "upgrade started", nil)
	}
	_, op, err := a.await(ctx, h)
	if err != nil {
		return res, err
	}
	m := decodeMove(op)
	u := m.Upgrade
	switch {
	case u != nil && u.Nothing:
		res.Result, res.From = "up to date", u.Installed
		if a.dryRun && (a.format == fmtJSON || a.format == fmtYAML) {
			return res, a.preview("upgrade jail "+name, up)
		}
		switch {
		case inTable: // the summary table says it
		case u.Installed != "":
			a.say("jail/%s: up to date (FreeBSD %s)\n", name, u.Installed)
		default:
			a.say("jail/%s: up to date\n", name)
		}
		if !inTable {
			for _, c := range u.Checks {
				if strings.Contains(c, " are current") {
					a.say("  %s\n", c)
				}
			}
		}
		return res, nil
	case u == nil:
		res.Result = "up to date"
		if !inTable {
			a.say("jail/%s: up to date\n", name)
		}
		return res, nil
	}
	res.Result, res.From, res.To = "upgraded", u.From, u.To
	if a.dryRun {
		if a.format == fmtJSON || a.format == fmtYAML {
			return res, a.preview("upgrade jail "+name, up)
		}
		fprintf(a.env.Stdout, "dry-run: would upgrade jail %s (%s)\n", name, u.Installed)
		for _, c := range u.Checks {
			fprintf(a.env.Stdout, "  check ok: %s\n", c)
		}
		if len(u.BaseChanges) > 0 {
			fprintf(a.env.Stdout, "  %d base packages change (pkg's dry run):\n", len(u.BaseChanges))
			for i, c := range u.BaseChanges {
				if i == 40 {
					fprintf(a.env.Stdout, "    ... %d more\n", len(u.BaseChanges)-i)
					break
				}
				fprintf(a.env.Stdout, "    %s\n", c)
			}
		}
		for _, f := range u.Failed {
			fprintf(a.env.Stdout, "  check FAILED: %s\n", f)
		}
		if u.Download > 0 {
			fprintf(a.env.Stdout, "  pkg downloads %s\n", humanBytes(u.Download))
		}
		switch {
		case u.DiskDelta > 0:
			fprintf(a.env.Stdout, "  the root grows by %s\n", humanBytes(u.DiskDelta))
		case u.DiskDelta < 0:
			fprintf(a.env.Stdout, "  the root shrinks by %s\n", humanBytes(-u.DiskDelta))
		}
		if u.HealthCheck != "" {
			fprintf(a.env.Stdout, "  health check after the upgrade: %s\n", u.HealthCheck)
		}
		if len(u.PortsPending) > 0 {
			fprintf(a.env.Stdout, "  %d ports packages change: %s\n", len(u.PortsPending), listShort(u.PortsPending, 8))
		}
		// a jail is upgraded while it runs: it is never stopped, started or restarted
		fprintf(a.env.Stdout, "  the jail is not stopped: it runs throughout; its services use the new files once they are restarted\n")
		if u.ToRelease != "" && u.ToRelease != u.FromRelease {
			fprintf(a.env.Stdout, "  release: %s to %s\n", u.FromRelease, u.ToRelease)
		}
		if u.Snapshot != "" {
			fprintf(a.env.Stdout, "  rollback point: snapshot %s kept for a manual rollback ('jailops jail rollback %s', with the jail stopped; what was written to the root since is lost)\n", u.Snapshot, name)
		}
		for i, st := range m.Plan {
			fprintf(a.env.Stdout, "  %2d. %s\n", i+1, st)
		}
		fprintf(a.env.Stdout, "  conflicts between the packages are found by the real run; nothing rolls back on its own\n")
		for _, w := range m.Warnings {
			fprintf(a.env.Stdout, "warning: %s\n", w)
		}
		if len(u.Failed) > 0 {
			res.Result = "failed"
			return res, &exitError{code: ExitFailure}
		}
		return res, nil
	}
	what := u.To
	a.say("jail/%s upgraded (%s: %s)\n", name, u.Installed, what)
	if len(u.BasePending) > 0 {
		a.say("  base: %d packages upgraded\n", len(u.BasePending))
	}
	if len(u.PortsPending) > 0 {
		a.say("  ports: %d packages upgraded (%s)\n", len(u.PortsPending), listShort(u.PortsPending, 6))
	}
	if len(u.Pkgsave) > 0 {
		a.say("  %d .pkgsave files were left: %s\n", len(u.Pkgsave), listShort(u.Pkgsave, 6))
	}
	if len(u.RestartPending) > 0 {
		a.say("  restart pending (they still run replaced files; JailOps restarts nothing): %s\n", listShort(u.RestartPending, 8))
	}
	for _, w := range m.Warnings {
		a.say("warning: %s\n", w)
	}
	return res, nil
}

// listShort joins the first n of names and counts the rest.
func listShort(names []string, n int) string {
	if len(names) <= n {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:n], ", ") + fmt.Sprintf(", ... (%d more)", len(names)-n)
}

// moveInfo is what the server says about an upgrade, in the metadata of the operation.
type moveInfo struct {
	Plan     []string `json:"plan"`
	Warnings []string `json:"warnings"`
	Upgrade  *struct {
		From         string   `json:"from"`
		To           string   `json:"to"`
		FromRelease  string   `json:"from_release"`
		ToRelease    string   `json:"to_release"`
		Snapshot     string   `json:"snapshot"`
		Kept         []string `json:"kept"`
		Nothing      bool     `json:"nothing_to_upgrade"`
		Installed    string   `json:"installed"`
		BasePending  []string `json:"base_packages"`
		PortsPending []string `json:"ports_packages"`
		BaseChanges  []string `json:"base_changes"`
		Checks       []string `json:"checks"`
		HealthCheck  string   `json:"health_check"`
		Failed       []string `json:"failed_checks"`
		Download     int64    `json:"download_bytes"`
		DiskDelta    int64    `json:"disk_delta_bytes"`
		Pkgsave      []string `json:"pkgsave"`
		// the processes that still run replaced files, after the upgrade
		RestartPending []string `json:"restart_pending"`
	} `json:"upgrade"`
}

func decodeMove(op api.Operation) moveInfo {
	var m moveInfo
	if len(op.Metadata) > 0 {
		_ = json.Unmarshal(op.Metadata, &m)
	}
	return m
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
	var keepIP, data, start bool
	var snapshot string
	cmd := &cobra.Command{
		Use:   "copy SOURCE NAME",
		Short: "Create a jail from another: its specification, with --data its data too",
		Long: `Create a new jail from an existing one, in the same project and on the same storage.

Without --data only the specification and labels are copied: the new jail starts from its image, empty. Fixed
addresses are replaced by "auto" so the copy does not collide with the source (--keep-ip keeps them, which only works
when the source is on another host).

With --data the new jail has the data of the source as well: its root and its persistent directories (/etc,
/usr/local, /var, /home). The source may be running; its datasets are snapshotted in one atomic step, so the copy
is what a power loss at that moment would have left (stop the source first for a copy a database does not have to
recover). The copy is independent of the source (a full send and receive, never a clone): the source can be deleted,
rolled back or snapshotted afterwards. --snapshot copies an earlier snapshot of the source instead of its present
state ('jailops jail snapshot list SOURCE'); the snapshot stays on the source and the copy does not carry it.

A copy has addresses and a MAC address of its own, no volume attached (attach what it needs with 'jailops volume
attach': two jails writing one volume by accident is not what a copy is for) and no exposure of the source's ports
('jailops expose'). Its hostname and the configuration inside it are the source's: change them before both run.
--start starts the copy when it is made. NAME has at most 29 characters.`,
		Example: `jailops jail copy web web2
jailops jail copy web web2 --data --start
jailops jail copy db db-test --data --snapshot before-upgrade
jailops --host host-b jail copy web web-b`,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeArgs(completeKind(a, d), completeNone),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if !data && snapshot != "" {
				return usagef("--snapshot copies the data of a snapshot: add --data")
			}
			if data && keepIP {
				return usagef("--keep-ip applies to a copy without --data: the copy of the data has addresses of its own")
			}
			if start && !a.waiting() {
				return usagef("--start waits for the copy: drop --no-wait")
			}
			c, err := a.connect(ctx)
			if err != nil {
				return err
			}
			src, err := c.Jails().Get(ctx, args[0])
			if err != nil {
				return err
			}
			if data {
				req := api.JailCopy{Name: args[1], Snapshot: snapshot}
				what := "copy jail " + args[0] + " to " + args[1] + " with its data"
				if snapshot != "" {
					what += " from snapshot " + snapshot
				}
				if ok, err := a.dry(what, req); ok || err != nil {
					return err
				}
				h, err := handle(c.Jails().Copy(ctx, args[0], req))
				if err != nil {
					return err
				}
				if !start {
					return a.finishOp(ctx, h, "jail", args[1], "created", nil)
				}
				if _, _, err := a.await(ctx, h); err != nil {
					return err
				}
				sh, err := handle(c.Jails().Start(ctx, args[1]))
				if err != nil {
					return err
				}
				return a.finishOp(ctx, sh, "jail", args[1], "created and started", nil)
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
			if !start {
				return a.finishOp(ctx, h, "jail", args[1], "created", nil)
			}
			if _, _, err := a.await(ctx, h); err != nil {
				return err
			}
			sh, err := handle(c.Jails().Start(ctx, args[1]))
			if err != nil {
				return err
			}
			return a.finishOp(ctx, sh, "jail", args[1], "created and started", nil)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&keepIP, "keep-ip", false, "keep fixed addresses instead of allocating new ones (a copy without --data)")
	f.BoolVar(&data, "data", false, "copy the data of the source as well: its root and persistent directories, independent of the source")
	f.StringVar(&snapshot, "snapshot", "", "with --data: copy this snapshot of the source instead of its present state")
	f.BoolVar(&start, "start", false, "start the new jail when it is made")
	_ = cmd.RegisterFlagCompletionFunc("snapshot", a.completeSnapshots())
	return cmd
}

func (a *app) jailShellSetup(d *kindDesc[api.Jail]) *cobra.Command {
	return &cobra.Command{
		Use:   "shell-setup NAME",
		Short: "Give a jail its shell and the JailOps prompt, as new jails get them",
		Long: `Give a running FreeBSD jail the shell and prompt a new jail gets: the shell and
prompt keys the jail leaves empty are set from the host's defaults (jails.shell
and jails.prompt of jailopsd.yaml: sh and the default prompt) and the prompt is
written. It installs nothing: when the shell is bash and the jail has none, the
answer says so and names 'jailops pkg install NAME -- bash'. Running it again
repairs the files and changes nothing else.

The files of /usr/local/etc/profile.d that end in .sh are sourced from a marked block at
the top of /usr/local/etc/profile (login bash) and /usr/local/etc/bash.bashrc (other
interactive bash), and by FreeBSD's /etc/profile for a login sh, so they survive OS
updates: jailops-env.sh exports JAILOPS_JAIL, JAILOPS_PROJECT and JAILOPS_HOST,
jailops-prompt.sh sets the prompt, and a file of your own (or of a recipe) is read too. A user's own ~/.bashrc, ~/.bash_profile or ~/.profile wins over them.
The default prompt shows the jail, and in grey its project and the host it runs
on: root@web [shop/babel]:/# . Change it with 'jailops config set NAME prompt ...'
(default, short, none or a PS1 text); a text may hold {jail}, {project} and {host},
which read the variables. 'short' has no grey tag; 'none' removes the prompt and keeps
the variables. Existing jails are never changed on their own.`,
		Example: `jailops jail shell-setup web
jailops config set web prompt none
jailops config set web prompt short
jailops config set web prompt '\[\e[38;5;196m\]\u@{jail}\[\e[0m\] [{project}/{host}]:\w\$ '`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKind(a, d),
		RunE: func(cmd *cobra.Command, args []string) error {
			if ok, err := a.dry("set up the shell of jail "+args[0], api.ShellSetupRequest{}); ok || err != nil {
				return err
			}
			c, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			h, err := handle(c.Jails().ShellSetup(cmd.Context(), args[0]))
			if err != nil {
				return err
			}
			if !a.waiting() {
				return a.finishOp(cmd.Context(), h, "jail", args[0], "shell setup", nil)
			}
			v, _, err := a.await(cmd.Context(), h)
			if err != nil {
				return err
			}
			res, _ := v.(api.ShellSetupResult)
			if a.format != fmtTable {
				return a.emit(res, nil, nil, func() []string { return []string{args[0]} })
			}
			fprintf(a.env.Stdout, "jail/%s: shell %s, prompt %s\n", args[0], res.Shell, orNone(res.Prompt))
			if res.PS1 != "" {
				fprintf(a.env.Stdout, "  PS1='%s'\n", res.PS1)
			}
			for _, s := range res.Steps {
				fprintf(a.env.Stdout, "  %s\n", s)
			}
			return nil
		},
	}
}

func orNone(s string) string {
	if s == "" {
		return "(not set)"
	}
	return s
}

// usedCell is the USED column of a jail list: what the jail's own datasets take on the pool, "-" when it is not known.
func usedCell(j api.Jail) string {
	if j.Status.DiskUsed <= 0 {
		return "-"
	}
	return humanBytes(j.Status.DiskUsed)
}

// versionCell is the VERSION column of 'jailops list -v': the FreeBSD release installed in the jail, the image of a Linux
// jail (the list does not carry its distribution), "-" when neither is known.
func versionCell(j api.Jail) string {
	if j.Spec.OS == api.OSLinux {
		return dash(j.Spec.Image)
	}
	return dash(j.Status.FreeBSDVersion)
}

// uptime is how long a jail has run, in its two largest units: "45s", "5m", "4h12m", "3d4h".
func uptime(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", max(0, int(d.Seconds())))
	}
	m := int(d.Minutes())
	days, hours, mins := m/(24*60), m/60%24, m%60
	two := func(a int, ua string, b int, ub string) string {
		if b == 0 {
			return fmt.Sprintf("%d%s", a, ua)
		}
		return fmt.Sprintf("%d%s%d%s", a, ua, b, ub)
	}
	switch {
	case days > 0:
		return two(days, "d", hours, "h")
	case hours > 0:
		return two(hours, "h", mins, "m")
	}
	return fmt.Sprintf("%dm", mins)
}

// autostartCell is the AUTOSTART column of a jail list: whether the jail starts when the host boots.
func autostartCell(j api.Jail) string {
	if j.Spec.Boot.Autostart {
		return "yes"
	}
	return "-"
}
