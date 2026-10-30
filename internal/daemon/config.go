// Package daemon wires jailopsd: configuration, start order, listeners,
// reconciler and graceful shutdown. It holds no business logic.
package daemon

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"jailops.net/jailops/internal/apps"
	"jailops.net/jailops/internal/service"
)

// DefaultConfigPath is where jailopsd looks for its configuration.
const DefaultConfigPath = "/usr/local/etc/jailops/jailopsd.yaml"

// Config is jailopsd.yaml. Unknown keys are an error.
type Config struct {
	// Role is "host" (the only role of this binary). The backup node is a
	// separate daemon.
	Role string `yaml:"role"`
	// Hub turns the daemon into a hub: it keeps a registry of remotes and
	// forwards ?host= requests over pinned mTLS.
	Hub bool `yaml:"hub"`

	Pool string `yaml:"pool"`
	// Dataset is the JailOps dataset; default "<pool>/jailops".
	Dataset string `yaml:"dataset"`
	// MountBase is where the dataset is mounted; default "/jailops".
	MountBase string `yaml:"mount_base"`
	// StateDir defaults to <data_dir>/state, which is where jailops-priv init mounts the state dataset. It must lie
	// outside the storage root: the root is owned by the helper and not writable by the daemon.
	StateDir string `yaml:"state_dir"`
	// DataDir holds the SQLite database, the seal key, TLS material and the
	// operation journal; default /var/db/jailops.
	DataDir string `yaml:"data_dir"`
	// ConfDir is where rendered jail.conf files go (the conf_dir of jailops-priv).
	ConfDir string `yaml:"conf_dir"`
	WorkDir string `yaml:"work_dir"`

	Listen Listen `yaml:"listen"`
	// Web switches the embedded management console. The API does not depend on it.
	Web  Web  `yaml:"web"`
	Priv Priv `yaml:"priv"`
	Log  Log  `yaml:"log"`

	Reconcile Reconcile   `yaml:"reconcile"`
	ZFS       ZFSConf     `yaml:"zfs"`
	Limits    Limits      `yaml:"limits"`
	Projects  Projects    `yaml:"projects"`
	Network   NetworkConf `yaml:"network"`
	Lifecycle Lifecycle   `yaml:"lifecycle"`
	// Backup, when present, enables host-side backups (design 9): the key,
	// capture, the schedule, restore and restore tests, against one backup
	// node. Absent, every backup call answers unsupported.
	Backup *BackupConf `yaml:"backup"`
	// Updates configures the update scheduler.
	Updates UpdatesConf `yaml:"updates"`
	// Apps configures the application catalog (docs/apps.md).
	Apps AppsConf `yaml:"apps"`
}

// AppsConf configures the application catalog: definitions the daemon reads
// from local files, installed by the jailops-apps package.
type AppsConf struct {
	// CatalogDir holds the *.yaml definitions (default
	// /usr/local/share/jailops/apps). A missing directory is an empty catalog.
	CatalogDir string `yaml:"catalog_dir"`
	// LocalDir holds the operator's own definitions (default
	// /usr/local/etc/jailops/apps), read with the catalog; one of the same
	// name as a packaged one wins. A missing directory is empty.
	LocalDir string `yaml:"local_dir"`
}

// UpdatesConf configures managed updates (design 8.4). Updates are off for
// every jail until a user sets its updates.mode.
type UpdatesConf struct {
	// Tick is how often schedules are evaluated (default 1m).
	Tick Duration `yaml:"tick"`
	// ProjectFilesDir holds the provisioning files, one directory per project
	// (default <state_dir>/project-files).
	ProjectFilesDir string `yaml:"project_files_dir"`
}

// BackupConf configures internal/hostbackup.
type BackupConf struct {
	// KeyPath is the host key file (default /var/db/jailops/backup.key).
	KeyPath string `yaml:"key_path"`
	// PassphraseFile, when set, holds the passphrase (first line) that wraps
	// the key file; empty relies on the file mode, which unattended
	// schedules need.
	PassphraseFile string `yaml:"passphrase_file"`
	// Node is the backup node this host pushes to.
	Node BackupNode `yaml:"node"`
	// RecoveryRecipients are age public keys the key is escrowed to.
	RecoveryRecipients []string `yaml:"recovery_recipients"`
	// AllowUnescrowed lets backups run while the key is not escrowed.
	AllowUnescrowed bool `yaml:"allow_unescrowed"`
	// Concurrency bounds the jobs that run at once (default 2).
	Concurrency int `yaml:"concurrency"`
	// ScratchDir and ScratchDataset are where restore tests restore.
	ScratchDir     string `yaml:"scratch_dir"`
	ScratchDataset string `yaml:"scratch_dataset"`
}

// BackupNode names the node and pins its certificate.
type BackupNode struct {
	Name        string `yaml:"name"`
	Address     string `yaml:"address"`     // host:port
	Fingerprint string `yaml:"fingerprint"` // hex SHA-256 of the node certificate
	CertFile    string `yaml:"cert_file"`   // this host's client certificate
	KeyFile     string `yaml:"key_file"`
}

// Lifecycle configures thin jails.
type Lifecycle struct {
	// Persistent replaces the list of paths of a thin jail that survive a
	// generation switch (each in its own dataset). Empty keeps the default:
	// /etc, /usr/local, /var/db (UNVERIFIED: pkg database persistence),
	// /var/log, /var/mail, /var/spool, /home and /root.
	Persistent []PersistentPath `yaml:"persistent"`
}

// PersistentPath is one entry of Lifecycle.Persistent.
type PersistentPath struct {
	Dataset string `yaml:"dataset"`
	Path    string `yaml:"path"`
	Class   string `yaml:"class"` // system or data
	Merge   bool   `yaml:"merge"`
	NoSeed  bool   `yaml:"no_seed"`
}

// NetworkConf configures host networking.
type NetworkConf struct {
	// Sysctl overrides the bridge sysctls the daemon sets at runtime
	// through the helper; only net.link.bridge.pfil_bridge, pfil_member and
	// pfil_onlyip are accepted, each 0 or 1. They are re-asserted by the
	// reconcile loop and never written to /etc/sysctl.conf.
	Sysctl map[string]string `yaml:"sysctl"`
}

// Projects configures project creation. NetworkPool is the pool of subnets
// that `project create --new-network` carves dedicated networks from; without
// it that option answers unsupported.
type Projects struct {
	// NetworkPool is an IPv4 prefix such as "10.64.0.0/12".
	NetworkPool string `yaml:"network_pool"`
	// NetworkBits is the size of each project subnet (24 when zero).
	NetworkBits int `yaml:"network_bits"`
	// Uplink and NAT apply to the dedicated networks.
	Uplink string `yaml:"uplink"`
	NAT    bool   `yaml:"nat"`
}

// Web configures the embedded web console of the host. The console is always in the binary and off until the operator
// turns it on: with enabled false (the default) the daemon serves the API only, every path outside /api/v1 and /healthz
// answers 404, and nothing else changes.
type Web struct {
	// Enabled defaults to false.
	Enabled *bool `yaml:"enabled"`
}

// UI reports whether the embedded console is served.
func (c Config) UI() bool { return c.Web.Enabled != nil && *c.Web.Enabled }

// DefaultHTTPSAddress is the port of JailOps on the network (docs/ports.md).
const DefaultHTTPSAddress = ":39110"

// ApplyWebOverride is what the -web and -no-web flags (and the rc.conf knob jailopsd_web) do to a configuration. -no-web
// switches the console off. -web switches it on, and, when no TLS listener is configured, opens the default one
// (DefaultHTTPSAddress): asking for the console from the command line or from rc.conf means asking to see it in a
// browser. A console switched on only in the file (web.enabled: true) opens no listener by itself.
func ApplyWebOverride(c Config, force, no bool) Config {
	if !force && !no {
		return c
	}
	on := force && !no
	c.Web.Enabled = &on
	if on && c.Listen.HTTPS.Address == "" {
		c.Listen.HTTPS.Address = DefaultHTTPSAddress
	}
	return c
}

// Listen configures the listeners.
type Listen struct {
	Unix  UnixListen  `yaml:"unix"`
	HTTPS HTTPSListen `yaml:"https"`
}

// UnixListen is the local socket. The kernel-reported peer uid decides one thing: uid 0 (root) is a global
// administrator, the host's recovery access. Every other peer is anonymous, exactly like an unauthenticated
// remote client, and authenticates like one (API token, session login). There is no mapping of Unix users or groups
// to roles. Mode 0666 is therefore safe: who may connect is not what decides who may do something.
type UnixListen struct {
	Path string `yaml:"path"`
	Mode uint32 `yaml:"mode"`
}

// HTTPSListen is the network listener: pinned mTLS, API tokens and the web UI
// login. An empty address disables it.
type HTTPSListen struct {
	Address string `yaml:"address"`
	// Cert and Key default to <data_dir>/tls/server.{crt,key}, generated on
	// first start.
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
	// Names are extra DNS names and IPs for a generated certificate.
	Names []string `yaml:"names"`
}

// Priv says how jailopsd reaches the root helper.
type Priv struct {
	Socket string `yaml:"socket"`
}

// Log configures logging to stderr.
type Log struct {
	Level  string `yaml:"level"`  // debug, info, warn, error
	Format string `yaml:"format"` // text or json
}

// Reconcile tunes the loop.
type Reconcile struct {
	Interval  Duration `yaml:"interval"`
	Autostart *bool    `yaml:"autostart"`
}

// ZFSConf holds ZFS compatibility settings.
type ZFSConf struct {
	// CompatFile is the OpenZFS compatibility file name of the target release
	// line, for example "openzfs-2.2-freebsd".
	CompatFile string `yaml:"compat_file"`
	CompatDir  string `yaml:"compat_dir"`
}

// Limits bounds requests.
type Limits struct {
	MaxBodyBytes   int64    `yaml:"max_body_bytes"`
	RequestTimeout Duration `yaml:"request_timeout"`
	// AuthFailureThreshold is the number of failed authentications from one
	// source (bad token, untrusted certificate, forged requestor claim)
	// before that source is locked out (default 5).
	AuthFailureThreshold int `yaml:"auth_failure_threshold"`
	// ExecSessionsPerJail, ExecIdle and ExecMax bound exec sessions
	// (defaults 8, 30m and 12h).
	ExecSessionsPerJail int      `yaml:"exec_sessions_per_jail"`
	ExecIdle            Duration `yaml:"exec_idle"`
	ExecMax             Duration `yaml:"exec_max"`
}

// Duration is a time.Duration that reads "30s" in YAML.
type Duration time.Duration

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q", s)
	}
	*d = Duration(v)
	return nil
}

// Std returns the value as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// Defaults returns the configuration with every default filled in.
func Defaults() Config {
	t := true
	return Config{
		Role: "host", MountBase: "/jailops", DataDir: "/var/db/jailops",
		ConfDir: "/usr/local/etc/jailops/jails", WorkDir: "/var/tmp/jailops",
		Listen:    Listen{Unix: UnixListen{Path: "/var/run/jailops/jailops.sock", Mode: 0o666}},
		Priv:      Priv{Socket: "/var/run/jailops/priv.sock"},
		Log:       Log{Level: "info", Format: "text"},
		Reconcile: Reconcile{Interval: Duration(60 * time.Second), Autostart: &t},
		Limits:    Limits{MaxBodyBytes: 4 << 20, RequestTimeout: Duration(60 * time.Second)},
		Apps:      AppsConf{CatalogDir: apps.DefaultDir, LocalDir: apps.DefaultLocalDir},
	}
}

// Decode reads a configuration, rejecting unknown keys, and fills defaults.
func Decode(r io.Reader) (Config, error) {
	cfg := Defaults()
	raw, err := io.ReadAll(r)
	if err != nil {
		return cfg, fmt.Errorf("config: %w", err)
	}
	if err := rejectRemovedKeys(raw); err != nil {
		return cfg, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && err != io.EOF {
		return cfg, fmt.Errorf("config: %w", err)
	}
	return cfg.finish()
}

// removedUnixKeys are the keys of the old Unix user/group to role mapping.
var removedUnixKeys = []string{"admin_group", "user_group", "user_projects"}

// rejectRemovedKeys fails with an explanation, not a bare "unknown field", for the keys that mapped Unix groups to
// roles: that mapping no longer exists.
func rejectRemovedKeys(raw []byte) error {
	var probe struct {
		Listen struct {
			Unix map[string]any `yaml:"unix"`
		} `yaml:"listen"`
	}
	if err := yaml.Unmarshal(raw, &probe); err != nil {
		return nil // the strict decode reports the real syntax or type error
	}
	for _, k := range removedUnixKeys {
		if _, ok := probe.Listen.Unix[k]; ok {
			return fmt.Errorf("config: listen.unix.%s is no longer supported: Unix users and groups are not mapped to roles any more. "+
				"Remove the key; root on the host is the global administrator, and everyone else signs in with a JailOps user, API token or client certificate "+
				"(create the first administrator with: sudo jailops user create NAME --admin)", k)
		}
	}
	return nil
}

// Load reads the file at path.
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	cfg, err := Decode(bytes.NewReader(b))
	if err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

func (c Config) finish() (Config, error) {
	if c.Role == "" {
		c.Role = "host"
	}
	if c.Role != "host" {
		return c, fmt.Errorf("config: role %q is not supported by jailopsd: the backup node is a separate daemon (jailops-backupd)", c.Role)
	}
	if c.Pool == "" {
		return c, fmt.Errorf("config: pool is required")
	}
	if c.Dataset == "" {
		c.Dataset = c.Pool + "/jailops"
	}
	if c.StateDir == "" {
		c.StateDir = filepath.Join(c.DataDir, "state")
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return c, fmt.Errorf("config: log.level %q must be debug, info, warn or error", c.Log.Level)
	}
	switch c.Log.Format {
	case "text", "json":
	default:
		return c, fmt.Errorf("config: log.format %q must be text or json", c.Log.Format)
	}
	if c.Apps.CatalogDir == "" {
		c.Apps.CatalogDir = apps.DefaultDir
	}
	if c.Apps.LocalDir == "" {
		c.Apps.LocalDir = apps.DefaultLocalDir
	}
	for name, p := range map[string]string{"mount_base": c.MountBase, "state_dir": c.StateDir, "data_dir": c.DataDir, "conf_dir": c.ConfDir,
		"work_dir": c.WorkDir, "listen.unix.path": c.Listen.Unix.Path, "priv.socket": c.Priv.Socket, "apps.catalog_dir": c.Apps.CatalogDir,
		"apps.local_dir": c.Apps.LocalDir} {
		if name == "listen.unix.path" && p == "" {
			continue // the socket is disabled
		}
		if !filepath.IsAbs(p) {
			return c, fmt.Errorf("config: %s must be an absolute path", name)
		}
	}
	if a := c.Listen.HTTPS.Address; a != "" {
		host, port, err := net.SplitHostPort(a)
		if err != nil {
			return c, fmt.Errorf("config: listen.https.address %q must be host:port or :port (for example \":39110\" or \"127.0.0.1:39110\"): %v", a, err)
		}
		if n, err := strconv.Atoi(port); err != nil || n < 0 || n > 65535 {
			return c, fmt.Errorf("config: listen.https.address %q has an invalid port", a)
		}
		if host != "" {
			if _, err := netip.ParseAddr(strings.Trim(host, "[]")); err != nil && !validHostname(host) {
				return c, fmt.Errorf("config: listen.https.address %q: %q is neither an IP address nor a host name", a, host)
			}
		}
	}
	if (c.Listen.HTTPS.Cert == "") != (c.Listen.HTTPS.Key == "") {
		return c, fmt.Errorf("config: listen.https.cert and listen.https.key go together: give both (your certificate) or neither (a generated, self-signed one)")
	}
	for name, p := range map[string]string{"listen.https.cert": c.Listen.HTTPS.Cert, "listen.https.key": c.Listen.HTTPS.Key} {
		if p != "" && !filepath.IsAbs(p) {
			return c, fmt.Errorf("config: %s must be an absolute path", name)
		}
	}
	if c.Projects.NetworkPool != "" {
		p, err := netip.ParsePrefix(c.Projects.NetworkPool)
		if err != nil || !p.Addr().Is4() {
			return c, fmt.Errorf("config: projects.network_pool %q must be an IPv4 prefix such as 10.64.0.0/12", c.Projects.NetworkPool)
		}
		if bits := c.Projects.NetworkBits; bits != 0 && (bits <= p.Bits() || bits > 29) {
			return c, fmt.Errorf("config: projects.network_bits %d does not fit %s", bits, p)
		}
	}
	if v, ok := c.Network.Sysctl[service.SysctlPfilMemberKey]; ok && v != "1" {
		return c, fmt.Errorf("config: network.sysctl %s must be 1: the per-jail pf rules stand on the ports of the bridge", service.SysctlPfilMemberKey)
	}
	for k, v := range c.Network.Sysctl {
		if !slices.Contains(service.NetSysctlKeys, k) {
			return c, fmt.Errorf("config: network.sysctl accepts only %s, not %q", strings.Join(service.NetSysctlKeys, ", "), k)
		}
		if v != "0" && v != "1" {
			return c, fmt.Errorf("config: network.sysctl %s must be 0 or 1, not %q", k, v)
		}
	}
	if b := c.Backup; b != nil {
		if b.Node.Address == "" || b.Node.Fingerprint == "" {
			return c, fmt.Errorf("config: backup.node.address and backup.node.fingerprint are required (the node's certificate is pinned)")
		}
		if b.Node.CertFile == "" || b.Node.KeyFile == "" {
			return c, fmt.Errorf("config: backup.node.cert_file and backup.node.key_file are required (this host's client certificate)")
		}
		for name, p := range map[string]string{"key_path": b.KeyPath, "passphrase_file": b.PassphraseFile, "node.cert_file": b.Node.CertFile,
			"node.key_file": b.Node.KeyFile, "scratch_dir": b.ScratchDir} {
			if p != "" && !filepath.IsAbs(p) {
				return c, fmt.Errorf("config: backup.%s must be an absolute path", name)
			}
		}
		if b.Concurrency < 0 || b.Concurrency > 32 {
			return c, fmt.Errorf("config: backup.concurrency must be between 0 and 32")
		}
	}
	seenDS := map[string]bool{}
	for i, p := range c.Lifecycle.Persistent {
		if p.Dataset == "" || strings.ContainsAny(p.Dataset, "/@ ") || seenDS[p.Dataset] {
			return c, fmt.Errorf("config: lifecycle.persistent[%d].dataset %q must be a unique single dataset name", i, p.Dataset)
		}
		seenDS[p.Dataset] = true
		if !filepath.IsAbs(p.Path) || filepath.Clean(p.Path) != p.Path || p.Path == "/" {
			return c, fmt.Errorf("config: lifecycle.persistent[%d].path %q must be a clean absolute path below /", i, p.Path)
		}
		if p.Class != "system" && p.Class != "data" {
			return c, fmt.Errorf("config: lifecycle.persistent[%d].class must be system or data", i)
		}
	}
	if c.Reconcile.Interval.Std() < time.Second {
		return c, fmt.Errorf("config: reconcile.interval must be at least 1s")
	}
	if c.Reconcile.Autostart == nil {
		t := true
		c.Reconcile.Autostart = &t
	}
	return c, nil
}

var hostnameRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)

func validHostname(h string) bool { return len(h) <= 253 && hostnameRE.MatchString(h) }
