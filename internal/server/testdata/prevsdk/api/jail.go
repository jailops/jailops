package api

import "time"

// OS is the guest operating system family of a jail or image.
type OS string

const (
	OSFreeBSD OS = "freebsd"
	OSLinux   OS = "linux" // Linuxulator; Alpine first, Debian later
)

// Layout selects how a FreeBSD jail's root is built from an image generation.
//
// JailOps is ZFS-native: a thin jail's root is a ZFS clone of an immutable
// image generation, and everything that must survive an update lives in
// separate datasets. There is no nullfs-based thin layout.
type Layout string

const (
	LayoutThin  Layout = "thin"
	LayoutThick Layout = "thick" // full independent copy
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
	// Image is an image name such as "freebsd-15-1" (its newest generation),
	// or a pinned generation such as "freebsd-15-1#g3".
	Image string `json:"image"`
	// OS is the guest family. It defaults to the image's OS and is set by the
	// server so validators and renderers can apply OS-specific rules.
	OS OS `json:"os,omitempty"`
	// Storage names the storage the jail's datasets are created on. It is
	// empty only when a profile names one; otherwise the jail is refused (nothing is filled in).
	Storage string `json:"storage,omitempty"`
	// Layout defaults to thin for FreeBSD jails and thick for Linux jails.
	Layout Layout `json:"layout,omitempty"`
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
	// Packages updates the packages inside the jail from the host (pkg or apk).
	Packages bool `json:"packages,omitempty"`
	// OS moves the jail to a newer image generation (root swap, design 4.3).
	OS bool `json:"os,omitempty"`
	// Reboot says whether a jail restart that an update needs is applied
	// automatically (auto) or left to the user (manual, the default).
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

// UpdateReboot says whether a restart an update needs is automatic.
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
	Disk   string `json:"disk,omitempty"`   // ZFS quota of the container: root, persistent datasets, volumes and snapshots
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

// ShellSetupStatus is the machine form of the ShellSetup condition: State is ShellSetupInstalling or ShellSetupFailed,
// Step names the step of an installation (for example "fetching bash").
type ShellSetupStatus struct {
	State string `json:"state"`
	Step  string `json:"step,omitempty"`
}

// JailStatus is the observed state of a jail on its host.
type JailStatus struct {
	Power PowerState `json:"power"`
	// JID is the kernel jail id while the jail runs.
	JID int `json:"jid,omitempty"`
	// ImageGeneration is the image generation the root currently uses, written IMAGE#gN.
	ImageGeneration string `json:"image_generation,omitempty"`
	// FreeBSDVersion is the FreeBSD version of that generation (ImageGeneration.FreeBSDVersion); empty when it was
	// not recorded.
	FreeBSDVersion string `json:"freebsd_version,omitempty"`
	// Addresses are the addresses actually configured.
	Addresses []string  `json:"addresses,omitempty"`
	StartedAt time.Time `json:"started_at,omitzero"`
	// Drift lists differences between the rendered and the live system.
	Drift      []string    `json:"drift,omitempty"`
	Conditions []Condition `json:"conditions,omitempty"`
	// ShellSetup is the background setup of the jail's shell; nil when none is running and the latest did not fail.
	ShellSetup *ShellSetupStatus `json:"shell_setup,omitempty"`
	// ProvisionedAt is when the jail's provisioning last completed; zero when
	// it has not run (or the jail has none).
	ProvisionedAt time.Time `json:"provisioned_at,omitzero"`
	// Warnings say what the spec asks for and the host does not do (rate
	// limits are stored and not enforced yet).
	Warnings []string `json:"warnings,omitempty"`
	// Locked names the encrypted volumes whose key is not loaded: the jail does not start until it is unlocked.
	Locked []string `json:"locked,omitempty"`
	// DiskUsed is what the jail's own datasets take on the pool (`used` of the root and of the persistent datasets, the
	// space a thin root shares with its image excluded); the volumes it attaches are the project's. Zero when ZFS
	// could not be read. Filled by list and get.
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
	// RollbackRoot returns only the OS root to the previous generation and
	// leaves application data untouched. It is the default.
	RollbackRoot RollbackScope = "root"
	// RollbackData rolls the data datasets back to a snapshot and discards
	// everything written since.
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

// JailConvert is the body of POST /api/v1/jails/{name}/convert: the jail's root changes layout. Thin to thick makes the root
// a full copy of its own (the persistent datasets are folded into it); thick to thin gives the jail a clone of an image
// generation as its root and the directories of the persistent datasets keep the jail's data. The jail is stopped for it
// and started again when it ran. Linux jails are thick and stay so.
type JailConvert struct {
	// Layout is the layout the jail gets: thin or thick.
	Layout Layout `json:"layout"`
	// Image is the image generation a jail converted to thin takes its root from: NAME (the newest generation) or
	// NAME#gN. Empty: the generation the thick root was copied from, which is the exact image the jail is made of.
	Image  string `json:"image,omitempty"`
	DryRun bool   `json:"dry_run,omitempty"`
}

// JailConvertResult is the result of a convert operation (the plan of a dry run).
type JailConvertResult struct {
	Jail string `json:"jail"`
	From Layout `json:"from"`
	To   Layout `json:"to"`
	// Image is the image the jail uses afterwards.
	Image string `json:"image,omitempty"`
	// Changed is false when the jail has the layout already.
	Changed bool `json:"changed"`
	DryRun  bool `json:"dry_run,omitempty"`
	// Steps are the steps of the plan, in order.
	Steps []string `json:"steps,omitempty"`
	// Kept are the paths of the jail that carry its data over the conversion; Replaced says what comes from the image
	// instead of the old root.
	Kept     []string `json:"kept,omitempty"`
	Replaced string   `json:"replaced,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// JailUpgrade is the body of POST /api/v1/jails/{name}/upgrade.
type JailUpgrade struct {
	// Generation pins the target, written gN or IMAGE#gN; empty means the newest generation of the
	// jail's image (or of Image).
	Generation string `json:"generation,omitempty"`
	// Image moves a thin FreeBSD jail to another image of the same OS and architecture whose release is the same
	// or newer (a new release): the jail keeps its data, volumes, addresses and configuration, and follows that
	// image from then on. Empty keeps the jail's image.
	Image  string `json:"image,omitempty"`
	DryRun bool   `json:"dry_run,omitempty"`
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
	// pool by itself (a thin jail's root shares the rest with its image).
	RootBytes int64 `json:"root_bytes"`
	// RootReferencedBytes is `referenced` of the root: the size of the root's
	// tree, shared blocks included.
	RootReferencedBytes int64 `json:"root_referenced_bytes"`
	// DataBytes is `used` of the data datasets (persistent paths and
	// volumes) together, their snapshots included.
	DataBytes int64 `json:"data_bytes"`
}
