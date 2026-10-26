package hostbackup

import (
	"context"
	"errors"

	"jailops.net/jailops/internal/repo/engine"

	"jailops.net/jailops/internal/repo/keys"
	"jailops.net/jailops/sdk/api"
)

func isNoKeyFile(err error) bool { return errors.Is(err, keys.ErrNoKeyFile) }

func (s *Service) currentKey() *keys.Key {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.key
}

func (s *Service) setKey(k *keys.Key) {
	s.mu.Lock()
	s.key = k
	s.mu.Unlock()
	s.dropRemotes() // their key domain changed
}

// LoadKey reads the key file. It returns keys.ErrNoKeyFile when there is none.
func (s *Service) LoadKey() error {
	pass, err := s.cfg.passphrase()
	if err != nil {
		return err
	}
	k, err := keys.Load(s.cfg.KeyPath, pass)
	if err != nil {
		return err
	}
	s.setKey(k)
	return nil
}

// HoldsKey reports whether this host holds the key of a key domain.
func (s *Service) HoldsKey(domain string) bool {
	k := s.currentKey()
	return k != nil && domain != "" && k.DomainID() == domain
}

// CreateKeyOptions control CreateKey.
type CreateKeyOptions struct {
	// Recipients are age public keys the key is escrowed to; empty uses
	// Config.RecoveryRecipients. Escrow needs the node, so it is skipped
	// (and reported) when there are no recipients.
	Recipients []string
}

// CreateKeyResult is what CreateKey returns.
type CreateKeyResult struct {
	Domain string
	// RecoveryKey is shown to the operator once. Nothing stores it.
	RecoveryKey string
	// Escrowed is true when the escrow blob is in the repository.
	Escrowed bool
	// EscrowErr is why escrow did not happen, if it was attempted. The key
	// exists either way; retry with EscrowKey.
	EscrowErr error
}

// CreateKey creates the host key file (mode 0600, wrapped with Argon2id when
// a passphrase is configured), then escrows it when recipients are known. It
// refuses to replace an existing key file: rotating means a new repository.
func (s *Service) CreateKey(ctx context.Context, o CreateKeyOptions) (CreateKeyResult, error) {
	var res CreateKeyResult
	pass, err := s.cfg.passphrase()
	if err != nil {
		return res, err
	}
	k, rec, err := keys.Create(s.cfg.KeyPath, keys.WrapOptions{Passphrase: pass, Argon2: s.cfg.Argon2})
	if err != nil {
		if errors.Is(err, keys.ErrKeyFileExists) {
			return res, api.Errorf(api.CodeAlreadyExists, "a backup key already exists at %s", s.cfg.KeyPath).
				WithHint("rotating the key means a new repository; the old one stays readable")
		}
		return res, err
	}
	s.setKey(k)
	res.Domain, res.RecoveryKey = k.DomainID(), rec
	recips := o.Recipients
	if len(recips) == 0 {
		recips = s.cfg.RecoveryRecipients
	}
	if len(recips) > 0 {
		if err := s.EscrowKey(ctx, recips); err != nil {
			res.EscrowErr = err
		} else {
			res.Escrowed = true
		}
	}
	return res, nil
}

// EscrowKey wraps the key to the recovery recipients (age public keys; empty
// uses Config.RecoveryRecipients) and uploads the blob to the node. The node
// stores it create-only; the same escrow again is a success.
func (s *Service) EscrowKey(ctx context.Context, recipients []string) error {
	k := s.currentKey()
	if k == nil {
		return noKeyErr()
	}
	if len(recipients) == 0 {
		recipients = s.cfg.RecoveryRecipients
	}
	if len(recipients) == 0 {
		return api.Errorf(api.CodeInvalid, "escrow needs at least one recovery recipient (an age public key)")
	}
	r, err := s.remoteFor(ctx, "")
	if err != nil {
		return err
	}
	if err := r.Refresh(ctx); err != nil {
		return err
	}
	if err := s.checkRepoKey(ctx, r, k); err != nil {
		return err
	}
	return keys.Escrow(ctx, k, recipients, r)
}

// KeyStatus is what the host knows about its key.
type KeyStatus struct {
	Status keys.Status
	Domain string
	Path   string
}

// Status reports none, present or escrowed. When the repository cannot be
// asked the status is present and the error says why.
func (s *Service) Status(ctx context.Context) (KeyStatus, error) {
	k := s.currentKey()
	st := KeyStatus{Path: s.cfg.KeyPath}
	if k == nil {
		return st, nil
	}
	st.Domain = k.DomainID()
	r, err := s.remoteFor(ctx, "")
	if err != nil {
		st.Status = keys.StatusPresent
		return st, err
	}
	st.Status, err = keys.StatusOf(ctx, k, r)
	return st, err
}

// ImportKey installs a key from a recovery key (a new host restoring needs
// only this) and writes the key file. It refuses to replace an existing key.
func (s *Service) ImportKey(recoveryKey string) error {
	k, err := keys.FromRecoveryKey(recoveryKey)
	if err != nil {
		return api.Errorf(api.CodeInvalid, "the recovery key is not valid: %v", err)
	}
	return s.installKey(k)
}

// RecoverKeyFromEscrow fetches the escrow blob of a key domain from the
// repository, opens it with an age identity (AGE-SECRET-KEY-1...) and
// installs the key. The identity is used once and not stored.
func (s *Service) RecoverKeyFromEscrow(ctx context.Context, domain, identity string) error {
	r, err := s.remoteFor(ctx, "")
	if err != nil {
		return err
	}
	var domains []string
	if domain != "" {
		domains = []string{domain}
	} else if domains, err = r.ListEscrow(ctx); err != nil {
		return err
	}
	if len(domains) == 0 {
		return api.Errorf(api.CodeNotFound, "the repository holds no escrow blob")
	}
	var k *keys.Key
	for _, d := range domains {
		blob, gerr := r.GetEscrow(ctx, d)
		if gerr != nil {
			return gerr
		}
		if k, err = keys.OpenEscrow(blob, identity); err == nil {
			break
		}
	}
	if k == nil || err != nil {
		return api.Errorf(api.CodeForbidden, "%v", err)
	}
	return s.installKey(k)
}

func (s *Service) installKey(k *keys.Key) error {
	if s.currentKey() != nil {
		return api.Errorf(api.CodeAlreadyExists, "this host already holds a backup key")
	}
	pass, err := s.cfg.passphrase()
	if err != nil {
		return err
	}
	if err := keys.WriteKeyFile(s.cfg.KeyPath, k, keys.WrapOptions{Passphrase: pass, Argon2: s.cfg.Argon2}); err != nil {
		if errors.Is(err, keys.ErrKeyFileExists) {
			return api.Errorf(api.CodeAlreadyExists, "a key file already exists at %s", s.cfg.KeyPath)
		}
		return err
	}
	s.setKey(k)
	return nil
}

// UseKey holds a key in memory only (a recovery host that loads the recovery
// key temporarily); nothing is written.
func (s *Service) UseKey(k *keys.Key) { s.setKey(k) }

func noKeyErr() error {
	return api.Errorf(api.CodePlaintextDenied, "this host holds no backup key; backups are always encrypted on the host, so nothing is sent").
		WithHint("create the key with CreateKey (jailops backup key create) or import it from the recovery key")
}

// requireKey is the check before every capture: a key must be held and, unless
// the configuration allows it, escrowed.
func (s *Service) requireKey(ctx context.Context, r Remote) (*keys.Key, error) {
	k := s.currentKey()
	if k == nil {
		return nil, noKeyErr()
	}
	if err := s.checkRepoKey(ctx, r, k); err != nil {
		return nil, err
	}
	st, err := keys.StatusOf(ctx, k, r)
	if err != nil {
		return nil, err
	}
	if st != keys.StatusEscrowed && !s.cfg.AllowUnescrowed {
		return nil, api.Errorf(api.CodeKeyNotEscrowed, "the backup key is not escrowed to an offline recovery recipient").
			WithHint("run EscrowKey with an age public key, or allow unescrowed keys in the configuration")
	}
	return k, nil
}

// checkRepoKey is the host's side of the key-domain pin. A repository that recorded a domain must record this
// key's; one that recorded none (an old repository, or a node a stranger initialised) is trusted only when the
// key opens a snapshot already in it, or when it holds nothing yet. Without this a node could claim any domain
// (or none) and receive an escrow blob and backups for a key it was never bound to.
func (s *Service) checkRepoKey(ctx context.Context, r Remote, k *keys.Key) error {
	d := r.KeyDomain()
	if d != "" {
		if d != k.DomainID() {
			return api.Errorf(api.CodeForbidden, "the repository belongs to key domain %s, this host's key is %s", d, k.DomainID())
		}
		return nil
	}
	if _, isNode := r.(*nodeRemote); !isNode {
		return nil // a directory the host itself attached is trusted by being its own
	}
	groups, err := r.Groups(ctx)
	if err != nil {
		return err
	}
	if len(groups) == 0 {
		return nil
	}
	if s.deps.Catalog != nil {
		bs, err := s.deps.Catalog.Backups(ctx)
		if err != nil {
			return err
		}
		for _, b := range bs {
			if b.Spec.KeyDomain != k.DomainID() {
				continue
			}
			g := k.Keys.GroupID(b.Spec.HostUUID, b.Spec.JailUUID)
			if _, err := engine.OpenSnapshot(ctx, r, k.Keys, g, b.Spec.Snapshot); err == nil {
				return nil
			} else if errors.Is(err, engine.ErrWrongKey) {
				return api.Errorf(api.CodeForbidden, "the repository records no key domain and this key does not open its snapshots")
			}
		}
	}
	return api.Errorf(api.CodeForbidden, "the repository records no key domain and holds snapshots this host cannot show to be its own; initialise the node with the key domain %s", k.DomainID())
}
