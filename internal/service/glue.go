package service

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"jailops.net/jailops/internal/imagegen"
	"jailops.net/jailops/internal/reconcile"
	"jailops.net/jailops/internal/sys/jailos"
	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/sdk/api"
)

// confDir is the directory of the rendered files of one jail. key is the
// project-qualified jail key, which is also its OS-level name.
func (s *Service) confDir(key string) string { return filepath.Join(s.d.ConfDir, key) }

// anchorFile is where the rendered PF anchor of a jail is left for the start hook.
func (s *Service) anchorFile(key string) string {
	return filepath.Join(s.d.StateDir, "rendered", "pf", key+".conf")
}
func (s *Service) confPath(key string) string {
	return filepath.Join(s.confDir(key), "jail.conf")
}
func (s *Service) fstabPath(key string) string { return filepath.Join(s.confDir(key), "fstab") }
func (s *Service) jailMount(key string) string { return s.lcfg.ContainerMP(key) }
func (s *Service) rootMount(key string) string { return s.lcfg.RootMP(key) }

// jailRuntimeAdapter implements lifecycle.Jails over jailos.
type jailRuntimeAdapter struct{ s *Service }

func (a *jailRuntimeAdapter) Start(ctx context.Context, name string) error {
	if err := a.s.d.Jails.Create(ctx, a.s.confPath(name), name); err != nil {
		return err
	}
	a.s.markStarted(name)
	return nil
}

func (a *jailRuntimeAdapter) Stop(ctx context.Context, name string) error {
	_, up, err := a.s.d.Jails.Get(ctx, name)
	if err != nil || !up {
		return err
	}
	defer a.s.markStopped(name)
	return a.s.d.Jails.Remove(ctx, a.s.confPath(name), name)
}

func (a *jailRuntimeAdapter) Running(ctx context.Context, name string) (bool, error) {
	_, up, err := a.s.d.Jails.Get(ctx, name)
	return up, err
}

func (a *jailRuntimeAdapter) Exec(ctx context.Context, name string, argv []string) ([]byte, error) {
	res, err := a.s.d.Jails.Exec(ctx, name, jailos.ExecOpts{Argv: argv, Timeout: 5 * time.Minute})
	out := append(res.Stdout, res.Stderr...)
	return out, err
}

// HostRelease reports the running kernel as "15.1-RELEASE" (patch level dropped).
func (a *jailRuntimeAdapter) HostRelease(ctx context.Context) (string, error) {
	r, err := a.s.sysctl(ctx, "kern.osrelease")
	if err != nil {
		return "", err
	}
	if i := strings.Index(r, "-p"); i > 0 {
		r = r[:i]
	}
	return r, nil
}

// imageAdapter implements lifecycle.Images over the image manager.
type imageAdapter struct{ s *Service }

// Resolve and Ensure answer with a generation on the pool of root: one that
// only another pool holds is received there, an image without generations is
// built there.
func (a *imageAdapter) Resolve(ctx context.Context, root, ref string) (api.ImageGeneration, error) {
	return a.Ensure(ctx, root, ref)
}
func (a *imageAdapter) Ensure(ctx context.Context, root, ref string) (api.ImageGeneration, error) {
	m := a.s.imagesOfRoot(root)
	if m == nil {
		return api.ImageGeneration{}, api.Errorf(api.CodeInternal, "no image pool for %s", root)
	}
	return m.Provide(ctx, ref)
}
func (a *imageAdapter) Collect(ctx context.Context) ([]string, error) {
	var out []string
	var errs []error
	for _, m := range a.s.imageManagers() {
		l, err := m.Collect(ctx)
		out = append(out, l...)
		errs = append(errs, err)
	}
	return out, errors.Join(errs...)
}

func (s *Service) imageSpec(ctx context.Context, image string) (api.ImageSpec, error) {
	o, err := s.imgKind.get(image)
	if err != nil {
		return api.ImageSpec{}, err
	}
	return o.Spec, nil
}

// sysctl reads one value through the configured reader or the runner.
func (s *Service) sysctl(ctx context.Context, name string) (string, error) {
	if s.d.Sysctl != nil {
		v, err := s.d.Sysctl(ctx, name)
		return strings.TrimSpace(v), err
	}
	return reconcile.Sysctl(ctx, s.d.Run, name)
}

var _ = imagegen.PropGen
var _ = run.DefaultTimeout
var _ = time.Second
