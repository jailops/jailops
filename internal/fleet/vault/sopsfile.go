package vault

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/getsops/sops/v3"
	"github.com/getsops/sops/v3/aes"
	"github.com/getsops/sops/v3/cmd/sops/common"
	"github.com/getsops/sops/v3/config"
	"github.com/getsops/sops/v3/decrypt"
	"github.com/getsops/sops/v3/keyservice"
	"github.com/getsops/sops/v3/version"
)

// maxFile bounds every SOPS file read.
const maxFile = 1 << 20

// IsEncrypted reports whether a YAML file carries a top-level sops block.
func IsEncrypted(raw []byte) bool {
	return bytes.HasPrefix(raw, []byte("sops:\n")) || bytes.Contains(raw, []byte("\nsops:\n"))
}

// ReadFile reads a regular, bounded file; symlinks are refused.
func ReadFile(p string) ([]byte, error) {
	st, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", p)
	}
	if st.Size() > maxFile {
		return nil, fmt.Errorf("%s: larger than %d bytes", p, maxFile)
	}
	return os.ReadFile(p)
}

// DecryptYAML decrypts SOPS-encrypted YAML. A file without a sops block is
// refused: a plaintext secrets file in a repository is a mistake that must not
// pass silently.
func DecryptYAML(raw []byte, getenv func(string) string) ([]byte, error) {
	if !IsEncrypted(raw) {
		return nil, errors.New("the file is not SOPS-encrypted (no sops block); encrypt it with the repository's .sops.yaml before committing")
	}
	if err := EnsureUnlocked(getenv); err != nil {
		return nil, err
	}
	// SOPS treats a variable that is set but empty as configured, and its
	// SOPS_AGE_KEY_CMD handling panics on an empty command: drop empty ones.
	for _, k := range []string{EnvKey, EnvKeyFile, EnvKeyCmd} {
		if v, ok := os.LookupEnv(k); ok && v == "" {
			_ = os.Unsetenv(k)
		}
	}
	out, err := decrypt.Data(raw, "yaml")
	if err != nil {
		return nil, fmt.Errorf("cannot decrypt with the configured key: %w", err)
	}
	return out, nil
}

// EncryptYAML encrypts plaintext YAML for the file at fleetPath/rel under the
// creation rule of fleetPath/.sops.yaml that matches rel. The plaintext never
// touches the disk. It does not write the result.
func EncryptYAML(fleetPath, rel string, plaintext []byte) ([]byte, error) {
	policy := filepath.Join(fleetPath, SopsPolicyFilename)
	abs := filepath.Join(fleetPath, rel)
	conf, err := config.LoadCreationRuleForFile(policy, abs, map[string]*string{})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", SopsPolicyFilename, err)
	}
	if conf == nil {
		return nil, fmt.Errorf("%s has no creation_rule for %s", SopsPolicyFilename, rel)
	}
	sc, err := config.LoadStoresConfig(policy)
	if err != nil {
		return nil, err
	}
	store := common.DefaultStoreForPathOrFormat(sc, abs, "yaml")
	branches, err := store.LoadPlainFile(plaintext)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", rel, err)
	}
	if len(branches) < 1 {
		return nil, errors.New("the file is empty")
	}
	if store.HasSopsTopLevelKey(branches[0]) {
		return nil, fmt.Errorf("%s already has a sops key; it is probably encrypted", rel)
	}
	tree := sops.Tree{
		Branches: branches,
		FilePath: abs,
		Metadata: sops.Metadata{
			KeyGroups:               conf.KeyGroups,
			UnencryptedSuffix:       conf.UnencryptedSuffix,
			EncryptedSuffix:         conf.EncryptedSuffix,
			UnencryptedRegex:        conf.UnencryptedRegex,
			EncryptedRegex:          conf.EncryptedRegex,
			UnencryptedCommentRegex: conf.UnencryptedCommentRegex,
			EncryptedCommentRegex:   conf.EncryptedCommentRegex,
			MACOnlyEncrypted:        conf.MACOnlyEncrypted,
			Version:                 version.Version,
			ShamirThreshold:         conf.ShamirThreshold,
		},
	}
	svcs := []keyservice.KeyServiceClient{keyservice.NewLocalClient()}
	dataKey, errs := tree.GenerateDataKeyWithKeyServices(svcs)
	if len(errs) > 0 {
		return nil, fmt.Errorf("cannot generate the data key: %v", errs)
	}
	if err := common.EncryptTree(common.EncryptTreeOpts{DataKey: dataKey, Tree: &tree, Cipher: aes.NewCipher()}); err != nil {
		return nil, err
	}
	return store.EmitEncryptedFile(tree)
}

// UpdateKeys re-wraps the data key of the encrypted files (paths relative to
// fleetPath) for the recipients .sops.yaml lists now. It stops at the first
// failure and says which files were done. The work is SOPS's own (the routine
// of `sops updatekeys`), without its prompts and printing.
func UpdateKeys(fleetPath string, rels []string) (done []string, failed string, err error) {
	policy := filepath.Join(fleetPath, SopsPolicyFilename)
	for _, rel := range rels {
		if e := updateKeysOf(filepath.Join(fleetPath, filepath.FromSlash(rel)), policy); e != nil {
			return done, rel, fmt.Errorf("re-wrap %s: %w", rel, e)
		}
		done = append(done, rel)
	}
	return done, "", nil
}

func updateKeysOf(path, policy string) error {
	sc, err := config.LoadStoresConfig(policy)
	if err != nil {
		return err
	}
	store := common.DefaultStoreForPathOrFormat(sc, path, "yaml")
	tree, err := common.LoadEncryptedFile(store, path)
	if err != nil {
		return err
	}
	conf, err := config.LoadCreationRuleForFile(policy, path, map[string]*string{})
	if err != nil {
		return err
	}
	if conf == nil || len(conf.KeyGroups) == 0 {
		return fmt.Errorf("%s has no creation_rule with recipients for this file", SopsPolicyFilename)
	}
	changed := false
	for _, d := range common.DiffKeyGroups(tree.Metadata.KeyGroups, conf.KeyGroups) {
		if len(d.Added) > 0 || len(d.Removed) > 0 {
			changed = true
		}
	}
	if !changed {
		return nil
	}
	svcs := []keyservice.KeyServiceClient{keyservice.NewLocalClient()}
	key, err := tree.Metadata.GetDataKeyWithKeyServices(svcs, nil)
	if err != nil {
		return fmt.Errorf("cannot open the data key (is the configured key still a recipient?): %w", err)
	}
	tree.Metadata.KeyGroups = conf.KeyGroups
	tree.Metadata.ShamirThreshold = min(tree.Metadata.ShamirThreshold, len(conf.KeyGroups))
	if errs := tree.Metadata.UpdateMasterKeysWithKeyServices(key, svcs); len(errs) > 0 {
		return fmt.Errorf("cannot wrap the data key for every recipient: %v", errs)
	}
	out, err := store.EmitEncryptedFile(*tree)
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, out, 0o644)
}

// WriteFileAtomic writes data to p through a temporary file in the same
// directory.
func WriteFileAtomic(p string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".sops-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// RelSlash returns p relative to base with forward slashes.
func RelSlash(base, p string) string {
	r, err := filepath.Rel(base, p)
	if err != nil {
		return p
	}
	return strings.ReplaceAll(r, string(filepath.Separator), "/")
}

// CheckCovered fails when .sops.yaml is missing or has no creation_rule for the
// file at fleetPath/rel. It needs no key.
func CheckCovered(fleetPath, rel string) error {
	policy := filepath.Join(fleetPath, SopsPolicyFilename)
	if _, err := os.Stat(policy); err != nil {
		return fmt.Errorf("%s has no %s: it decides who can decrypt", rel, SopsPolicyFilename)
	}
	conf, err := config.LoadCreationRuleForFile(policy, filepath.Join(fleetPath, filepath.FromSlash(rel)), map[string]*string{})
	if err != nil || conf == nil {
		return fmt.Errorf("%s has no creation_rule matching %s", SopsPolicyFilename, rel)
	}
	if len(conf.KeyGroups) == 0 {
		return fmt.Errorf("%s: the creation_rule for %s names no recipient", SopsPolicyFilename, rel)
	}
	return nil
}
