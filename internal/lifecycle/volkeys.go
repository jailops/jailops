package lifecycle

import (
	"context"
	"errors"
	"path"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// Encrypted volumes: a volume declared encrypted is created as its own ZFS encryption root with the passphrase a
// person typed (aes-256-gcm, keyformat=passphrase, keylocation=prompt). Every encrypted volume of a jail has the same
// passphrase. After a host boot their keys are not loaded: the jail is locked, it does not start, and an unlock loads
// the keys. Lock stops the jail, unmounts the volumes and unloads the keys.

// volumeName is the name of a volume dataset (its last component).
func volumeName(ds string) string { return path.Base(ds) }

func encryptedVolumes(st *jailState) []dsInfo {
	var out []dsInfo
	for _, v := range st.Volumes {
		if v.Encrypted {
			out = append(out, v)
		}
	}
	return out
}

// lockedVolumes returns the encrypted volumes whose key is not loaded.
func (s *Service) lockedVolumes(ctx context.Context, st *jailState) ([]dsInfo, error) {
	var out []dsInfo
	for _, v := range encryptedVolumes(st) {
		if s.Keys == nil {
			out = append(out, v)
			continue
		}
		ks, err := s.Keys.KeyStatus(ctx, v.DS)
		if err != nil {
			return nil, err
		}
		if ks != "available" {
			out = append(out, v)
		}
	}
	return out, nil
}

func names(l []dsInfo) []string {
	out := make([]string, len(l))
	for i, v := range l {
		out[i] = volumeName(v.DS)
	}
	return out
}

// LockedError is the error of starting a jail whose encrypted volumes wait for a passphrase.
func LockedError(key string, volumes []string) error {
	_, plain := splitKey(key)
	return api.Errorf(api.CodeLocked, "jail %q is locked: the encrypted volume(s) %s wait for a passphrase", plain, strings.Join(volumes, ", ")).
		WithHint("jailops jail unlock "+plain).WithDetail("volumes", strings.Join(volumes, ","))
}

func (s *Service) refuseLocked(ctx context.Context, st *jailState) error {
	l, err := s.lockedVolumes(ctx, st)
	if err != nil {
		return err
	}
	if len(l) > 0 {
		return LockedError(st.Name, names(l))
	}
	return nil
}

// Locked returns the names of the encrypted volumes of a jail whose key is not loaded (nil when none).
func (s *Service) Locked(ctx context.Context, name string) ([]string, error) {
	st, err := s.observe(ctx, name)
	if err != nil {
		return nil, err
	}
	l, err := s.lockedVolumes(ctx, st)
	if err != nil || len(l) == 0 {
		return nil, err
	}
	return names(l), nil
}

// EncryptedVolumes returns the names of the encrypted volumes of a jail.
func (s *Service) EncryptedVolumes(ctx context.Context, name string) ([]string, error) {
	st, err := s.observe(ctx, name)
	if err != nil {
		return nil, err
	}
	return names(encryptedVolumes(st)), nil
}

func (s *Service) keyedState(ctx context.Context, name string) (*jailState, []dsInfo, error) {
	if s.Keys == nil {
		return nil, nil, api.Errorf(api.CodeUnsupported, "this host has no key operations for encrypted volumes")
	}
	st, err := s.observe(ctx, name)
	if err != nil {
		return nil, nil, err
	}
	enc := encryptedVolumes(st)
	if len(enc) == 0 {
		_, plain := splitKey(name)
		return nil, nil, api.Errorf(api.CodeInvalid, "jail %q has no encrypted volume", plain)
	}
	return st, enc, nil
}

// UnlockVolumes loads the keys of the encrypted volumes of a jail. Nothing is mounted: the start mounts the root
// first and the volumes over it. A wrong passphrase unloads what this call loaded and answers bad_passphrase.
func (s *Service) UnlockVolumes(ctx context.Context, name string, passphrase []byte) error {
	st, _, err := s.keyedState(ctx, name)
	if err != nil {
		return err
	}
	locked, err := s.lockedVolumes(ctx, st)
	if err != nil {
		return err
	}
	var loaded []string
	for _, v := range locked {
		if err := s.Keys.LoadKey(ctx, v.DS, passphrase); err != nil {
			for _, ds := range loaded {
				_ = s.Keys.UnloadKey(ctx, ds)
			}
			return err
		}
		loaded = append(loaded, v.DS)
	}
	return nil
}

// LockVolumes stops a jail, unmounts its datasets and unloads the keys of its encrypted volumes.
func (s *Service) LockVolumes(ctx context.Context, name string) error {
	_, enc, err := s.keyedState(ctx, name)
	if err != nil {
		return err
	}
	if err := s.StopJail(ctx, name); err != nil {
		return err
	}
	var errs []error
	for _, v := range enc {
		if err := s.ZFS.Unmount(ctx, v.DS); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := s.Keys.UnloadKey(ctx, v.DS); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ChangePassphrase changes the passphrase of every encrypted volume of an unlocked jail. When one volume fails, the
// volumes changed before it are changed back, so all volumes keep one passphrase.
func (s *Service) ChangePassphrase(ctx context.Context, name string, oldPass, newPass []byte) error {
	st, enc, err := s.keyedState(ctx, name)
	if err != nil {
		return err
	}
	if err := s.refuseLocked(ctx, st); err != nil {
		return err
	}
	for i, v := range enc {
		if err := s.Keys.ChangeKey(ctx, v.DS, oldPass, newPass); err != nil {
			var undo []error
			for _, d := range enc[:i] {
				if uerr := s.Keys.ChangeKey(ctx, d.DS, newPass, oldPass); uerr != nil {
					undo = append(undo, uerr)
				}
			}
			if len(undo) > 0 {
				return errors.Join(err, api.Errorf(api.CodeInternal, "changing the passphrase back failed; the volumes %s now have the new passphrase", strings.Join(names(enc[:i]), ", ")))
			}
			return err
		}
	}
	return nil
}
