package vault

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// SecretsRel is the fleet-wide SOPS secrets file, relative to the repository.
const SecretsRel = "shared/secrets.sops.yaml"

// SecretsPath returns the path of the secrets file in a fleet.
func SecretsPath(fleetPath string) string {
	return filepath.Join(fleetPath, filepath.FromSlash(SecretsRel))
}

// LoadSecrets decrypts shared/secrets.sops.yaml into a map. A repository
// without the file has no secrets and gets an empty map. A file that is not
// encrypted is an error.
func LoadSecrets(fleetPath string, getenv func(string) string) (map[string]any, error) {
	p := SecretsPath(fleetPath)
	raw, err := ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	plain, err := DecryptYAML(raw, getenv)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", SecretsRel, err)
	}
	var m map[string]any
	if err := yaml.Unmarshal(plain, &m); err != nil {
		return nil, fmt.Errorf("%s: parse: %w", SecretsRel, err)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

// SecretsStructure returns the key structure of the secrets file without
// decrypting anything: SOPS encrypts values and leaves keys readable. The sops
// metadata block is removed. A missing file gives an empty map.
func SecretsStructure(fleetPath string) (map[string]any, error) {
	raw, err := ReadFile(SecretsPath(fleetPath))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	var m map[string]any
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("%s: parse: %w", SecretsRel, err)
	}
	if m == nil {
		m = map[string]any{}
	}
	delete(m, "sops")
	return m, nil
}

// PathExists reports whether a dotted path leads to a leaf (or subtree) of m.
func PathExists(m map[string]any, path string) bool {
	if path == "" {
		return false
	}
	var cur any = m
	for _, p := range strings.Split(path, ".") {
		mm, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		if cur, ok = mm[p]; !ok {
			return false
		}
	}
	return true
}

// Lookup returns the string at a dotted path. Secrets are leaf strings;
// anything else is an error.
func Lookup(m map[string]any, path string) (string, error) {
	if path == "" {
		return "", errors.New("empty secret path")
	}
	parts := strings.Split(path, ".")
	var cur any = m
	for i, p := range parts {
		mm, ok := cur.(map[string]any)
		if !ok {
			return "", fmt.Errorf("secret %q: %s is not a mapping", path, strings.Join(parts[:i], "."))
		}
		if cur, ok = mm[p]; !ok {
			return "", fmt.Errorf("secret %q: missing at %s", path, strings.Join(parts[:i+1], "."))
		}
	}
	s, ok := cur.(string)
	if !ok {
		return "", fmt.Errorf("secret %q: the value is not a string (it is %T)", path, cur)
	}
	return s, nil
}
