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
	// Image is an alias such as "freebsd-15.0@stable" or a pinned generation
	// such as "freebsd-15.0#g3".
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
	Volumes   []VolumeMount   `json:"volumes,omitempty"`
	// HostMounts mount existing host directories, usually datasets of a pool JailOps does not manage (a large
	// archive pool), into the jail with nullfs. Only a global admin may set them, the source must lie below a
	// directory the host operator approved in the helper's configuration (admin-host-mount), and a profile cannot
	// carry them.
	HostMounts []HostMount `json:"host_mounts,omitempty"`
	Boot       BootPolicy  `json:"boot,omitempty"`
	// Packages is reserved: installing packages at creation is not implemented and creation with it set is refused.
	Packages []string          `json:"packages,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	// Shell is the path of the shell `jailops shell` starts inside the jail;
	// empty means /bin/sh. It is a management setting and does not touch
	// /etc/passwd.
	Shell string `json:"shell,omitempty"`
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
	// Proxy forwards host ports to the jail through the jail's PF anchor.
	Proxy []ProxyDevice `json:"proxy,omitempty"`
	// Encrypted is reserved: native ZFS encryption of jails is not implemented and creation with it set is refused.
	Encrypted bool `json:"encrypted,omitempty"`
	// Delegate asks for the jail's data datasets to be handed to the jail (jailed=on, zfs
	// allow). It is accepted and rendered into jail.conf (allow.mount.zfs), but the helper does not hand the
	// datasets over yet. Only a global admin may set it, and a project is not a security boundary for a jail that
	// has it.
	Delegate bool `json:"zfs_delegation,omitempty"`
}

// UpdateStrategy selects how a jail follows its image.
type UpdateStrategy string

const (
	// UpdateRebase replaces the OS root from a newer generation and keeps
	// application data (FreeBSD thin jails).
	UpdateRebase UpdateStrategy = "rebase"
	// UpdateInPlace upgrades packages inside the jail (thick and Linux jails).
	UpdateInPlace UpdateStrategy = "inplace"
	// UpdateManual never updates unless asked.
	UpdateManual UpdateStrategy = "manual"
)

// UpdatePolicy controls updates (design 8.4). The zero value is manual:
// nothing happens unless a user runs it.
type UpdatePolicy struct {
	Strategy    UpdateStrategy `json:"strategy,omitempty"`
	Window      string         `json:"window,omitempty"`      // e.g. "sun 03:00"
	Healthcheck string         `json:"healthcheck,omitempty"` // command run in the jail
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

// Resources limits a jail. Enforcement uses jail parameters and rctl.
type Resources struct {
	CPU    int    `json:"cpu,omitempty"`    // cores
	Memory string `json:"memory,omitempty"` // "2G"
	Disk   string `json:"disk,omitempty"`   // ZFS quota on the root and data
	PIDs   int    `json:"pids,omitempty"`
	// DiskReservation is an optional ZFS reservation for the jail's root.
	DiskReservation string `json:"disk_reservation,omitempty"`
}

// NetworkAttach connects a jail to a managed network. Every jail uses VNET;
// shared-IP jails are not supported.
type NetworkAttach struct {
	// Name is the device name ("eth0", "eth1", ... when empty). Devices are
	// identified by name when profiles are merged.
	Name    string `json:"name,omitempty"`
	Network string `json:"network"`
	// MTU overrides the network's MTU for this interface; zero keeps it.
	MTU int `json:"mtu,omitempty"`
	// Ingress and Egress are rate limits in bits per second ("10M", "1G")
	// for traffic into and out of the jail. UNVERIFIED: enforcement needs a
	// shaper (dummynet pipes loaded by the root helper); until that is wired
	// the limit is stored and reported as a condition "RateLimit" with status
	// False, never as enforced.
	Ingress string `json:"ingress,omitempty"`
	Egress  string `json:"egress,omitempty"`
	// IPv4 and IPv6 are "auto" (allocate from the network's range), "none",
	// or a fixed address. The allocation is written back to the spec so it
	// travels with the jail.
	IPv4 string `json:"ipv4,omitempty"`
	IPv6 string `json:"ipv6,omitempty"`
	MAC  string `json:"mac,omitempty"` // derived from the jail UUID when empty
}

// FirewallAttach selects the policy applied to the jail's anchor.
type FirewallAttach struct {
	Policy string   `json:"policy,omitempty"`
	Tags   []string `json:"tags,omitempty"`
}

// VolumeMount is a data dataset mounted into the jail.
type VolumeMount struct {
	// Name is the device name and the name of the volume dataset.
	Name     string `json:"name"`
	Mount    string `json:"mount"`
	Quota    string `json:"quota,omitempty"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

// HostMount is an existing directory of the host mounted into the jail.
type HostMount struct {
	// Source is the absolute path on the host, for example the mountpoint of a dataset of another pool.
	Source string `json:"source"`
	// Target is the absolute path inside the jail.
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

// DevfsDevice exposes one device node of an allow-list (tun, tap, pf, bpf,
// fuse, dri) inside the jail. Exposing devices needs the admin role.
type DevfsDevice struct {
	// Name is the device name used to override it from a profile.
	Name   string `json:"name"`
	Device string `json:"device"`
}

// ProxyDevice forwards a port of the host to a port of the jail.
type ProxyDevice struct {
	Name string `json:"name"`
	// Listen is "host:port" or ":port" ("host" is an address of the host; empty
	// means the uplink address).
	Listen   string `json:"listen"`
	JailPort int    `json:"jail_port"`
	// Proto is "tcp" (default) or "udp".
	Proto string `json:"proto,omitempty"`
}

// BootPolicy orders jails at host start.
type BootPolicy struct {
	Autostart bool     `json:"autostart,omitempty"`
	Priority  int      `json:"priority,omitempty"`
	Depends   []string `json:"depends,omitempty"`
}

// PowerState is the observed run state of a jail.
type PowerState string

const (
	PowerStopped  PowerState = "stopped"
	PowerStarting PowerState = "starting"
	PowerRunning  PowerState = "running"
	PowerStopping PowerState = "stopping"
	PowerError    PowerState = "error"
)

// JailStatus is the observed state of a jail on its host.
type JailStatus struct {
	Power PowerState `json:"power"`
	// JID is the kernel jail id while the jail runs.
	JID int `json:"jid,omitempty"`
	// Generation is the image generation the root currently uses.
	Generation string `json:"generation,omitempty"`
	// Addresses are the addresses actually configured.
	Addresses []string  `json:"addresses,omitempty"`
	StartedAt time.Time `json:"started_at,omitzero"`
	// Drift lists differences between the rendered and the live system.
	Drift      []string    `json:"drift,omitempty"`
	Conditions []Condition `json:"conditions,omitempty"`
	// ProvisionedAt is when the jail's provisioning last completed; zero when
	// it has not run (or the jail has none).
	ProvisionedAt time.Time `json:"provisioned_at,omitzero"`
}

// JailAction is the body of PUT /1.0/jails/{name}/state.
type JailAction struct {
	Action  string `json:"action"` // start, stop, restart
	Timeout int    `json:"timeout,omitempty"`
	Force   bool   `json:"force,omitempty"`
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

// JailRollback is the body of POST /1.0/jails/{name}/rollback.
type JailRollback struct {
	Scope    RollbackScope `json:"scope,omitempty"`
	Snapshot string        `json:"snapshot,omitempty"`
	// ConfirmDataLoss must be true for RollbackData and RollbackAll.
	ConfirmDataLoss bool `json:"confirm_data_loss,omitempty"`
}

// JailUpgrade is the body of POST /1.0/jails/{name}/upgrade.
type JailUpgrade struct {
	// Generation pins the target; empty means the newest generation of the
	// jail's image alias.
	Generation string `json:"generation,omitempty"`
	DryRun     bool   `json:"dry_run,omitempty"`
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
	Written int64 `json:"written,omitempty"`
}
