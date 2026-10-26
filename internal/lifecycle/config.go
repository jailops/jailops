package lifecycle

import (
	"context"
	"path"
	"time"
)

// Class says how a persistent dataset is treated by rollback scopes.
type Class string

const (
	// ClassSystem datasets hold configuration that belongs to the OS root
	// generation (/etc). Only the "all" rollback scope rolls them back.
	ClassSystem Class = "system"
	// ClassData datasets hold application data. The "data" and "all" scopes
	// roll them back.
	ClassData Class = "data"
)

// PersistentPath is one path of a thin jail that survives an upgrade because it
// lives in its own dataset mounted under the root.
type PersistentPath struct {
	// Dataset is the child name below the jail's container dataset.
	Dataset string
	// Path is the mount path inside the jail.
	Path  string
	Class Class
	// Merge marks datasets the merge tool writes to; they are rolled back to
	// a pre-merge snapshot when an upgrade fails after the merge.
	Merge bool
	// NoSeed leaves the dataset empty at creation instead of copying the
	// image's content of Path into it.
	NoSeed bool
}

// DefaultPersistent is the persistent path list of design section 4.2. It is
// configuration (lifecycle.persistent in jailopsd.yaml) and this is only the
// default. Prototype 2 verified /etc, /usr/local, /home and /root survive a
// generation switch. UNVERIFIED: pkg database persistence, that is /var/db
// (pkg keeps its database in /var/db/pkg, so a switch that keeps /var/db also
// keeps the package records of a generation the root no longer has).
func DefaultPersistent() []PersistentPath {
	return []PersistentPath{
		{Dataset: "etc", Path: "/etc", Class: ClassSystem, Merge: true},
		{Dataset: "local", Path: "/usr/local", Class: ClassData},
		{Dataset: "var-db", Path: "/var/db", Class: ClassData, Merge: true},
		{Dataset: "var-log", Path: "/var/log", Class: ClassData},
		{Dataset: "var-mail", Path: "/var/mail", Class: ClassData},
		{Dataset: "var-spool", Path: "/var/spool", Class: ClassData},
		{Dataset: "home", Path: "/home", Class: ClassData},
		{Dataset: "rootdir", Path: "/root", Class: ClassData},
	}
}

// Config configures the lifecycle.
type Config struct {
	// Root is the JailOps dataset, for example "tank/jailops".
	Root string
	// MountBase is the directory under which jails are mounted.
	MountBase  string
	Persistent []PersistentPath
	Merge      MergeConfig
	// RootOldRetention is how long root.old is kept after an upgrade.
	RootOldRetention time.Duration
	// ReadyTimeout and PollInterval govern the readiness check after start.
	ReadyTimeout time.Duration
	PollInterval time.Duration
	Now          func() time.Time
	Sleep        func(ctx context.Context, d time.Duration) error
	// Replicating reports whether replication of the jail is running.
	Replicating func(ctx context.Context, jail string) (bool, error)
	// Locate says which storage a jail key lives on (the service knows it
	// from the jail's spec). Nil, or an unknown key, means the storage
	// described by Root and MountBase.
	Locate func(key string) (Storage, bool)
	// Storages lists every storage to search when jails are discovered. Nil
	// means the one storage described by Root and MountBase.
	Storages func() []Storage
}

func (c Config) persistent() []PersistentPath {
	if c.Persistent == nil {
		return DefaultPersistent()
	}
	return c.Persistent
}

func (c Config) merge() MergeConfig { return c.Merge.names() }

func (c Config) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func (c Config) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ---- paths ----

// Storage is where the datasets of a jail live: a ZFS dataset root and the
// directory it is mounted under. A host has one Storage per configured
// storage; the layout below a root is
// <root>/projects/<project>/jails/<name>.
type Storage struct {
	Name      string
	Root      string
	MountBase string
}

// at returns the paths of one jail. key is the project-qualified jail key
// (<project>_<name>); its storage comes from Locate, and without Locate
// or for an unknown key from Root and MountBase.
func (c Config) at(key string) jpaths {
	st := Storage{Root: c.Root, MountBase: c.MountBase}
	if c.Locate != nil {
		if s, ok := c.Locate(key); ok {
			st = s
		}
	}
	project, name := splitKey(key)
	return jpaths{st: st, project: project, name: name}
}

// StorageOf returns the storage a jail key lives on.
func (c Config) StorageOf(key string) Storage { return c.at(key).st }

// ContainerDS is the container dataset of a jail.
func (c Config) ContainerDS(key string) string { return c.at(key).containerDS() }

// RootDS is the root dataset of a jail.
func (c Config) RootDS(key string) string { return c.at(key).rootDS() }

// ContainerMP is where the container dataset of a jail is mounted.
func (c Config) ContainerMP(key string) string { return c.at(key).containerMP() }

// RootMP is where the root of a jail is mounted.
func (c Config) RootMP(key string) string { return c.at(key).rootMP() }

// ProjectDS is the dataset of a project on a storage.
func ProjectDS(st Storage, project string) string { return path.Join(st.Root, "projects", project) }

// ProjectJailsDS is the parent of the container datasets of a project.
func ProjectJailsDS(st Storage, project string) string {
	return path.Join(st.Root, "projects", project, "jails")
}

type jpaths struct {
	st            Storage
	project, name string
}

func (p jpaths) jailsDS() string          { return ProjectJailsDS(p.st, p.project) }
func (p jpaths) containerDS() string      { return path.Join(p.jailsDS(), p.name) }
func (p jpaths) rootDS() string           { return p.containerDS() + "/root" }
func (p jpaths) rootNewDS() string        { return p.containerDS() + "/root.new" }
func (p jpaths) rootOldDS() string        { return p.containerDS() + "/root.old" }
func (p jpaths) rootStaleDS() string      { return p.containerDS() + "/root.stale" }
func (p jpaths) volumesDS() string        { return p.containerDS() + "/data" }
func (p jpaths) volumeDS(v string) string { return p.containerDS() + "/data/" + v }
func (p jpaths) containerMP() string {
	return path.Join(p.st.MountBase, "projects", p.project, "jails", p.name)
}
func (p jpaths) rootMP() string     { return p.containerMP() + "/root" }
func (p jpaths) rootNewMP() string  { return p.containerMP() + "/root.new" }
func (p jpaths) rootOldMP() string  { return p.containerMP() + "/root.old" }
func (p jpaths) specPath() string   { return p.containerMP() + "/spec.json" }
func (p jpaths) bundlePath() string { return p.containerMP() + "/bundle.json" }
