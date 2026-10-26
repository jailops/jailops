package vault

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// RemoteFile is the per-host file with the HTTPS endpoint and client identity.
const RemoteFile = "remote.sops.yaml"

// Remote is how to reach one host over HTTPS with a client certificate. url and
// server_fingerprint are public and stay readable in git; client_cert and
// client_key are encrypted by SOPS (see the encrypted_regex of the policy).
type Remote struct {
	// URL is the host's API address, for example https://web1.example.com:8443.
	URL string `yaml:"url"`
	// ServerFingerprint pins the server certificate: hex SHA-256 of its DER,
	// the form `jailops` and the SDK use. Without it the tool refuses to connect.
	ServerFingerprint string `yaml:"server_fingerprint"`
	// ClientCert and ClientKey are the PEM client identity.
	ClientCert string `yaml:"client_cert"`
	ClientKey  string `yaml:"client_key"`
}

// RemotePath returns hosts/<host>/remote.sops.yaml of a fleet.
func RemotePath(fleetPath, host string) string {
	return filepath.Join(fleetPath, "hosts", host, RemoteFile)
}

// RemoteRel is the path of a host's remote file relative to the repository.
func RemoteRel(host string) string { return "hosts/" + host + "/" + RemoteFile }

// NormalizeFingerprint accepts "sha256:AA:BB..." or bare hex and returns lower
// case hex.
func NormalizeFingerprint(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "sha256:")
	return strings.ReplaceAll(s, ":", "")
}

// Check validates the fields (not the key pair against a server).
func (r *Remote) Check() error {
	if !strings.HasPrefix(r.URL, "https://") {
		return fmt.Errorf("url %q must start with https:// (a client certificate is only presented over TLS)", r.URL)
	}
	fp := NormalizeFingerprint(r.ServerFingerprint)
	if raw, err := hex.DecodeString(fp); err != nil || len(raw) != 32 {
		return errors.New("server_fingerprint must be a hex SHA-256 (the connection is pinned to it; there is no trust on first use)")
	}
	if r.ClientCert == "" || r.ClientKey == "" {
		return errors.New("client_cert and client_key are required")
	}
	if _, err := r.TLSCertificate(); err != nil {
		return err
	}
	return nil
}

// TLSCertificate parses the client identity.
func (r *Remote) TLSCertificate() (tls.Certificate, error) {
	c, err := tls.X509KeyPair([]byte(r.ClientCert), []byte(r.ClientKey))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("client_cert and client_key do not form a key pair: %w", err)
	}
	return c, nil
}

// ClientFingerprint is the pin form of the client certificate, the value a host
// is told to trust.
func (r *Remote) ClientFingerprint() (string, error) {
	blk, _ := pem.Decode([]byte(r.ClientCert))
	if blk == nil {
		return "", errors.New("client_cert is not PEM")
	}
	cert, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return "", err
	}
	return fingerprint(cert), nil
}

// RemoteStructure reads the plaintext fields of the remote file without
// decrypting (url and server_fingerprint stay readable). The encrypted fields
// come back as the ciphertext strings, which are never used.
func RemoteStructure(fleetPath, host string) (*Remote, error) {
	raw, err := ReadFile(RemotePath(fleetPath, host))
	if err != nil {
		return nil, err
	}
	var r Remote
	if err := yaml.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("%s: parse: %w", RemoteRel(host), err)
	}
	return &r, nil
}

// LoadRemote decrypts hosts/<host>/remote.sops.yaml. It returns (nil, nil)
// when the host has no such file.
func LoadRemote(fleetPath, host string, getenv func(string) string) (*Remote, error) {
	raw, err := ReadFile(RemotePath(fleetPath, host))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	plain, err := DecryptYAML(raw, getenv)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", RemoteRel(host), err)
	}
	var r Remote
	dec := yaml.NewDecoder(strings.NewReader(string(plain)))
	dec.KnownFields(true)
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("%s: parse: %w", RemoteRel(host), err)
	}
	r.ServerFingerprint = NormalizeFingerprint(r.ServerFingerprint)
	if err := r.Check(); err != nil {
		return nil, fmt.Errorf("%s: %w", RemoteRel(host), err)
	}
	return &r, nil
}

// ListRemotes returns the hosts that have a remote file, sorted.
func ListRemotes(fleetPath string) ([]string, error) {
	ents, err := os.ReadDir(filepath.Join(fleetPath, "hosts"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Lstat(RemotePath(fleetPath, e.Name())); err == nil {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}
