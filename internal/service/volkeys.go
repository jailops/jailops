package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"jailops.net/jailops/internal/store"
	"jailops.net/jailops/internal/sys/zfs"
	"jailops.net/jailops/internal/validate"
	"jailops.net/jailops/sdk/api"
)

// Encrypted volumes: unlock, lock and the passphrase change of a jail (docs/storage.md). The passphrase arrives in the
// request body, is handed to the helper on its request stream and is dropped; it is in no operation, journal, audit
// entry, log line or reply.

// Wrong passphrases: after unlockMaxFails failures in a row a jail refuses unlocks for unlockLockout.
const (
	unlockMaxFails = 5
	unlockLockout  = 30 * time.Second
)

type keyFail struct {
	n     int
	until time.Time
}

// keyState is the memory of the key operations. due names the jails the boot wanted to start while they were
// locked: an unlock starts them. A daemon restart forgets it and the next boot pass derives it again from
// boot.autostart.
type keyState struct {
	mu    sync.Mutex
	fails map[string]*keyFail
	due   map[string]bool
}

func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// creationPassphrase checks the passphrase of a create request against its volumes.
func (s *Service) creationPassphrase(spec api.JailSpec, p string) ([]byte, error) {
	enc := false
	for _, v := range spec.Volumes {
		enc = enc || v.Encrypted
	}
	switch {
	case !enc && p != "":
		return nil, invalid("volume_passphrase was given but no volume is encrypted").WithHint("mark the volume encrypted (--volume NAME:MOUNT:encrypted) or leave the passphrase out")
	case !enc:
		return nil, nil
	case s.d.Keys == nil:
		return nil, api.Errorf(api.CodeUnsupported, "encrypted volumes are not available on this host").
			WithHint("the daemon runs without the privileged helper's key operations")
	case p == "":
		return nil, invalid("a jail with an encrypted volume needs volume_passphrase").WithHint("jailops prompts for it; scripts use --passphrase-file")
	}
	if err := api.CheckPassphrase(p); err != nil {
		return nil, err
	}
	return []byte(p), nil
}

// givenPassphrase checks a passphrase that must match an existing one: 1 to MaxPassphrase bytes, one line.
func givenPassphrase(p, field string) ([]byte, error) {
	if p == "" || len(p) > api.MaxPassphrase || strings.ContainsAny(p, "\n\r\x00") {
		return nil, invalid("%s must be 1 to %d bytes without a line break", field, api.MaxPassphrase)
	}
	return []byte(p), nil
}

// lockedOut refuses while a jail is locked out after failed unlocks.
func (s *Service) lockedOut(key string) error {
	s.keys.mu.Lock()
	defer s.keys.mu.Unlock()
	f := s.keys.fails[key]
	if f == nil || f.until.IsZero() {
		return nil
	}
	now := s.now()
	if !now.Before(f.until) {
		delete(s.keys.fails, key)
		return nil
	}
	wait := f.until.Sub(now).Round(time.Second)
	if wait < time.Second {
		wait = time.Second
	}
	return api.Errorf(api.CodeRateLimited, "too many wrong passphrases for this jail").WithHint(fmt.Sprintf("wait %s and try again", wait))
}

// passphraseResult counts a wrong passphrase, or forgets the failures after a right one.
func (s *Service) passphraseResult(key string, err error) {
	s.keys.mu.Lock()
	defer s.keys.mu.Unlock()
	if s.keys.fails == nil {
		s.keys.fails = map[string]*keyFail{}
	}
	switch {
	case err == nil:
		delete(s.keys.fails, key)
	case api.IsCode(err, api.CodeBadPassphrase):
		f := s.keys.fails[key]
		if f == nil {
			f = &keyFail{}
			s.keys.fails[key] = f
		}
		f.n++
		if f.n >= unlockMaxFails {
			f.n, f.until = 0, s.now().Add(unlockLockout)
		}
	}
}

// markStartDue remembers that the boot wanted to start a locked jail.
func (s *Service) markStartDue(key string, due bool) {
	s.keys.mu.Lock()
	defer s.keys.mu.Unlock()
	if s.keys.due == nil {
		s.keys.due = map[string]bool{}
	}
	if due {
		s.keys.due[key] = true
	} else {
		delete(s.keys.due, key)
	}
}

// forgetKeys drops what is remembered about a deleted jail.
func (s *Service) forgetKeys(key string) {
	s.keys.mu.Lock()
	defer s.keys.mu.Unlock()
	delete(s.keys.due, key)
	delete(s.keys.fails, key)
}

func (s *Service) startDue(key string) bool {
	s.keys.mu.Lock()
	defer s.keys.mu.Unlock()
	return s.keys.due[key]
}

// setLocked records which encrypted volumes of a jail are locked now.
func (s *Service) setLocked(key string, locked []string) {
	s.mu.Lock()
	if e := s.jails[key]; e != nil {
		e.locked = locked
	}
	s.mu.Unlock()
}

func (s *Service) observeLocked(ctx context.Context, key string) {
	if l, err := s.life.Locked(ctx, key); err == nil {
		s.setLocked(key, l)
	}
}

// keyedJail resolves the jail of a key request.
func (s *Service) keyedJail(ctx context.Context, name string) (project, string, error) {
	if err := s.requireHost("jails"); err != nil {
		return project{}, "", err
	}
	p, key, err := s.jailRef(ctx, name)
	if err != nil {
		return p, "", err
	}
	j, err := s.getByKey(ctx, key)
	if err != nil {
		return p, "", err
	}
	if s.d.Keys == nil || s.life == nil {
		return p, "", api.Errorf(api.CodeUnsupported, "encrypted volumes are not available on this host")
	}
	for _, v := range j.Spec.Volumes {
		if v.Encrypted {
			return p, key, nil
		}
	}
	return p, "", invalid("jail %q has no encrypted volume", name)
}

// UnlockJail loads the keys of a jail's encrypted volumes with the passphrase, as an operation. The jail starts
// afterwards when the request asks for it or when the boot wanted to start it while it was locked.
func (s *Service) UnlockJail(ctx context.Context, name string, req api.JailUnlock) (api.Operation, error) {
	p, key, err := s.keyedJail(ctx, name)
	if err != nil {
		return api.Operation{}, err
	}
	pass, err := givenPassphrase(req.Passphrase, "passphrase")
	if err != nil {
		return api.Operation{}, err
	}
	if err := s.lockedOut(key); err != nil {
		wipe(pass)
		return api.Operation{}, err
	}
	op, err := s.Submit(ctx, OpSpec{
		Description: "Unlock jail " + name, Locks: []string{jailLock(key), anchorLock(key)}, Project: p.Meta.Name,
		Resources: map[string][]string{"jails": {"/api/v1/jails/" + name}},
		Run: func(ctx context.Context, h *OpHandle) (any, error) {
			defer wipe(pass)
			if err := s.lockedOut(key); err != nil {
				return nil, err
			}
			err := s.life.UnlockVolumes(ctx, key, pass)
			s.passphraseResult(key, err)
			s.observeLocked(ctx, key)
			if err != nil {
				return nil, err
			}
			s.emitIn(ctx, p.Meta.Name, "jail-unlocked", "/api/v1/jails/"+name)
			if req.Start || s.startDue(key) {
				s.markStartDue(key, false)
				_ = h.Bind("start/"+key, &opRequest{Kind: "start", Jail: key})
				if err := s.life.StartJail(ctx, key); err != nil {
					return nil, fmt.Errorf("unlocked, but the start failed: %w", err)
				}
				if err := s.provisionPending(ctx, key); err != nil {
					s.kick()
					return nil, err
				}
				s.emitIn(ctx, p.Meta.Name, "jail-started", "/api/v1/jails/"+name)
			}
			s.kick()
			return s.getByKey(ctx, key)
		},
	})
	if err != nil {
		wipe(pass)
	}
	return op, err
}

// LockJail stops a jail, unmounts its datasets and unloads the keys of its encrypted volumes, as an operation.
func (s *Service) LockJail(ctx context.Context, name string) (api.Operation, error) {
	p, key, err := s.keyedJail(ctx, name)
	if err != nil {
		return api.Operation{}, err
	}
	return s.Submit(ctx, OpSpec{
		Description: "Lock jail " + name, Locks: []string{jailLock(key), anchorLock(key)}, Project: p.Meta.Name,
		Resources: map[string][]string{"jails": {"/api/v1/jails/" + name}},
		Run: func(ctx context.Context, h *OpHandle) (any, error) {
			s.markStartDue(key, false)
			_ = h.Bind("stop/"+key, &opRequest{Kind: "stop", Jail: key})
			err := s.life.LockVolumes(ctx, key)
			s.observeLocked(ctx, key)
			if err != nil {
				return nil, err
			}
			s.markStopped(key)
			s.emitIn(ctx, p.Meta.Name, "jail-locked", "/api/v1/jails/"+name)
			s.kick()
			return s.getByKey(ctx, key)
		},
	})
}

// ChangeJailPassphrase changes the passphrase of every encrypted volume of an unlocked jail, as an operation. A
// wrong old passphrase counts like a wrong unlock.
func (s *Service) ChangeJailPassphrase(ctx context.Context, name string, req api.JailPassphraseChange) (api.Operation, error) {
	p, key, err := s.keyedJail(ctx, name)
	if err != nil {
		return api.Operation{}, err
	}
	oldPass, err := givenPassphrase(req.Old, "old_passphrase")
	if err != nil {
		return api.Operation{}, err
	}
	if err := api.CheckPassphrase(req.New); err != nil {
		return api.Operation{}, err
	}
	if req.New == req.Old {
		return api.Operation{}, invalid("the new passphrase is the old one")
	}
	newPass := []byte(req.New)
	if err := s.lockedOut(key); err != nil {
		return api.Operation{}, err
	}
	op, err := s.Submit(ctx, OpSpec{
		Description: "Change the volume passphrase of jail " + name, Locks: []string{jailLock(key)}, Project: p.Meta.Name,
		Resources: map[string][]string{"jails": {"/api/v1/jails/" + name}},
		Run: func(ctx context.Context, h *OpHandle) (any, error) {
			defer wipe(oldPass)
			defer wipe(newPass)
			if err := s.lockedOut(key); err != nil {
				return nil, err
			}
			err := s.life.ChangePassphrase(ctx, key, oldPass, newPass)
			s.passphraseResult(key, err)
			if err != nil {
				return nil, err
			}
			s.emitIn(ctx, p.Meta.Name, "jail-passphrase-changed", "/api/v1/jails/"+name)
			return s.getByKey(ctx, key)
		},
	})
	if err != nil {
		wipe(oldPass)
		wipe(newPass)
	}
	return op, err
}

// bootLocked is what the boot does with a jail it may not start yet: it is remembered and started by its unlock.
func (s *Service) bootLocked(ctx context.Context, key string, err error) {
	s.markStartDue(key, true)
	project, name := validate.SplitQualified(key)
	s.Audit(ctx, store.AuditEntry{Actor: ActorFrom(ctx).Name, ActorKind: ActorFrom(ctx).Kind, Method: "AUTOSTART", Route: "reconcile",
		Object: "/api/v1/jails/" + name, Project: project, Result: "skipped", Detail: "locked: waiting for a passphrase"})
	s.log.Info("autostart waits for the passphrase of a locked jail", "jail", key, "err", err)
}

// lockedOf names the locked volumes of one jail.
func (s *Service) lockedOf(key string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if e := s.jails[key]; e != nil {
		return append([]string(nil), e.locked...)
	}
	return nil
}

// LockedJails lists the keys of the jails with a locked volume, and which volumes (doctor).
func (s *Service) LockedJails() map[string][]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string][]string{}
	for k, e := range s.jails {
		if len(e.locked) > 0 {
			out[k] = append([]string(nil), e.locked...)
		}
	}
	return out
}

// doctorEncryption reports the jails that wait for a passphrase (volumes.locked) and whether the pools of the
// storages can hold encrypted volumes (volumes.encryption).
func (s *Service) doctorEncryption(ctx context.Context, c *checkSet) {
	var keys []string
	s.mu.RLock()
	for k, e := range s.jails {
		for _, v := range e.jail.Spec.Volumes {
			if v.Encrypted {
				keys = append(keys, k)
				break
			}
		}
	}
	s.mu.RUnlock()
	encrypted := map[string]bool{} // pool -> a jail there has an encrypted volume
	for _, k := range keys {
		if si, ok := s.storageOfJail(k); ok {
			encrypted[poolOf(si.Root)] = true
		}
	}
	if locked := s.LockedJails(); len(locked) > 0 {
		var names []string
		for k := range locked {
			_, n := validate.SplitQualified(k)
			names = append(names, n)
		}
		sort.Strings(names)
		c.add("volumes.locked", api.DoctorInfo, fmt.Sprintf("%d jail(s) wait for a passphrase: %s", len(names), strings.Join(names, ", ")),
			"jailops jail unlock NAME (a jail due for autostart starts after its unlock)")
	} else if len(encrypted) > 0 {
		c.add("volumes.locked", api.DoctorOK, "no jail waits for a passphrase", "")
	}
	fs, ok := s.d.ZClient.(zfs.FeatureStater)
	if s.d.ZClient == nil || !ok {
		c.add("volumes.encryption", api.DoctorUnknown, "the pool features cannot be read here", "")
		return
	}
	pools := map[string]bool{}
	for _, si := range s.storageList() {
		pools[poolOf(si.Root)] = true
	}
	var names []string
	for p := range pools {
		names = append(names, p)
	}
	sort.Strings(names)
	for _, p := range names {
		name := "volumes.encryption"
		if p != s.d.Pool {
			name += "." + p
		}
		st, err := fs.FeatureState(ctx, p, "encryption")
		switch {
		case err != nil:
			c.add(name, api.DoctorUnknown, "feature@encryption of pool "+p+": "+err.Error(), "")
		case st == zfs.FeatureActive || st == zfs.FeatureEnabled:
			c.add(name, api.DoctorOK, "pool "+p+" can hold encrypted volumes (feature@encryption "+st+")", "")
		case encrypted[p]:
			c.add(name, api.DoctorFail, "pool "+p+" holds jails with encrypted volumes but reports feature@encryption "+dashIf(st), "zpool get feature@encryption "+p)
		default:
			c.add(name, api.DoctorWarn, "pool "+p+" cannot hold encrypted volumes: feature@encryption is "+dashIf(st),
				"JailOps never changes pool features; an administrator may run 'zpool set feature@encryption=enabled "+p+"' (older ZFS releases then cannot import the pool)")
		}
	}
}

func dashIf(s string) string {
	if s == "" {
		return "unknown to this ZFS"
	}
	return s
}
