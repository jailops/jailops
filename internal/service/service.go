// Package service is the business logic of jailopsd. It has no HTTP in it: the
// server translates requests into calls on Service and Service answers with
// api types and *api.Error values.
//
// Every state change goes through state.Apply with a proposer-generated id and
// time, every mutating request runs as a persisted operation under per-resource
// locks, and lifecycle events are written to the store and fanned out.
package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"filippo.io/age"
	"fmt"
	"io"
	"log/slog"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"jailops.net/jailops/internal/auth"
	"jailops.net/jailops/internal/imagegen"
	"jailops.net/jailops/internal/lifecycle"
	"jailops.net/jailops/internal/plan"
	"jailops.net/jailops/internal/reconcile"
	"jailops.net/jailops/internal/state"
	"jailops.net/jailops/internal/store"
	"jailops.net/jailops/internal/sys/ifconfig"
	"jailops.net/jailops/internal/sys/jailos"
	"jailops.net/jailops/internal/sys/pfctl"
	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/internal/sys/zfs"
	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

// FS is the file access the lifecycle and the image builder need. The daemon
// supplies the real filesystem, tests an in-memory one.
type FS interface {
	lifecycle.Files
	imagegen.Files
}

// Deps are the collaborators of a Service. Everything that touches the
// operating system is an interface so tests can substitute fakes.
type Deps struct {
	Now      func() time.Time
	Log      *slog.Logger
	Version  string
	Hostname string
	HostUUID string
	Role     api.Role
	Hub      bool
	Env      api.Environment

	// Pool is the ZFS pool name; Root the JailOps dataset ("tank/jailops") and
	// MountBase the directory its datasets are mounted under ("/jailops").
	Pool      string
	Root      string
	MountBase string
	// ConfDir holds rendered per-jail jail.conf and fstab files
	// (<ConfDir>/<jail>/jail.conf); it is the ConfDir of jailops-priv.
	ConfDir string
	// StateDir is the state directory; the rendered PF anchor of a jail is
	// left in <StateDir>/rendered/pf/<jail>.conf for jailops-priv's start hook.
	StateDir string
	// WorkDir is scratch space for image builds.
	WorkDir string
	// JournalDir holds the plan journal and operation requests that let a
	// restart roll interrupted operations back.
	JournalDir string

	State   *state.Store
	DB      *store.Store
	SealKey []byte
	// SecretsIdentity opens values sealed to SecretsRecipient (env values "sealed:age:..."). Nil means the host
	// accepts no sealed values.
	SecretsIdentity  age.Identity
	SecretsRecipient string

	ZFS      lifecycle.ZFS
	ImageZFS imagegen.ZFS
	Exec     lifecycle.Exec
	Files    FS
	Jails    jailos.Jails
	PF       pfctl.Client
	Net      ifconfig.Client
	Run      run.Runner
	// Sysctl reads one sysctl value; when nil the runner is used.
	Sysctl func(ctx context.Context, name string) (string, error)
	// ZClient is optional: it feeds pool usage and the ZFS compatibility check.
	ZClient zfs.Client
	// CompatFile is the OpenZFS compatibility file name for the target release
	// line (for example "openzfs-2.2-freebsd"); CompatDir is where they live.
	CompatFile string
	CompatDir  string

	Lifecycle    lifecycle.Config
	ImageSources map[string]imagegen.Source

	// Remotes dials remotes in hub mode; nil disables probing.
	Remotes RemoteProber
	// Backups is the backup backend. Nil means backups and restores answer
	// api.CodeUnsupported.
	Backups Backups
	// BackupAdmin serves the key and run routes; nil answers unsupported.
	BackupAdmin BackupAdmin

	Lockout        auth.LockoutConfig
	PasswordParams auth.PasswordParams
	// Reconcile is the reconciler (optional); the service kicks it when
	// desired state changes.
	Reconciler *reconcile.Reconciler
	// NetExec executes network steps; built from Net/PF/Run when nil.
	NetExec *reconcile.NetExec

	// Registrar registers storage datasets with the root helper. Nil means
	// the in-process registrar.
	Registrar StorageRegistrar
	// NetworkPool is the pool dedicated project networks are carved from
	// ("10.64.0.0/12"); NetworkPoolBits is the size of each subnet (24 when
	// zero). Empty NetworkPool makes new_network unsupported.
	NetworkPool     string
	NetworkPoolBits int
	// NetworkUplink and NetworkNAT configure the dedicated networks.
	NetworkUplink string
	NetworkNAT    bool
	// Dial opens an SDK client to a remote host (hub mode: fleet projects).
	Dial func(ctx context.Context, r api.Remote) (*sdk.Client, error)
	// Execer runs commands in jails (the root helper in production).
	Execer JailExecer
	// ExecLimits bound exec sessions.
	ExecLimits ExecLimits
	// Logs reads the log files of jail roots (the root helper in production);
	// nil answers unsupported.
	Logs LogReader
	// ProjectFilesDir holds the provisioning files of each project, one
	// directory per project (default <StateDir>/project-files).
	ProjectFilesDir string
	// UpdateTick is how often the update schedules are evaluated (default 1m).
	UpdateTick time.Duration
	// AppsDir is the application catalog directory (default apps.DefaultDir);
	// a missing directory is an empty catalog.
	AppsDir string
	// AppsLocalDir holds the operator's own definitions, read with the
	// catalog (default apps.DefaultLocalDir); a local definition wins over a
	// packaged one of the same name.
	AppsLocalDir string
	// DialJail opens a connection from the host to a jail (the tcp and http
	// health checks of an app installation); nil uses the network.
	DialJail func(ctx context.Context, network, address string) (net.Conn, error)
	// NetSysctl overrides NetSysctlDefaults (network.sysctl of jailopsd.yaml).
	NetSysctl map[string]string
	// Persistent replaces lifecycle.DefaultPersistent (nil keeps it).
	Persistent []lifecycle.PersistentPath
	// ServerFingerprint is the pin of the server certificate; trust tokens
	// carry it.
	ServerFingerprint string
}

// Service implements the business logic.
type Service struct {
	d   Deps
	log *slog.Logger
	now func() time.Time

	locks   *lockSet
	events  *eventBus
	ops     *opManager
	life    *lifecycle.Service
	journal *fileJournal
	nx      *reconcile.NetExec
	login   *auth.Lockout
	// totpMu makes "read the last accepted step, validate, record the new one" one step: two logins presenting
	// the same code at once must not both succeed.
	totpMu sync.Mutex
	// trustLim rate limits failed redemptions of trust tokens per source.
	trustLim *auth.Lockout

	planExec *plan.Executor
	lcfg     lifecycle.Config
	localReg *LocalRegistrar

	imgKind  kindStore[api.ImageSpec]
	netKind  kindStore[api.NetworkSpec]
	polKind  kindStore[api.FirewallPolicySpec]
	setKind  kindStore[api.AddressSetSpec]
	tgtKind  kindStore[api.BackupTargetSpec]
	bpolKind kindStore[api.BackupPolicySpec]
	remKind  kindStore[api.RemoteSpec]
	certKind kindStore[api.CertificateSpec]
	projKind kindStore[api.ProjectSpec]
	stoKind  kindStore[api.StorageSpec]
	profKind kindStore[api.ProfileSpec]

	// storages is the registry of storage (dataset roots and their image
	// pools); placed maps the key of a jail that is being created to its
	// storage.
	stMu     sync.RWMutex
	storages map[string]*storageInfo
	placed   map[string]string
	// pending are the jails being created: their share of the project
	// quotas is reserved from admission until they are in the registry.
	quotaMu sync.Mutex
	pending map[string]pendingJail

	fleet fleetState

	execMu sync.Mutex
	execN  map[string]int
	// installing are the jails an app installation holds: no exec session
	// may enter them while pkg works on their root from the host.
	installing map[string]bool
	logAud     logAudit

	upd     updateState
	prov    provMarks
	histMem histMemory

	mu sync.RWMutex
	// linuxBuilds counts Linux image builds in progress; the host keeps the Linux kernel state while one runs.
	linuxBuilds atomic.Int32
	jails       map[string]*jailEntry
	status      map[string]jailRuntime
	recovered   []RecoveryOutcome
	netStat     map[string]api.NetworkStatus

	certMu    sync.Mutex
	certCache map[string]certInfo
	certGen   uint64

	closeOnce sync.Once
	baseCtx   context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

type jailEntry struct {
	jail     api.Jail
	problems []string
}

// jailRuntime is the observed, in-memory part of a jail's status.
type jailRuntime struct {
	conditions []api.Condition
	drift      []string
	startedAt  time.Time
}

// New builds a Service. Call Start afterwards to load the registry and to roll
// interrupted operations back.
func New(d Deps) (*Service, error) {
	if d.State == nil || d.DB == nil {
		return nil, errors.New("service: state and store are required")
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	// jailopsd is the per-host daemon: the host role only. The backup node is
	// a separate binary.
	d.Role = api.RoleHost
	if d.HostUUID == "" {
		d.HostUUID = state.NewUUID()
	}
	if d.PasswordParams == (auth.PasswordParams{}) {
		d.PasswordParams = auth.DefaultPasswordParams
	}
	if d.Env.OS == "" {
		d.Env.OS = "FreeBSD"
	}
	if d.Env.Pool == "" {
		d.Env.Pool = d.Pool
	}
	s := &Service{d: d, log: d.Log, now: d.Now,
		jails: map[string]*jailEntry{}, status: map[string]jailRuntime{}, netStat: map[string]api.NetworkStatus{},
		login: auth.NewLockout(withClock(d.Lockout, d.Now)), trustLim: auth.NewLockout(withClock(d.Lockout, d.Now)), storages: map[string]*storageInfo{}, placed: map[string]string{},
		pending: map[string]pendingJail{}, execN: map[string]int{}, installing: map[string]bool{}, localReg: NewLocalRegistrar()}
	s.baseCtx, s.cancel = context.WithCancel(context.Background())
	s.imgKind = newKindStore[api.ImageSpec](s, api.KindImage, api.ScopeFleet, "image", "/api/v1/images")
	s.netKind = newKindStore[api.NetworkSpec](s, api.KindNetwork, api.ScopeFleet, "network", "/api/v1/networks")
	s.polKind = newProjectKindStore[api.FirewallPolicySpec](s, api.KindFirewallPolicy, api.ScopeFleet, "firewall-policy", "/api/v1/firewall/policies")
	s.setKind = newProjectKindStore[api.AddressSetSpec](s, api.KindAddressSet, api.ScopeFleet, "address-set", "/api/v1/firewall/address-sets")
	s.tgtKind = newKindStore[api.BackupTargetSpec](s, api.KindBackupTarget, api.ScopeFleet, "backup-target", "/api/v1/backup-targets")
	s.bpolKind = newProjectKindStore[api.BackupPolicySpec](s, api.KindBackupPolicy, api.ScopeFleet, "backup-policy", "/api/v1/backup-policies")
	s.remKind = newKindStore[api.RemoteSpec](s, api.KindRemote, api.ScopeFleet, "remote", "/api/v1/remotes")
	s.certKind = newKindStore[api.CertificateSpec](s, api.KindCertificate, api.ScopeFleet, "certificate", "/api/v1/certificates")
	s.projKind = newKindStore[api.ProjectSpec](s, api.KindProject, api.ScopeHost, "project", "/api/v1/projects")
	s.stoKind = newKindStore[api.StorageSpec](s, api.KindStorage, api.ScopeHost, "storage", "/api/v1/storage")
	s.profKind = newProjectKindStore[api.ProfileSpec](s, api.KindProfile, api.ScopeFleet, "profile", "/api/v1/profiles")
	s.locks = newLockSet()
	s.events = newEventBus(d.DB, d.HostUUID)
	jd, err := newFileJournal(d.JournalDir)
	if err != nil {
		return nil, err
	}
	s.journal = jd
	s.ops = newOpManager(s)
	s.nx = d.NetExec
	if s.nx == nil {
		s.nx = &reconcile.NetExec{Net: d.Net, PF: d.PF, Run: d.Run, Sysctl: d.Sysctl}
	}
	s.planExec = &plan.Executor{Journal: s.journal, OnEvent: s.ops.planEvent}
	s.lcfg = lifecycle.Config{Root: d.Root, MountBase: d.MountBase, Locate: s.locate, Storages: s.lifecycleStorages, Persistent: d.Persistent}
	if d.ZFS != nil {
		lc := d.Lifecycle
		lc.Root, lc.MountBase = d.Root, d.MountBase
		lc.Locate, lc.Storages = s.locate, s.lifecycleStorages
		if lc.Now == nil {
			lc.Now = d.Now
		}
		s.lcfg = lc
		s.life = &lifecycle.Service{Cfg: lc, ZFS: d.ZFS, Exec: d.Exec, Jails: &jailRuntimeAdapter{s: s}, Files: d.Files,
			Hooks: &hooks{s: s}, Images: &imageAdapter{s: s}, Executor: s.planExec}
	}
	if d.Root != "" {
		s.addStorage(DefaultStorage, api.StorageSpec{Dataset: d.Root}, d.Root, d.MountBase)
	}
	return s, nil
}

func withClock(c auth.LockoutConfig, now func() time.Time) auth.LockoutConfig {
	if c.Now == nil {
		c.Now = now
	}
	return c
}

// execAdapter lets the lifecycle.Exec value serve as imagegen.Exec.
type execAdapter struct{ lifecycle.Exec }

// Start loads the jail registry from the pool and rolls interrupted
// operations back. It must be called once before the service is used.
func (s *Service) Start(ctx context.Context) error {
	if err := s.loadStorages(ctx); err != nil {
		return fmt.Errorf("service: storage: %w", err)
	}
	if err := s.RefreshJails(ctx); err != nil {
		s.log.Warn("loading the jail registry failed", "err", err)
	}
	if err := s.recoverInterrupted(ctx); err != nil {
		s.log.Error("recovering interrupted operations failed", "err", err)
	}
	s.warmCerts()
	if s.life != nil {
		s.wg.Add(1)
		go func() { defer s.wg.Done(); s.updateLoop(s.baseCtx) }()
	}
	return nil
}

// Close stops background work and cancels running operations.
func (s *Service) Close() {
	s.closeOnce.Do(func() {
		s.cancel()
		s.events.close()
	})
	s.wg.Wait()
}

// Events exposes the event bus to the server.
func (s *Service) Events() *EventBus { return &EventBus{s.events} }

// HostUUID returns the host identity.
func (s *Service) HostUUID() string { return s.d.HostUUID }

// Hub reports whether this daemon runs in hub mode.
func (s *Service) Hub() bool { return s.d.Hub }

// Clock returns the service clock.
func (s *Service) Clock() time.Time { return s.now() }

// DB returns the operational store (the server needs it for sessions).
func (s *Service) DB() *store.Store { return s.d.DB }

// ---- helpers ----

func newOpID() string { return state.NewUUID() }

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// internal wraps an unexpected error as api.CodeInternal, keeping *api.Error.
func internal(err error) error {
	if err == nil {
		return nil
	}
	var ae *api.Error
	if errors.As(err, &ae) {
		return ae
	}
	return api.Errorf(api.CodeInternal, "%v", err)
}

func notFound(what, name string) *api.Error {
	return api.Errorf(api.CodeNotFound, "%s %q not found", what, name)
}

func invalid(format string, args ...any) *api.Error {
	return api.Errorf(api.CodeInvalid, format, args...)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// Extensions returns the capability list this daemon advertises: the SDK list
// without the backup transport names while no backup backend is wired.
func (s *Service) Extensions() []string {
	var out []string
	for _, e := range api.Extensions {
		switch e {
		case "backup_pull", "backup_push", "restore_preflight":
			if s.d.Backups == nil {
				continue
			}
		case "sealed_secrets":
			if s.d.SecretsIdentity == nil {
				continue
			}
		case "backup_key", "backup_run", "backup_verify":
			if s.d.BackupAdmin == nil {
				continue
			}
		}
		out = append(out, e)
	}
	return out
}

// Info answers GET /api/v1. auth is "trusted" or "untrusted".
func (s *Service) Info(auth string) api.ServerInfo {
	return api.ServerInfo{
		APIVersion: api.Version, APIExtensions: s.Extensions(), Version: s.d.Version,
		HostUUID: s.d.HostUUID, Hostname: s.d.Hostname, Role: s.d.Role, Hub: s.d.Hub,
		Auth: auth, Environment: s.d.Env, SecretsRecipient: s.d.SecretsRecipient,
	}
}

func (s *Service) requireHost(what string) error {
	if s.life == nil {
		return api.Errorf(api.CodeUnsupported, "%s is not available on this daemon", what).
			WithHint("this daemon runs without the host role")
	}
	return nil
}

func errorf(code api.Code, format string, a ...any) error { return api.Errorf(code, format, a...) }

var _ = fmt.Sprintf

func asAPI(err error, target **api.Error) bool { return errors.As(err, target) }

func state_uuid() string { return state.NewUUID() }

// SetReconciler hands the service the reconciler so changes kick it and
// doctor reports its last pass.
func (s *Service) SetReconciler(r *reconcile.Reconciler) { s.d.Reconciler = r }
