package hostbackup

import (
	"context"
	"crypto/tls"
	"log/slog"
	"os"
	"strings"
	"time"

	"jailops.net/jailops/internal/repo/keys"
	"jailops.net/jailops/sdk/api"
)

// Defaults.
const (
	DefaultKeyPath     = "/var/db/jailops/backup.key"
	SnapshotPrefix     = "jailops-backup-"
	DefaultConcurrency = 2
	DefaultTick        = 30 * time.Second
)

// NodeConfig is the backup node this host pushes to.
type NodeConfig struct {
	// Name is the BackupTarget name this node serves. Empty serves every
	// target of kind "node" that names no address of its own.
	Name string
	// Address is host:port of the node's host listener; Fingerprint pins its
	// certificate (hex SHA-256).
	Address     string
	Fingerprint string
	// CertFile and KeyFile are the host's client certificate (PEM).
	CertFile string
	KeyFile  string
	// TLS replaces the pinned configuration (tests).
	TLS *tls.Config
}

// RetryConfig is the retry policy of a failed job.
type RetryConfig struct {
	// Attempts is the number of tries per scheduled run (default 3).
	Attempts int
	// Base and Max bound the exponential wait between tries (default 1 minute, 30 minutes).
	Base, Max time.Duration
}

// Config configures a Service.
type Config struct {
	// KeyPath is the key file (default DefaultKeyPath). It is mode 0600.
	KeyPath string
	// Passphrase, or the first line of PassphraseFile, unwraps a key file
	// protected with Argon2id and wraps a new one. Empty means an unprotected
	// file that relies on its permissions (needed for unattended schedules).
	Passphrase     string
	PassphraseFile string
	// Argon2 overrides the Argon2id cost when a key file is created.
	Argon2 keys.Argon2Params
	// RecoveryRecipients are age public keys (age1...) of the offline
	// recovery recipients the key is escrowed to.
	RecoveryRecipients []string
	// AllowUnescrowed lets a backup run while the key is not escrowed. By
	// default a first backup is refused with api.CodeKeyNotEscrowed.
	AllowUnescrowed bool

	// HostUUID is the UUID of this host: with the jail UUID it forms the group
	// of a jail's backups.
	HostUUID string
	Node     NodeConfig

	// Concurrency bounds the jobs that run at once (default 2). A jail never
	// has more than one.
	Concurrency int
	// EngineWorkers is the number of chunks encoded and uploaded at once per
	// backup (0 = the engine default).
	EngineWorkers int
	// NoTrustCtime tells change detection not to compare ctime and inode
	// numbers. UNVERIFIED: whether both are stable across ZFS snapshots on
	// FreeBSD decides how far the comparison can be trusted (design 9.4).
	NoTrustCtime bool
	// JitterWindow spreads the schedule of a policy over a window, per jail
	// and deterministically (default 10 minutes; negative disables).
	JitterWindow time.Duration
	Retry        RetryConfig
	// Tick is how often the scheduler looks for due jobs (default 30 s).
	Tick time.Duration
	// MaintenanceDelay is the wait after the node answered api.CodeMaintenance
	// (default 15 minutes). It is not a failure and uses no attempt.
	MaintenanceDelay time.Duration
	// StaleSnapshotAge is the age after which a leftover jailops-backup-*
	// snapshot is destroyed by CleanupStale (default 6 hours).
	StaleSnapshotAge time.Duration

	// ScratchDir is where extract tests restore (default /var/tmp/jailops-drill).
	ScratchDir string
	// ScratchDataset is the dataset under which full tests restore; empty
	// refuses full tests.
	ScratchDataset string
	// ScratchMountBase is the directory scratch datasets are mounted under
	// (default /var/tmp/jailops-drill-mnt).
	ScratchMountBase string

	// NoChown restores without changing owners. Default: only when not root.
	NoChown *bool

	Location *time.Location
	Clock    func() time.Time
	// HealthTimeout is how long a full restore test waits for the restored jail to pass its health check
	// (default 60s).
	HealthTimeout time.Duration
	// Sleep waits d or until ctx ends (tests replace it).
	Sleep func(ctx context.Context, d time.Duration) error
	Log   *slog.Logger
}

func (c Config) withDefaults() Config {
	if c.KeyPath == "" {
		c.KeyPath = DefaultKeyPath
	}
	if c.Concurrency <= 0 {
		c.Concurrency = DefaultConcurrency
	}
	if c.JitterWindow == 0 {
		c.JitterWindow = 10 * time.Minute
	}
	if c.Retry.Attempts <= 0 {
		c.Retry.Attempts = 3
	}
	if c.Retry.Base <= 0 {
		c.Retry.Base = time.Minute
	}
	if c.Retry.Max <= 0 {
		c.Retry.Max = 30 * time.Minute
	}
	if c.Tick <= 0 {
		c.Tick = DefaultTick
	}
	if c.MaintenanceDelay <= 0 {
		c.MaintenanceDelay = 15 * time.Minute
	}
	if c.StaleSnapshotAge <= 0 {
		c.StaleSnapshotAge = 6 * time.Hour
	}
	if c.ScratchDir == "" {
		c.ScratchDir = "/var/tmp/jailops-drill"
	}
	if c.ScratchMountBase == "" {
		c.ScratchMountBase = "/var/tmp/jailops-drill-mnt"
	}
	if c.Location == nil {
		c.Location = time.UTC
	}
	if c.Clock == nil {
		c.Clock = time.Now
	}
	if c.Sleep == nil {
		c.Sleep = sleepCtx
	}
	if c.Log == nil {
		c.Log = slog.New(slog.DiscardHandler)
	}
	return c
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c Config) noChown() bool {
	if c.NoChown != nil {
		return *c.NoChown
	}
	return os.Geteuid() != 0
}

// passphrase resolves the configured passphrase.
func (c Config) passphrase() (string, error) {
	if c.PassphraseFile != "" {
		b, err := os.ReadFile(c.PassphraseFile)
		if err != nil {
			return "", api.Errorf(api.CodeInternal, "reading the key passphrase file: %v", err)
		}
		line, _, _ := strings.Cut(string(b), "\n")
		return strings.TrimRight(line, "\r"), nil
	}
	return c.Passphrase, nil
}

// Validate checks the static configuration.
func (c Config) Validate() error {
	if c.HostUUID == "" {
		return api.Errorf(api.CodeInvalid, "hostbackup: Config.HostUUID is required")
	}
	if c.Node.Address != "" && c.Node.TLS == nil && c.Node.Fingerprint == "" {
		return api.Errorf(api.CodeInvalid, "hostbackup: the node needs a pinned certificate fingerprint")
	}
	return nil
}
