package hostbackup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"jailops.net/jailops/internal/repo/keys"
	"jailops.net/jailops/sdk/api"
)

// ImportKeyOptions say where the key to import comes from and how to treat it. Exactly one of RecoveryKey, KeyFile
// and Identity names the source.
type ImportKeyOptions struct {
	// RecoveryKey is the printable recovery key (JOR1-...) that was shown once when the key was created.
	RecoveryKey string
	// KeyFile is the content of a key file (a copy of the backup.key of the lost host); Passphrase opens it when it
	// is wrapped.
	KeyFile    []byte
	Passphrase string
	// Identity is an age secret key (AGE-SECRET-KEY-1...) that opens the escrow blob of the repository; Domain
	// picks the blob (empty: any blob the identity opens).
	Identity string
	Domain   string
	// Replace allows a key that differs from the one the host holds: the old key file is kept beside the new one.
	Replace bool
	// Check validates and reports; nothing is written.
	Check bool
	// SkipNodeCheck imports without asking the repository (a node that is not enrolled yet, or unreachable).
	SkipNodeCheck bool
}

// ImportKeyResult describes what the import did, or with Check what it would do.
type ImportKeyResult struct {
	// Domain is the key domain of the key that came in: not secret, the fingerprint an operator compares.
	Domain string
	// Repository is the key domain the repository recorded; empty when it records none or was not asked.
	Repository string
	// Verified is true when the repository recorded exactly this domain.
	Verified bool
	// Existing is the key domain of the key the host holds now; empty when it holds none.
	Existing string
	// Installed is false when the host already held this key.
	Installed bool
	// Replaced is true when a different key was moved aside.
	Replaced bool
	// Path is the key file on the host.
	Path string
	// Aside is where the replaced key file went.
	Aside string
	// Backups is the number of backups of this key the repository holds: after an import the catalog of this host is
	// rebuilt from it, so that they are listed and can be restored. Warning says why that did not happen.
	Backups int
	Warning string
}

// resolveImportKey turns the source of an import into a key.
func (s *Service) resolveImportKey(ctx context.Context, o ImportKeyOptions) (*keys.Key, error) {
	n := 0
	for _, set := range []bool{o.RecoveryKey != "", len(o.KeyFile) > 0, o.Identity != ""} {
		if set {
			n++
		}
	}
	switch {
	case n == 0:
		return nil, api.Errorf(api.CodeInvalid, "give the key to import: a recovery key, a key file or an escrow identity")
	case n > 1:
		return nil, api.Errorf(api.CodeInvalid, "give one source of the key: a recovery key, a key file or an escrow identity")
	}
	switch {
	case o.RecoveryKey != "":
		k, err := keys.FromRecoveryKey(strings.TrimSpace(o.RecoveryKey))
		if err != nil {
			return nil, api.Errorf(api.CodeInvalid, "the recovery key is not valid: %v", err).
				WithHint("it starts with JOR1- and was printed once by 'jailops backup key create'")
		}
		return k, nil
	case len(o.KeyFile) > 0:
		kf, err := keys.ParseKeyFile(o.KeyFile)
		if err != nil {
			return nil, api.Errorf(api.CodeInvalid, "the key file is not valid: %v", err)
		}
		k, err := kf.Unwrap(o.Passphrase)
		switch {
		case errors.Is(err, keys.ErrPassphraseRequired):
			return nil, api.Errorf(api.CodeInvalid, "the key file is protected by a passphrase").WithHint("give the passphrase of the key file")
		case errors.Is(err, keys.ErrPassphrase):
			return nil, api.Errorf(api.CodeForbidden, "wrong passphrase or damaged key file")
		case err != nil:
			return nil, api.Errorf(api.CodeInvalid, "the key file is not valid: %v", err)
		}
		return k, nil
	default:
		return s.keyFromEscrow(ctx, o.Domain, o.Identity)
	}
}

// ImportKeyChecked installs a key that came from outside (a recovery key, a key file or the escrow blob) on a host
// that lost its own. Before anything is written it asks the repository: a key whose domain differs from the one the
// repository recorded is refused, so a wrong key never gets as far as a restore. It is not an error to import the key
// the host already holds; a different key needs Replace and the old key file is kept beside the new one.
func (s *Service) ImportKeyChecked(ctx context.Context, o ImportKeyOptions) (ImportKeyResult, error) {
	res := ImportKeyResult{Path: s.cfg.KeyPath}
	k, err := s.resolveImportKey(ctx, o)
	if err != nil {
		return res, err
	}
	res.Domain = k.DomainID()
	cur := s.currentKey()
	if cur != nil {
		res.Existing = cur.DomainID()
	}
	if !o.SkipNodeCheck {
		r, err := s.remoteFor(ctx, "")
		if err != nil {
			return res, api.Errorf(api.CodeInvalid, "the key cannot be checked against the repository: %v", err).
				WithHint("configure the backup node and enrol this host first, or import without the check")
		}
		if err := r.Refresh(ctx); err != nil {
			return res, api.Errorf(api.CodeMaintenance, "the key cannot be checked: the backup node did not answer: %v", err).
				WithHint("retry when the node is reachable, or import without the check")
		}
		if err := s.checkRepoKey(ctx, r, k); err != nil {
			var ae *api.Error
			if errors.As(err, &ae) && ae.Code == api.CodeForbidden && ae.Hint == "" {
				return res, ae.WithHint("this is not the key of the backups on this node: compare its key domain with 'jailops backup key status' of the old host")
			}
			return res, err
		}
		res.Repository = r.KeyDomain()
		res.Verified = res.Repository != "" && res.Repository == res.Domain
	}
	if cur != nil && res.Existing == res.Domain {
		return res, nil // already the key of this host
	}
	if o.Check {
		return res, nil
	}
	if cur != nil && !o.Replace {
		return res, api.Errorf(api.CodeAlreadyExists, "this host already holds a different backup key (domain %s)", res.Existing).
			WithHint("importing replaces it and moves the old key file aside; pass replace to do that")
	}
	if cur == nil && fileExists(s.cfg.KeyPath) {
		// A key file this daemon could not load (damaged, or a passphrase that no longer matches) is not overwritten silently.
		if !o.Replace {
			return res, api.Errorf(api.CodeAlreadyExists, "a key file already exists at %s", s.cfg.KeyPath).
				WithHint("it could not be loaded; import with replace to move it aside")
		}
	}
	if err := s.writeImportedKey(k, &res); err != nil {
		return res, err
	}
	res.Installed = true
	res.Replaced = res.Existing != ""
	// A new host has an empty catalog: rebuild it from the repository now, so that 'backup list' shows the backups and
	// a restore finds them. The key is installed either way; a node that does not answer is a warning, and the catalog
	// is rebuilt on the first restore or the next start.
	if n, err := s.Reindex(ctx, ""); err != nil {
		res.Warning = fmt.Sprintf("the key is installed, but the backups could not be listed from the repository: %v", err)
	} else {
		res.Backups = n
	}
	return res, nil
}

// writeImportedKey writes the key file, moving an existing one aside first.
func (s *Service) writeImportedKey(k *keys.Key, res *ImportKeyResult) error {
	pass, err := s.cfg.passphrase()
	if err != nil {
		return err
	}
	aside := ""
	if fileExists(s.cfg.KeyPath) {
		aside = fmt.Sprintf("%s.replaced-%d", s.cfg.KeyPath, s.now().Unix())
		if err := os.Rename(s.cfg.KeyPath, aside); err != nil {
			return fmt.Errorf("moving the old key file aside: %w", err)
		}
	}
	if err := keys.WriteKeyFile(s.cfg.KeyPath, k, keys.WrapOptions{Passphrase: pass, Argon2: s.cfg.Argon2}); err != nil {
		if aside != "" {
			_ = os.Rename(aside, s.cfg.KeyPath) // put the old key back
		}
		return err
	}
	res.Aside = aside
	s.setKey(k)
	return nil
}

func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
