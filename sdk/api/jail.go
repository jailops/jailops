package api

import "time"

// OS is the guest operating system family of a jail or image.
type OS string

const (
	OSFreeBSD OS = "freebsd"
	OSLinux   OS = "linux" // Linuxulator; Alpine first, Debian later
)

// Jail is a managed jail. Jails are ScopeHost objects.
type Jail struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     JailSpec   `json:"spec"`
	Status   JailStatus `json:"status"`
}

// JailSpec is the desired state of a jail.
type JailSpec struct {
	// Comment is what the object is for: one line of free text.
	Comment string `json:"comment,omitempty"`
	// Notes is a long text about the object: what it does, how it was set up, who to ask. Multi-line, at most
	// MaxNotes bytes (ValidNotes).
	Notes string `json:"notes,omitempty"`
	// Image is an image name such as "freebsd-15-1" (its newest generation),
	// or a pinned generation such as "freebsd-15-1#g3".
	Image string `json:"image"`
	// OS is the guest family. It defaults to the image's OS and is set by the
	// server so validators and renderers can apply OS-specific rules.
	OS OS `json:"os,omitempty"`
	// Storage names the storage the jail's datasets are created on. It is
	// empty only when a profile names one; otherwise the jail is refused (nothing is filled in).
	Storage string `json:"storage,omitempty"`
	// Hostname defaults to the jail name.
	Hostname string `json:"hostname,omitempty"`

	Update    UpdatePolicy    `json:"update,omitempty"`
	Resources Resources       `json:"resources,omitempty"`
	Network   []NetworkAttach `json:"network,omitempty"`
	Firewall  FirewallAttach  `json:"firewall,omitempty"`
	Volumes   []VolumeAttach  `json:"volumes,omitempty"`
	Boot      BootPolicy      `json:"boot,omitempty"`
	// Packages is reserved: installing packages at creation is not implemented and creation with it set is refused.
	Packages []string          `json:"packages,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	// Shell is the path of the shell `jailops shell` and the console start inside the jail; empty means /bin/sh. It
	// is a management setting and does not touch /etc/passwd. A new FreeBSD jail gets BashPath (bash installed at
	// its first start) unless the request or jails.shell of jailopsd.yaml says otherwise.
	Shell string `json:"shell,omitempty"`
	// Prompt is the bash prompt of a FreeBSD jail: "default" (DefaultPS1 in the colour of jails.prompt_color),
	// "short" (ShortPS1, the same without the tag), "none" (no prompt file; the variables of EnvFile stay), or a PS1
	// text (CheckPrompt) that may hold the words {jail}, {project} and {host} (ExpandPrompt: they read the variables of
	// EnvFile). JailOps writes it into PromptFile in the jail's /usr/local; empty leaves the jail's files alone. A new jail whose shell is bash gets jails.prompt of
	// jailopsd.yaml.
	Prompt string `json:"prompt,omitempty"`
	// Provision is a short, repeatable creation step: packages, files,
	// services and commands, run once and then marked complete in the status
	// (design 8.5). There is no agent in the jail and no convergence.
	Provision *Provision `json:"provision,omitempty"`
	// Profiles name reusable sets of keys and devices, merged in order; the
	// jail's own keys and devices override them device by device and name by
	// name.
	Profiles []string `json:"profiles,omitempty"`
	// Devfs exposes selected device nodes through an allow-listed devfs ruleset.
	Devfs []DevfsDevice `json:"devfs,omitempty"`
	// Expose lets traffic from the Internet reach the jail on its ports (jailops expose): an allow rule in the jail's anchor,
	// on the uplink for the addresses the upstream routes, and a port forward for an IPv4 address behind NAT.
	Expose []ExposeEntry `json:"expose,omitempty"`
	// Encrypted is reserved: native ZFS encryption of jails is not implemented and creation with it set is refused.
	Encrypted bool `json:"encrypted,omitempty"`
	// Delegate hands the jail one ZFS dataset of its own, <container>/delegated (jailed=on, zfs.dataset in
	// jail.conf, allow.mount.zfs): root in the jail can create datasets, snapshots and mounts below it, outside
	// the rules JailOps applies to the jail's data. Only a global admin may set it, the host's priv.conf must allow it
	// (admin-zfs-delegation), and a project is not a security boundary for a jail that has it.
	Delegate bool `json:"zfs_delegation,omitempty"`
	// SysVIPC gives the jail System V message queues, semaphores and shared memory of its own (sysvmsg, sysvsem
	// and sysvshm = new in jail.conf): separate from the host's and from every other jail's. Some servers
	// (PostgreSQL) need it. Changing it takes effect at the next start.
	SysVIPC bool `json:"sysvipc,omitempty"`
}

// UpdatePolicy controls updates (design 8.4). The zero value is manual:
// nothing happens unless a user runs it.
type UpdatePolicy struct {
	Healthcheck string `json:"healthcheck,omitempty"` // command run in the jail
	// Mode is manual (default), notify (check on the schedule and report) or
	// managed (check and apply on the schedule).
	Mode UpdateMode `json:"mode,omitempty"`
	// Schedule is a five-field cron expression; required for notify and managed.
	Schedule string `json:"schedule,omitempty"`
	// Packages updates the ports packages of a FreeBSD jail from the host (pkg). A Linux jail carries no update
	// policy but the health check: it looks after its own packages.
	Packages bool `json:"packages,omitempty"`
	// OS upgrades the FreeBSD base of the jail in place, inside its release (as jail upgrade): with mode managed on the
	// jail's schedule, otherwise on demand by a global administrator. Turning it on together with mode managed needs a
	// global administrator (the request is forbidden for anyone else).
	OS bool `json:"os,omitempty"`
	// Reboot is kept for compatibility: JailOps never stops or restarts a
	// running jail for an update, so auto behaves as manual (the default): the
	// update record names the processes whose restart is pending.
	Reboot UpdateReboot `json:"reboot,omitempty"`
}

// UpdateMode selects what the daemon does on the update schedule.
type UpdateMode string

// Update modes.
const (
	UpdateModeManual  UpdateMode = "manual"
	UpdateModeNotify  UpdateMode = "notify"
	UpdateModeManaged UpdateMode = "managed"
)

// UpdateReboot is the restart policy of an update. Both values report a
// pending restart; JailOps never restarts a jail for an update.
type UpdateReboot string

// Reboot policies.
const (
	RebootManual UpdateReboot = "manual"
	RebootAuto   UpdateReboot = "auto"
)

// Resources limits a jail. CPU, Memory and PIDs are enforced with rctl rules added when the jail starts (they need
// kern.racct.enable=1), Disk and DiskReservation are ZFS properties of the jail's container dataset, and States is
// the limit of pf states each rule of the jail's anchor may create.
type Resources struct {
	CPU    int    `json:"cpu,omitempty"`    // cores: rctl pcpu, the jail is slowed down to that share
	Memory string `json:"memory,omitempty"` // "2G": rctl swapuse, the anonymous memory the jail may reserve; an allocation beyond it fails
	Disk   string `json:"disk,omitempty"`   // ZFS quota of the container: root, delegated dataset and snapshots
	PIDs   int    `json:"pids,omitempty"`   // rctl maxproc: fork fails at that many processes
	// DiskReservation is an optional ZFS reservation of the jail's container.
	DiskReservation string `json:"disk_reservation,omitempty"`
	// States is the maximum number of pf states each pass rule of the jail's anchor may create. 0 means the host's
	// default (jails.max_states of jailopsd.yaml, 10000), which is itself 0 for no limit on a host that sets it so.
	States int `json:"states,omitempty"`
	// CPUSet pins the jail to CPUs of the host (cpuset(1)): a list such as "2,3" or "0-3,6", kept in its canonical
	// form. It needs no resource accounting. A CPU the host does not have stops the jail from starting.
	CPUSet string `json:"cpuset,omitempty"`
}

// NetworkAttach connects a jail to a managed network. Every jail uses VNET;
// shared-IP jails are not supported.
type NetworkAttach struct {
	// Name is the device name ("eth0", "eth1", ... when empty). Devices are
	// identified by name when profiles are merged.
	Name    string `json:"name,omitempty"`
	Network string `json:"network"`
	// MTU of this interface (both ends of its epair). Zero takes the network's.
	MTU int `json:"mtu,omitempty"`
	// Egress is a rate limit in bits per second ("10M" is 10 Mbit/s, "1G" 1 Gbit/s; decimal units) for what the jail
	// sends through this device, whoever started the connection. The host enforces it with a dummynet pipe that the
	// jail's anchor sends the traffic into.
	Egress string `json:"egress,omitempty"`
	// Ingress is the same limit for what the jail receives through this device, from whoever sends it.
	Ingress string `json:"ingress,omitempty"`
	// IPv4 and IPv6 are "auto" (allocate from the network's range), "none",
	// or a fixed address. The allocation is written back to the spec so it
	// travels with the jail.
	IPv4 string `json:"ipv4,omitempty"`
	IPv6 string `json:"ipv6,omitempty"`
	MAC  string `json:"mac,omitempty"` // derived from the jail UUID when empty
}

// FirewallAttach selects the policies applied to the jail's anchor.
type FirewallAttach struct {
	// Policies are the names of the jail's own policies, rendered in this order after the project's.
	Policies []string `json:"policies,omitempty"`
	Tags     []string `json:"tags,omitempty"`
}

// VolumeAttach attaches a volume of the jail's project to the jail: the volume's dataset is mounted into the jail at
// Mount with nullfs, read-only when ReadOnly is set. The volume is its own object (see Volume): creating, sizing,
// owning, snapshotting and deleting it are done there, and one volume can be attached to several jails.
type VolumeAttach struct {
	// Name is the name of the volume (jailops volume list).
	Name     string `json:"volume"`
	Mount    string `json:"mount"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

// DevfsDevice exposes one device node of an allow-list (tun, tap, pf, bpf,
// fuse, dri) inside the jail. Exposing devices needs the admin role.
type DevfsDevice struct {
	// Name is the device name used to override it from a profile.
	Name   string `json:"name"`
	Device string `json:"device"`
}

// ExposeEntry is one exposure of a jail: the ports of one protocol that the Internet reaches, on the families named.
type ExposeEntry struct {
	// Proto is "tcp" (the default) or "udp".
	Proto string `json:"proto,omitempty"`
	// Ports are the jail's ports: "80", or a range "8000-8100" (a range is not forwarded).
	Ports []string `json:"ports"`
	// Families are "4" and "6": the families of the jail's addresses that were exposed. They are resolved when the
	// command runs and never widened later by a change of the jail's networks.
	Families []string `json:"families,omitempty"`
	// From restricts who may connect: addresses, prefixes and address sets (set:NAME). Empty means anyone.
	From []string `json:"from,omitempty"`
	// HostPort is the host port of the IPv4 forward when it differs from the jail's port (one port only).
	HostPort int `json:"host_port,omitempty"`
	// Listen is the host address of the IPv4 forward; empty means the uplink's address.
	Listen  string `json:"listen,omitempty"`
	Comment string `json:"comment,omitempty"`
}

// Protocol is the entry's protocol: tcp when none is written.
func (e ExposeEntry) Protocol() string {
	if e.Proto == "" {
		return "tcp"
	}
	return e.Proto
}

// DefaultStopTimeout is the stop timeout of a jail that does not set boot.stop_timeout_seconds, in seconds: the time the stop
// commands of the jail get to shut a service down cleanly, and the jail then to exit after SIGTERM, before it is removed.
const DefaultStopTimeout = 30

// MaxStopTimeout is the largest BootPolicy.StopTimeout, in seconds.
const MaxStopTimeout = 3600

// BootPolicy orders jails at host start and stop.
type BootPolicy struct {
	Autostart bool     `json:"autostart,omitempty"`
	Priority  int      `json:"priority,omitempty"`
	Depends   []string `json:"depends,omitempty"`
	// StopTimeoutSeconds is how many seconds the jail has to shut down by itself before it is killed (jail(8) stop.timeout);
	// zero is DefaultStopTimeout.
	StopTimeoutSeconds int `json:"stop_timeout_seconds,omitempty"`
}

// PowerState is the observed run state of a jail.
type PowerState string

const (
	PowerStopped  PowerState = "stopped"
	PowerStarting PowerState = "starting"
	PowerRunning  PowerState = "running"
	PowerStopping PowerState = "stopping"
	PowerError    PowerState = "error"
	// PowerLocked is a stopped jail whose encrypted volumes wait for their passphrase.
	PowerLocked PowerState = "locked"
)

// ShellSetupStatus is the machine form of the ShellSetup condition: State is ShellSetupFailed. Step is kept for the
// frozen contract and is always empty (it named the step of a bash installation, which JailOps no longer does).
type ShellSetupStatus struct {
	State string `json:"state"`
	Step  string `json:"step,omitempty"`
}

// JailStatus is the observed state of a jail on its host.
type JailStatus struct {
	Power PowerState `json:"power"`
	// JID is the kernel jail id while the jail runs.
	JID int `json:"jid,omitempty"`
	// ImageGeneration is the image generation the jail's root was copied from, written IMAGE#gN. It is where the
	// jail came from, not something it depends on.
	ImageGeneration string `json:"image_generation,omitempty"`
	// FreeBSDVersion is the FreeBSD version installed in the jail (what its last upgrade installed, else the version of
	// the generation it was copied from); empty when it was not recorded.
	FreeBSDVersion string `json:"freebsd_version,omitempty"`
	// Addresses are the addresses actually configured.
	Addresses []string  `json:"addresses,omitempty"`
	StartedAt time.Time `json:"started_at,omitzero"`
	// Drift lists differences between the rendered and the live system.
	Drift      []string    `json:"drift,omitempty"`
	Conditions []Condition `json:"conditions,omitempty"`
	// ShellSetup says that the latest writing of the jail's prompt failed; nil when it did not.
	ShellSetup *ShellSetupStatus `json:"shell_setup,omitempty"`
	// ProvisionedAt is when the jail's provisioning last completed; zero when
	// it has not run (or the jail has none).
	ProvisionedAt time.Time `json:"provisioned_at,omitzero"`
	// Warnings say what the spec asks for and the host does not do (rate
	// limits are stored and not enforced yet).
	Warnings []string `json:"warnings,omitempty"`
	// Locked names the encrypted volumes whose key is not loaded: the jail does not start until it is unlocked.
	Locked []string `json:"locked,omitempty"`
	// DiskUsed is what the jail's own datasets take on the pool (`used` of the root and the delegated dataset, their
	// snapshots included); the volumes it attaches are the project's. Zero when ZFS could not be read. Filled by list
	// and get.
	DiskUsed int64 `json:"disk_used_bytes,omitempty"`
	// Exposed is what the Internet reaches on the jail: pass rules on an uplink and port forwards, from the anchor the
	// renderer builds. Filled by list and get; empty when nothing is reachable or the anchor could not be built.
	Exposed []ExposedEntry `json:"exposed,omitempty"`
}

// JailAction is the body of PUT /api/v1/jails/{name}/state.
type JailAction struct {
	Action         string `json:"action"` // start, stop, restart
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	Force          bool   `json:"force,omitempty"`
}

// Passphrase limits of encrypted volumes. ZFS itself accepts 8 to 512 bytes; JailOps asks for at least 12.
const (
	MinPassphrase = 12
	MaxPassphrase = 512
)

// CheckPassphrase reports whether p is acceptable as a new passphrase: its length, and no line break or NUL
// (ZFS reads the passphrase as one line).
func CheckPassphrase(p string) error {
	if len(p) < MinPassphrase || len(p) > MaxPassphrase {
		return Errorf(CodeInvalid, "the passphrase must be between %d and %d bytes long", MinPassphrase, MaxPassphrase)
	}
	for i := 0; i < len(p); i++ {
		if p[i] == '\n' || p[i] == '\r' || p[i] == 0 {
			return Errorf(CodeInvalid, "the passphrase must not contain a line break or a NUL byte")
		}
	}
	return nil
}

// RollbackScope selects what a rollback restores.
type RollbackScope string

const (
	// RollbackRoot returns the root to the snapshot of the latest upgrade (a stopped jail only). It is the default.
	RollbackRoot RollbackScope = "root"
	// RollbackData rolls the delegated dataset back to a snapshot and discards everything written since; RollbackAll
	// rolls the root and the delegated dataset back to it.
	RollbackData RollbackScope = "data"
	RollbackAll  RollbackScope = "all"
)

// JailRollback is the body of POST /api/v1/jails/{name}/rollback.
type JailRollback struct {
	Scope    RollbackScope `json:"scope,omitempty"`
	Snapshot string        `json:"snapshot,omitempty"`
	// ConfirmDataLoss must be true for RollbackData and RollbackAll.
	ConfirmDataLoss bool `json:"confirm_data_loss,omitempty"`
}

// JailCopy is the body of POST /api/v1/jails/{name}: a copy of the jail, with its data, under a new name in the same
// project and on the same storage. The copy is independent of the source (a full send and receive, never a clone), has
// addresses of its own and no volume attached. It is made while the source runs: the datasets are snapshotted in one
// atomic transaction, so the copy is what a power loss at that moment would have left.
type JailCopy struct {
	// Name is the name of the copy.
	Name string `json:"name"`
	// Labels replace the labels of the source when set.
	Labels map[string]string `json:"labels,omitempty"`
	// Snapshot copies the state of an existing snapshot of the source (see JailSnapshot) instead of the present one.
	Snapshot string `json:"snapshot,omitempty"`
}

// JailUpgrade is the body of POST /api/v1/jails/{name}/upgrade.
type JailUpgrade struct {
	// Release moves a FreeBSD jail to a new FreeBSD release: the base packages are upgraded in place against the
	// repository of that release (15.2-RELEASE reads base_release_2; STABLE, CURRENT and the pre-release spellings such
	// as 16.0-CURRENT, 15.2-STABLE or 15.2-PRERELEASE read base_latest). It is not older than the release installed in
	// the jail and not newer than the host's kernel. Empty keeps the installed release: the base packages are brought to
	// the newest of its repository. Not with AddPackages.
	Release string `json:"release,omitempty"`
	// NoPackages leaves the ports packages of the jail alone: only the base is upgraded.
	NoPackages bool `json:"no_packages,omitempty"`
	// Force goes on although the jail's health check fails before the upgrade (a rollback point that is already
	// broken is not a safe base).
	Force bool `json:"force,omitempty"`
	// AddPackages installs base packages (FreeBSD-*, for example FreeBSD-ssh) into a FreeBSD jail from the host,
	// in place, with the snapshot and health check of an upgrade; the FreeBSD-base repository stays disabled inside
	// the jail.
	AddPackages []string `json:"add_packages,omitempty"`
	DryRun      bool     `json:"dry_run,omitempty"`
}

// JailBase is the body of GET /api/v1/jails/{name}/base: the base packages (FreeBSD-*) installed in a FreeBSD jail.
type JailBase struct {
	Jail     string            `json:"jail"`
	Packages []JailBasePackage `json:"packages"`
}

// JailBasePackage is one installed base package.
type JailBasePackage struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// JailSnapshot is a consistent snapshot across all datasets of a jail.
type JailSnapshot struct {
	Metadata ObjectMeta         `json:"metadata"`
	Spec     JailSnapshotSpec   `json:"spec"`
	Status   JailSnapshotStatus `json:"status"`
}

type JailSnapshotSpec struct {
	Jail    string `json:"jail"`
	Comment string `json:"comment,omitempty"`
}

type JailSnapshotStatus struct {
	// Written is the number of bytes written to the data datasets since this
	// snapshot, which is what a data rollback would discard.
	Written int64 `json:"written_bytes,omitempty"`
}

// JailUsage is what one jail uses, measured when it is asked for
// (GET /api/v1/jails/{name}/usage). It is never part of the Jail document, so
// listing jails stays cheap. A part that could not be measured carries the
// reason instead of zeros.
type JailUsage struct {
	Jail       string     `json:"jail"`
	Power      PowerState `json:"power"`
	MeasuredAt time.Time  `json:"measured_at"`
	// Accounting is the kernel's resource accounting of the jail (racct);
	// nil while the jail is not running.
	Accounting *JailAccounting `json:"accounting,omitempty"`
	Disk       JailDiskUsage   `json:"disk"`
}

// JailAccounting is the running jail's share of the host as racct counts it.
type JailAccounting struct {
	// Error says why nothing was measured ("racct is off: ..."); the numbers
	// are zero then and mean nothing.
	Error string `json:"error,omitempty"`
	// Processes is the number of processes in the jail.
	Processes int `json:"processes"`
	// CPUSeconds is the CPU time the jail consumed since it started, in
	// seconds, all cores added up.
	CPUSeconds int64 `json:"cpu_seconds"`
	// MemoryBytes is the resident memory of the jail's processes.
	MemoryBytes int64 `json:"memory_bytes"`
	// SwapBytes is the anonymous (swap-backed) memory the jail has reserved: the counter `resources.memory` limits
	// (rctl swapuse). It is not the resident memory: a jail can be under its limit and show a larger resident size.
	SwapBytes int64 `json:"swap_bytes"`
}

// JailDiskUsage is the space of the jail's datasets, running or not.
type JailDiskUsage struct {
	// Error says why nothing was measured.
	Error string `json:"error,omitempty"`
	// RootBytes is `used` of the root dataset: what the root takes on the
	// pool by itself (blocks cloned from the image are not counted).
	RootBytes int64 `json:"root_bytes"`
	// RootReferencedBytes is `referenced` of the root: the size of the root's
	// tree, shared blocks included.
	RootReferencedBytes int64 `json:"root_referenced_bytes"`
	// DataBytes is `used` of the dataset the jail manages itself (zfs_delegation), its snapshots included; the volumes
	// it attaches are the project's and are not counted.
	DataBytes int64 `json:"data_bytes"`
}

// JailAudit is the body of GET /api/v1/jails/{name}/audit: a read-only look at a FreeBSD jail, running or stopped, in
// two groups. The security checks look at what crosses the boundary of the jail or opens it (a link out of the root, a
// second account with uid 0, an unknown package repository); the practice checks at what a jail on JailOps does better
// (cron jitter, periodic reports to files). Nothing is changed and the jail is not stopped.
type JailAudit struct {
	Jail    string `json:"jail"`
	Running bool   `json:"running"`
	// Critical and Warnings count the checks with that result.
	Critical int              `json:"critical"`
	Warnings int              `json:"warnings"`
	Checks   []JailAuditCheck `json:"checks"`
}

// JailAuditCheck is one check of an audit: its result, one line of what was found and, unless it passed, the fix.
type JailAuditCheck struct {
	// ID names the check (accounts.uid0, pkg.repositories, cron.jitter, ...).
	ID string `json:"id"`
	// Group is "security" or "practice".
	Group  string          `json:"group"`
	Result JailAuditResult `json:"result"`
	Detail string          `json:"detail"`
	Fix    string          `json:"fix,omitempty"`
}

// JailAuditResult is the result of one audit check. Only a security check is ever critical.
type JailAuditResult string

const (
	AuditPass     JailAuditResult = "pass"
	AuditInfo     JailAuditResult = "info"
	AuditWarn     JailAuditResult = "warn"
	AuditCritical JailAuditResult = "critical"
)
