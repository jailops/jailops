package cli

import (
	"context"
	"encoding/json"
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
mountpoints, its records, its update history, the marks of its provisioning and shell setup, and the boot
order of the jails that depend on it. The jail must be stopped: the kernel knows a running jail by its
name, and its mounts, interfaces and firewall anchor are set up under it. Its hostname does not change`)
	d.create = func(ctx context.Context, c *sdk.Client, v api.Jail) (opHandle, api.Jail, error) {
		h, err := handle(c.Jails().Create(ctx, v))
		return h, api.Jail{}, err
	}
	d.del = func(ctx context.Context, c *sdk.Client, n string) (opHandle, error) {
		return handle(c.Jails().Delete(ctx, n))
	}
	d.head = []string{"NAME", "STATE", "IMAGE", "GEN", "ADDRESSES", "EXPOSED", "AUTOSTART", "USED", "AGE"}
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
			joinOrDash(addrs), exposedCell(j), autostartCell(j), usedCell(j), age(now, j.Metadata.CreatedAt)}
	}
	// On a terminal: one address per line with its network, the generation
	// next to the image, and no column a long IPv6 address would crowd out.
	d.brief = &listView[api.Jail]{
		head:  []string{"NAME", "STATE", "IPV4", "IPV6", "IMAGE", "EXPOSED", "AUTOSTART", "USED", "AGE"},
		style: stateStyle(1),
		drop:  []int{8, 7, 4, 3, 2},
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
			return []string{j.Metadata.Name, dash(string(j.Status.Power)), linesOrDash(v4), linesOrDash(v6),
				imageWithGen(j.Spec.Image, j.Status.Generation, j.Status.FreeBSDVersion), exposedCell(j), autostartCell(j), usedCell(j), age(now, j.Metadata.CreatedAt)}
		},
	}
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
		switch {
		case !j.Status.ProvisionedAt.IsZero():
			extra = append(extra, fmt.Sprintf("Provisioned: %s ago", age(a.env.Now(), j.Status.ProvisionedAt)))
		case j.Spec.Provision != nil:
			extra = append(extra, fmt.Sprintf("Provisioned: no (run 'jailops provision %s')", j.Metadata.Name))
		}
		return a.showObject(j.Metadata, extra, j.Spec, j.Status)
	}
	d.afterCreate = a.jailStartFlag
	d.beforeDelete = a.jailForceFlag
	d.targets = a.jailTargets
	d.loss = jailLoss
	d.emptyHint = "jailops jail create NAME --image IMAGE --network NETWORK --storage STORAGE"
	d.prepare = a.jailDefaults
	return &d
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
		return "", api.Errorf(api.CodeConflict, "jail %q is running", name).
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
// project's only grant of that kind, as app install does, and says which it took.
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
		if err := a.finishOp(ctx, h, "jail", name, "started", nil); err != nil {
			return err
		}
		a.noteShellInstalling(ctx, c, name)
		return nil
	}
}

// shellInstalling says whether the jail's bash is being installed in the background (its ShellSetup condition).
func shellInstalling(j api.Jail) bool {
	for _, c := range j.Status.Conditions {
		if c.Type == "ShellSetup" && c.Reason == api.ShellInstalling {
			return true
		}
	}
	return false
}

// noteShellInstalling tells, after a start, that the shell of a new jail is on its way: the start does not wait for
// the package server, and `jailops shell` waits for bash instead. It says nothing in machine-readable output.
func (a *app) noteShellInstalling(ctx context.Context, c *sdk.Client, name string) {
	if a.format != fmtTable {
		return
	}
	if j, err := c.Jails().Get(ctx, name); err == nil && shellInstalling(j) {
		fprintln(a.env.Stderr, "bash is being installed in the background; jailops shell waits for it")
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
	var selectors []string
	var all bool
	cmd.Flags().StringArrayVarP(&selectors, "selector", "l", nil, "act on the jails carrying these labels, key=value (comma separated or repeated), instead of naming them (--label is the same)")
	cmd.Flags().BoolVar(&all, "all", false, "act on every jail of the project")
	return func(ctx context.Context, args []string) ([]string, bool, error) {
		switch {
		case !all && len(selectors) == 0:
			if len(args) == 0 {
				return nil, false, usagef("name the jails, or select them with -l key=value or --all")
			}
			return args, false, nil
		case len(args) > 0:
			return nil, false, usagef("name jails or select them with -l or --all, not both")
		case all && len(selectors) > 0:
			return nil, false, usagef("use -l or --all, not both")
		}
		var labels []string
		for _, s := range selectors {
			labels = append(labels, strings.Split(s, ",")...)
		}
		opts, err := labelOpts(labels)
		if err != nil {
			return nil, false, err
		}
		c, err := a.connect(ctx)
		if err != nil {
			return nil, false, err
		}
		jails, err := collect(c.Jails().List(ctx, opts...))
		if err != nil {
			return nil, false, err
		}
		var names []string
		for _, j := range jails {
			names = append(names, j.Metadata.Name)
		}
		return names, true, nil
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
		`A jail is a FreeBSD jail on a ZFS-backed root cloned from an image generation,
with its own VNET network stack. These commands manage the specification
(create, edit, delete) and drive the lifecycle (start, stop, restart,
snapshots, rollback and upgrade).

Use --host to act on a host behind a hub.`,
		`jailops jail list --state running
jailops jail create web --image freebsd-15-1 --network lan --memory 2G
jailops jail show web
jailops jail start web`, "jails")
	crudCommands(a, jc, d, jailCreateLong, jailCreateExample, a.jailFlagBuild)
	jc.AddCommand(a.jailInfo(d))
	jc.AddCommand(a.jailLifecycleSet(d)...)
	jc.AddCommand(a.jailExec(d), a.pkgCmd(), a.cpCmd(), a.jailShell(d), a.provisionCmd(), a.labelCmd(), a.jailConsole(d), a.jailSnapshotCmd(d), a.jailRollback(d), a.jailUpgrade(d), a.jailConvert(d), a.jailCopy(d), a.jailLogs(d), a.jailDiff(d), a.jailExport(d), a.jailImport())
	jc.AddCommand(a.jailShellSetup(d))
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
	root.AddCommand(a.jailExec(d), a.pkgCmd(), a.cpCmd(), a.jailShell(d), a.provisionCmd(), a.labelCmd(), a.logsCmd(d), a.exposeCmd(), a.unexposeCmd())
}

func (a *app) jailFlagBuild(cmd *cobra.Command) func(string) (api.Jail, error) {
	var (
		image, layout, hostname, memory, disk, strategy, window, storage, comment string
		shell, prompt, cpuset                                                     string
		cpu, pids, priority, stopTimeout                                          int
		networks, envs, labels, depends, tags, profiles, fwPolicies               []string
		autostart, zfsDelegation                                                  bool
	)
	f := cmd.Flags()
	f.StringVar(&comment, "comment", "", "what the jail is for")
	f.StringVar(&image, "image", "", "image reference: NAME (the newest generation) or NAME#gN (default: the host's only image)")
	f.StringVar(&storage, "storage", "", "storage to place the jail on, one granted to the project (default: the project's only storage, unless a profile supplies it)")
	f.StringArrayVar(&profiles, "profile", nil, "profile to apply, later ones override earlier ones (repeatable)")
	f.StringVar(&layout, "layout", "", "root layout: thin or thick (default: thin for a FreeBSD image, thick for a Linux image; a Linux jail is always thick)")
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
	f.StringArrayVar(&depends, "depends", nil, "jail that must start first (repeatable)")
	f.StringArrayVar(&envs, "env", nil, "environment variable KEY=VALUE (repeatable)")
	f.StringArrayVar(&labels, "label", nil, "label KEY=VALUE (repeatable)")
	f.StringVar(&strategy, "update-strategy", "", "how the jail follows its image: rebase (a thin jail moves to a newer generation; the default), inplace (thick and Linux jails only) or manual")
	f.StringVar(&window, "update-window", "", "update window, for example \"sun 03:00\"")
	f.StringVar(&shell, "shell", "", "shell of jailops shell and the console: bash, sh or a path inside the jail (default: the host's jails.shell, bash)")
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
	enumFlag(cmd, "layout", string(api.LayoutThin), string(api.LayoutThick))
	enumFlag(cmd, "update-strategy", string(api.UpdateRebase), string(api.UpdateInPlace), string(api.UpdateManual))
	return func(name string) (api.Jail, error) {
		if cpuset != "" {
			c, err := api.NormalizeCPUSet(cpuset)
			if err != nil {
				return api.Jail{}, usagef("--cpuset: %v", err)
			}
			cpuset = c
		}
		spec := api.JailSpec{
			Comment: comment, Image: image, Layout: api.Layout(layout), Hostname: hostname,
			Storage: storage, Profiles: dropEmpty(profiles),
			Resources: api.Resources{CPU: cpu, CPUSet: cpuset, Memory: memory, Disk: disk, PIDs: pids},
			Firewall:  api.FirewallAttach{Policies: dropEmpty(fwPolicies), Tags: dropEmpty(tags)},
			Boot:      api.BootPolicy{Autostart: autostart, Priority: priority, Depends: dropEmpty(depends), StopTimeout: stopTimeout},
			Update:    api.UpdatePolicy{Strategy: api.UpdateStrategy(strategy), Window: window},
			Shell:     api.ShellPath(shell), Prompt: prompt,
			Delegate: zfsDelegation,
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
			if verb != "stop" {
				a.noteShellInstalling(cmd.Context(), c, name)
			}
		}
		return nil
	}
	if verb != "start" {
		cmd.Long += "\n--force skips the graceful shutdown; --grace sets how long the jail gets to shut down."
		cmd.Flags().BoolVar(&force, "force", false, "do not wait for a graceful shutdown")
		cmd.Flags().StringVar(&grace, "grace", "", "time the jail gets to shut down: seconds, or a duration such as 30s")
	}
	return cmd
}

// parseGrace reads --grace: whole seconds, or a duration such as 1m30s.
func parseGrace(s string) (int, error) {
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n, nil
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return seconds(d), nil
	}
	return 0, usagef("--grace must be a number of seconds or a duration such as 30s, not %q", s)
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
	sc := group("snapshot", "Create, list, show and delete jail snapshots",
		`A snapshot is a consistent ZFS snapshot across all datasets of a jail. It is the
target of 'jailops jail rollback --data' and the source of backups. Snapshots
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
	_ = cmd.RegisterFlagCompletionFunc("snapshot", a.completeSnapshots())
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
	var gen, image string
	cmd := &cobra.Command{
		Use:   "upgrade NAME... | -l KEY=VALUE | --all",
		Short: "Move jails to a newer image generation or to a newer release",
		Long: `Move a jail to a newer generation of its image. For a thin jail the root is
replaced and application data is kept; /etc is merged. The jail is restarted
by the server when needed.

With --dry-run the server checks the move and nothing changes; the command
prints the generations involved. Without --generation the newest generation of
the jail's image is used.

--image IMAGE moves a thin FreeBSD jail to another image: a new release
(freebsd-15-1 to freebsd-15-2, made with 'jailops image create IMAGE --release R
--build'). The target is the newest generation of IMAGE, or --generation gN of
it. The move is the same steps as a generation upgrade (snapshot, new root, /etc
merge, swap, start, health check, root rollback on failure) and keeps the data,
the volumes, the addresses and the configuration; from then on the jail follows
IMAGE. The image must be of the same OS and architecture, a release that is not
older, and not newer than the host's kernel (upgrade the host first). A move
is written to the update history, and 'jailops jail rollback' goes back to the
old image. Thick jails and Linux jails are not moved this way.

Several jails are named, selected by label with -l, or all of the project with
--all; they move one at a time and the first failure stops the rest. With
--dry-run every jail is checked first.`,
		Example: `jailops jail upgrade web --dry-run
jailops jail upgrade web
jailops jail upgrade web --generation g4
jailops jail upgrade web --image freebsd-15-2 --dry-run
jailops jail upgrade web db --image freebsd-15-2
jailops jail upgrade -l env=prod --image freebsd-15-2`,
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: completeKind(a, d),
	}
	resolve := a.jailTargets(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		names, bulk, err := resolve(ctx, args)
		if err != nil {
			return err
		}
		if bulk && len(names) == 0 {
			fprintln(a.env.Stderr, "No jails match.")
			return nil
		}
		if gen != "" && len(names) > 1 && image == "" {
			return usagef("--generation names a generation of one image: name one jail, or add --image")
		}
		if strings.ContainsAny(image, "#@") {
			return usagef("--image names an image, not a generation: use --generation gN")
		}
		c, err := a.connect(ctx)
		if err != nil {
			return err
		}
		for _, name := range names {
			if err := a.upgradeOne(ctx, c, name, gen, image); err != nil {
				return err
			}
		}
		return nil
	}
	cmd.Flags().StringVar(&gen, "generation", "", "target generation, gN (default: the newest)")
	cmd.Flags().StringVar(&image, "image", "", "move the jail to the newest generation of this image: a newer release of the same OS (see 'jailops image list')")
	_ = cmd.RegisterFlagCompletionFunc("generation", a.completeJailGenerations())
	_ = cmd.RegisterFlagCompletionFunc("image", a.completeMoveImages())
	return cmd
}

// upgradeOne moves one jail and prints what happened. A move to another image names the releases.
func (a *app) upgradeOne(ctx context.Context, c *sdk.Client, name, gen, image string) error {
	j, err := c.Jails().Get(ctx, name)
	if err != nil {
		return err
	}
	base := imageBase(j.Spec.Image)
	if image != "" {
		base = image
	}
	target := ""
	if gen != "" {
		n, err := parseGeneration("--generation", gen, base)
		if err != nil {
			return err
		}
		target = fmt.Sprintf("g%d", n)
	} else {
		gens, err := collect(c.Images().Generations(ctx, base))
		if err == nil && len(gens) > 0 {
			target = fmt.Sprintf("g%d", gens[len(gens)-1].Number)
		}
	}
	up := api.JailUpgrade{DryRun: a.dryRun, Image: image}
	if gen != "" {
		up.Generation = target
	}
	h, err := handle(c.Jails().Upgrade(ctx, name, up))
	if err != nil {
		return err
	}
	moving := image != "" && image != imageBase(j.Spec.Image)
	if a.dryRun {
		_, op, err := a.await(ctx, h)
		if err != nil {
			return err
		}
		if moving {
			return a.previewMove(name, j, base, target, up, op)
		}
		return a.preview(fmt.Sprintf("upgrade jail %s from %s to %s", name, dash(j.Status.Generation), dash(target)), up)
	}
	if !moving || a.format != fmtTable || !a.waiting() {
		return a.finishOp(ctx, h, "jail", name, "upgraded to "+dash(target), nil)
	}
	_, op, err := a.await(ctx, h)
	if err != nil {
		return err
	}
	m := decodeMove(op)
	a.say("jail/%s moved to %s%s\n", name, dash(m.to(base, target)), m.releases())
	for _, w := range m.Warnings {
		a.say("warning: %s\n", w)
	}
	return nil
}

// moveInfo is what the server says about a move to another image, in the metadata of the operation.
type moveInfo struct {
	Plan     []string `json:"plan"`
	Warnings []string `json:"warnings"`
	Upgrade  *struct {
		From        string   `json:"from"`
		To          string   `json:"to"`
		FromRelease string   `json:"from_release"`
		ToRelease   string   `json:"to_release"`
		Snapshot    string   `json:"snapshot"`
		Kept        []string `json:"kept"`
	} `json:"upgrade"`
}

func decodeMove(op api.Operation) moveInfo {
	var m moveInfo
	if len(op.Metadata) > 0 {
		_ = json.Unmarshal(op.Metadata, &m)
	}
	return m
}

// to is the generation the jail went to: the server's answer, else the image and generation the request named.
func (m moveInfo) to(image, gen string) string {
	if m.Upgrade != nil && m.Upgrade.To != "" {
		return m.Upgrade.To
	}
	if gen == "" {
		return image
	}
	return image + "#" + gen
}

// releases is " (15.1-RELEASE to 15.2-RELEASE)" when the answer has both releases.
func (m moveInfo) releases() string {
	if m.Upgrade == nil || m.Upgrade.FromRelease == "" || m.Upgrade.ToRelease == "" {
		return ""
	}
	if m.Upgrade.FromRelease == m.Upgrade.ToRelease {
		return " (" + m.Upgrade.ToRelease + ")"
	}
	return " (" + m.Upgrade.FromRelease + " to " + m.Upgrade.ToRelease + ")"
}

// previewMove prints the plan of a move of a jail to another image that the server has checked: the two generations
// with their releases, what the move keeps, and the steps.
func (a *app) previewMove(name string, j api.Jail, image, gen string, req api.JailUpgrade, op api.Operation) error {
	if a.format == fmtJSON || a.format == fmtYAML {
		return a.preview("move jail "+name+" to image "+image, req)
	}
	m := decodeMove(op)
	from := imageBase(j.Spec.Image) + "#" + strings.TrimPrefix(j.Status.Generation, imageBase(j.Spec.Image)+"#")
	if m.Upgrade != nil && m.Upgrade.From != "" {
		from = m.Upgrade.From
	}
	fprintf(a.env.Stdout, "dry-run: would move jail %s from %s to %s%s\n", name, from, m.to(image, gen), m.releases())
	if m.Upgrade != nil {
		if len(m.Upgrade.Kept) > 0 {
			fprintf(a.env.Stdout, "  kept: %s, the volumes, the addresses and the configuration\n", strings.Join(m.Upgrade.Kept, ", "))
		}
		if m.Upgrade.Snapshot != "" {
			fprintf(a.env.Stdout, "  rollback point: snapshot %s ('jailops jail rollback %s' goes back to %s)\n", m.Upgrade.Snapshot, name, from)
		}
	}
	for i, st := range m.Plan {
		fprintf(a.env.Stdout, "  %2d. %s\n", i+1, st)
	}
	for _, w := range m.Warnings {
		fprintf(a.env.Stdout, "warning: %s\n", w)
	}
	return nil
}

// completeMoveImages completes the images a jail can move to: the same OS and architecture as its image, a release
// that is not older, and not its own image.
func (a *app) completeMoveImages() completer {
	return a.completeWith(func(ctx context.Context, c *sdk.Client, args []string) ([]string, error) {
		if len(args) == 0 {
			return nil, nil
		}
		j, err := c.Jails().Get(ctx, args[0])
		if err != nil {
			return nil, err
		}
		imgs, err := collect(c.Images().List(ctx))
		cur := imageBase(j.Spec.Image)
		var from *api.Image
		for i := range imgs {
			if imgs[i].Metadata.Name == cur {
				from = &imgs[i]
			}
		}
		var out []string
		for _, i := range imgs {
			if i.Metadata.Name == cur || from == nil || i.Spec.OS != from.Spec.OS || i.Spec.Arch != from.Spec.Arch || releaseOlder(i.Spec.Release, from.Spec.Release) {
				continue
			}
			out = append(out, i.Metadata.Name)
		}
		return out, err
	})
}

// releaseOlder says whether release a ("15.1-RELEASE") is older than b. Releases that do not read as MAJOR.MINOR
// are never older (the server decides).
func releaseOlder(a, b string) bool {
	am, an, ok1 := releaseNumbers(a)
	bm, bn, ok2 := releaseNumbers(b)
	return ok1 && ok2 && (am < bm || am == bm && an < bn)
}

func releaseNumbers(r string) (int, int, bool) {
	v, _, _ := strings.Cut(r, "-")
	x, y, ok := strings.Cut(v, ".")
	if !ok {
		return 0, 0, false
	}
	m, e1 := strconv.Atoi(x)
	n, e2 := strconv.Atoi(y)
	return m, n, e1 == nil && e2 == nil
}

// completeJailGenerations completes a generation of the image of the jail
// named first.
func (a *app) completeJailGenerations() completer {
	return a.completeWith(func(ctx context.Context, c *sdk.Client, args []string) ([]string, error) {
		if len(args) == 0 {
			return nil, nil
		}
		j, err := c.Jails().Get(ctx, args[0])
		if err != nil {
			return nil, err
		}
		return generationNames(ctx, c, imageBase(j.Spec.Image))
	})
}

// preview prints a dry-run result the server has already checked.
func (a *app) preview(action string, request any) error {
	if a.format == fmtJSON || a.format == fmtYAML {
		return a.emit(map[string]any{"dry_run": true, "action": action, "request": request}, nil, nil, nil)
	}
	fprintf(a.env.Stdout, "dry-run: would %s\n", action)
	return nil
}

func (a *app) jailConvert(d *kindDesc[api.Jail]) *cobra.Command {
	var layout, image string
	cmd := &cobra.Command{
		Use:   "convert NAME --layout thin|thick",
		Short: "Change the layout of a FreeBSD jail's root: thin to thick, or thick to thin",
		Long: `Change how the root of a FreeBSD jail is stored.

A thin jail's root is a clone of an image generation, and its data lives in datasets of its own (/etc, /usr/local,
/var, /home). --layout thick makes the root a full copy of its own with that data folded into it: the jail no longer
depends on the image, and its generation can be deleted.

A thick jail's root is a full copy. --layout thin gives the jail a clone of an image generation as its root: the
generation the thick root was copied from, or the one --image names (g3, or NAME#g3). Only the directories of the
persistent paths (/etc, /usr/local, /var/db, /var/log, /var/mail, /var/spool, /home, /root) keep the jail's data;
everything else comes from the image, so a package installed outside /usr/local or a changed system file is replaced.
--dry-run prints exactly what the plan keeps and what it replaces.

The jail is stopped for the conversion and started again when it ran. A snapshot of every dataset is taken first
(jo-pre-convert-...); a failure at any step puts the old layout back. The old root stays as root.old for the
retention period. Linux jails are thick and cannot be converted.`,
		Example: `jailops jail convert web --layout thick --dry-run
jailops jail convert web --layout thick
jailops jail convert web --layout thin
jailops jail convert web --layout thin --image g3`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKind(a, d),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, name := cmd.Context(), args[0]
			l := api.Layout(layout)
			if l != api.LayoutThin && l != api.LayoutThick {
				return usagef("--layout is thin or thick")
			}
			if image != "" && l != api.LayoutThin {
				return usagef("--image names the generation of a thin root: add --layout thin")
			}
			c, err := a.connect(ctx)
			if err != nil {
				return err
			}
			h, err := handle(c.Jails().Convert(ctx, name, api.JailConvert{Layout: l, Image: image, DryRun: a.dryRun}))
			if err != nil {
				return err
			}
			if !a.dryRun {
				return a.finishOp(ctx, h, "jail", name, "converted to "+layout, nil)
			}
			v, _, err := a.await(ctx, h)
			if err != nil {
				return err
			}
			r, _ := v.(api.JailConvertResult)
			if a.format != fmtTable {
				return a.emit(r, nil, nil, nil)
			}
			if !r.Changed {
				fprintf(a.env.Stdout, "jail %s is %s already: nothing to do\n", name, r.To)
				return nil
			}
			fprintf(a.env.Stdout, "dry-run: would convert jail %s from %s to %s (image %s)\n", name, r.From, r.To, dash(r.Image))
			if len(r.Kept) > 0 {
				fprintf(a.env.Stdout, "keeps: %s\n", strings.Join(r.Kept, ", "))
			}
			if r.Replaced != "" {
				fprintf(a.env.Stdout, "replaces: %s\n", r.Replaced)
			}
			for _, w := range r.Warnings {
				fprintf(a.env.Stdout, "warning: %s\n", w)
			}
			fprintf(a.env.Stdout, "plan:\n")
			for i, st := range r.Steps {
				fprintf(a.env.Stdout, "  %2d. %s\n", i+1, st)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&layout, "layout", "", "the layout the jail gets: thin or thick")
	f.StringVar(&image, "image", "", "with --layout thin: the generation to take the root from, gN or NAME#gN (default: the one the root was copied from)")
	_ = cmd.RegisterFlagCompletionFunc("layout", cobra.FixedCompletions([]string{"thin", "thick"}, cobra.ShellCompDirectiveNoFileComp))
	return cmd
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
		Short: "Give a jail bash and the JailOps prompt, as new jails get them",
		Long: `Give a running FreeBSD jail the shell and prompt a new jail gets: the shell and
prompt keys the jail leaves empty are set from the host's defaults (jails.shell
and jails.prompt of jailopsd.yaml: bash and the default prompt), bash is
installed into the jail from the host when it is the shell and missing, and the
prompt is written. Running it again repairs the files and changes nothing else.

Two files in /usr/local/etc/profile.d are sourced from a marked block at the top of
/usr/local/etc/profile (login shells) and /usr/local/etc/bash.bashrc (other
interactive shells), so they survive OS updates: jailops-env.sh exports
JAILOPS_JAIL, JAILOPS_PROJECT and JAILOPS_HOST, and jailops-prompt.sh sets the
prompt. A user's own ~/.bashrc, ~/.bash_profile or ~/.profile wins over them.
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

// autostartCell is the AUTOSTART column of a jail list: whether the jail starts when the host boots.
func autostartCell(j api.Jail) string {
	if j.Spec.Boot.Autostart {
		return "yes"
	}
	return "-"
}
