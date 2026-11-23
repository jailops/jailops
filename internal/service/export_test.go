package service

import (
	"context"
	"time"

	"jailops.net/jailops/internal/state"
	"jailops.net/jailops/internal/validate"
	"jailops.net/jailops/sdk/api"
)

// SetRenameCrash makes the next renames stop at step n as if the daemon died there (0 never).
func SetRenameCrash(s *Service, n int) { s.renameCrashAt = n }

// SetPolicyRaw stores the spec of a firewall policy as it is, without the validation of the API: what a policy that was
// stored without its defaults looks like.
func SetPolicyRaw(s *Service, project, name string, spec api.FirewallPolicySpec) error {
	_, err := s.polKind.p(project).update(context.Background(), name, AnyGeneration, nil, nil, spec)
	return err
}

// RenderJail renders the anchor of a jail the way the reconcile loop and the start hooks do.
func RenderJail(s *Service, project, name string) error {
	key, err := validate.QualifiedName(project, name)
	if err != nil {
		return err
	}
	j, err := s.jailSnapshot(key)
	if err != nil {
		return err
	}
	refs, err := s.allRefs(project)
	if err != nil {
		return err
	}
	_, err = s.buildJail(context.Background(), j, refs)
	return err
}

// SetManagedPolicy stores a firewall policy that something else manages, as app access does for app-JAIL.
func SetManagedPolicy(s *Service, project, name, managedBy string, spec api.FirewallPolicySpec) error {
	_, err := s.polKind.p(project).create(context.Background(), api.ObjectMeta{Name: name, ManagedBy: managedBy}, spec)
	return err
}

// SeedJailopsRepo runs the copy of the JailOps repository's configuration into a jail root, from the host directory dir.
func SeedJailopsRepo(s *Service, ctx context.Context, dir, root string) error {
	old := hostPkgDir
	hostPkgDir = dir
	defer func() { hostPkgDir = old }()
	return s.seedJailopsRepo(ctx, root)
}

// SetShellSetupDeadline shortens the deadline of the setup after a start; the returned function puts it back.
func SetShellSetupDeadline(d time.Duration) func() {
	old := shellSetupDeadline
	shellSetupDeadline = d
	return func() { shellSetupDeadline = old }
}

// SetHostDirTimeouts shortens how long a read and a probe wait for a host directory that does not answer, and returns
// what puts them back.
func SetHostDirTimeouts(read, probe time.Duration) func() {
	r, p := hostDirReadTimeout.Load(), hostDirProbeTimeout.Load()
	hostDirReadTimeout.Store(int64(read))
	hostDirProbeTimeout.Store(int64(probe))
	return func() { hostDirReadTimeout.Store(r); hostDirProbeTimeout.Store(p) }
}

// HoldForTest holds a jail the way an app installation does and returns what lets it go.
func HoldForTest(s *Service, key, what string) (func(), error) {
	return s.holdJailFor(key, what, false)
}

// SetShellSettle shortens how long a stop waits for the setup of the shell and how long pkg has to end after SIGTERM;
// the returned function puts them back.
func SetShellSettle(wait, grace time.Duration) func() {
	w, g := shellSettleWait, shellTermGrace
	shellSettleWait, shellTermGrace = wait, grace
	return func() { shellSettleWait, shellTermGrace = w, g }
}

// ApplyForwards is applyForwards: the forwards a load took out of policies and profiles, given to the jails that used them.
func (s *Service) ApplyForwards(ctx context.Context, fws []state.MigratedForward) error {
	return s.applyForwards(ctx, fws)
}

// ReportImageAliases is reportImageAliases, and ImageDataset the dataset of an image on the default pool.
func (s *Service) ReportImageAliases(ctx context.Context, refs []state.MigratedImageRef) {
	s.reportImageAliases(ctx, refs)
}

func (s *Service) ImageDataset(image string) string {
	return s.imageManagers()[0].Layout.ImageDS(image)
}

// SetTopCallTimeout shortens the time one reading of `jailops top` may take and returns what it was.
func SetTopCallTimeout(d time.Duration) time.Duration {
	old := topCallTimeout
	topCallTimeout = d
	return old
}

// LockJailForTest takes the lock of a jail the way an operation does and returns what lets it go.
func LockJailForTest(s *Service, key string) (func(), error) {
	return s.locks.tryLock("test operation", jailLock(key))
}

// ThickPorts is the ports part an in-place upgrade of the jail would plan: the pending packages, whether the jail has
// no ports packages at all, and the warning.
func ThickPorts(s *Service, ctx context.Context, key string, dryRun bool) (pending []string, none bool, warn string) {
	j, err := s.getByKey(ctx, key)
	if err != nil {
		return nil, false, err.Error()
	}
	p, w := s.thickPorts(ctx, key, j, dryRun)
	if p == nil {
		return nil, false, w
	}
	return p.Pending, p.None, w
}
