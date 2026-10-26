package daemon

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"filippo.io/age"
	"fmt"
	"io/fs"
	"os"
	"runtime"

	"jailops.net/jailops/internal/auth"
	"jailops.net/jailops/internal/state"
	"jailops.net/jailops/internal/store"
	"jailops.net/jailops/internal/sys/zfs"
)

func goArch() string {
	if runtime.GOARCH == "arm64" {
		return "arm64"
	}
	return "amd64"
}

// hostIdentity returns the persistent host UUID, creating it on first start.
func hostIdentity(ctx context.Context, db *store.Store) (string, error) {
	if v, err := db.Meta.Get(ctx, "host_uuid"); err == nil && v != "" {
		return v, nil
	}
	id := state.NewUUID()
	if err := db.Meta.Set(ctx, "host_uuid", id); err != nil {
		return "", fmt.Errorf("daemon: storing the host uuid: %w", err)
	}
	return id, nil
}

// loadOrCreateKey reads the seal key (mode 0600) or creates it. It seals TOTP
// secrets; it never lives in the database it protects.
func loadOrCreateKey(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		raw, derr := hex.DecodeString(string(trimNL(b)))
		if derr != nil || len(raw) != auth.SealKeySize {
			return nil, fmt.Errorf("daemon: %s is not a valid seal key", path)
		}
		return raw, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	k, err := auth.NewSealKey()
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(k)+"\n"), 0o600); err != nil {
		return nil, err
	}
	return k, nil
}

// loadOrCreateSecretsKey reads the host's age identity (the key sealed environment values are opened with), or
// creates it, with mode 0600, on first start.
func loadOrCreateSecretsKey(path string) (*age.X25519Identity, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		ids, perr := age.ParseIdentities(bytes.NewReader(b))
		if perr != nil || len(ids) != 1 {
			return nil, fmt.Errorf("daemon: %s is not a single age identity", path)
		}
		id, ok := ids[0].(*age.X25519Identity)
		if !ok {
			return nil, fmt.Errorf("daemon: %s is not an age X25519 identity", path)
		}
		return id, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte("# created by jailopsd; the public key is the recipient to seal secrets to\n# "+id.Recipient().String()+"\n"+id.String()+"\n"), 0o600); err != nil {
		return nil, err
	}
	return id, nil
}

func trimNL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

// ensureLayout creates the JailOps datasets on first start and refuses to run
// on a dataset that exists but is not owned by JailOps.
func ensureLayout(ctx context.Context, z zfs.Client, cfg Config) error {
	for _, ds := range []struct{ name, kind string }{
		{cfg.Dataset, "root"}, {cfg.Dataset + "/images", "images"}, {cfg.Dataset + "/projects", "projects"},
		{cfg.Dataset + "/deleted", "deleted"}, {cfg.Dataset + "/state", "state"},
	} {
		ok, err := z.Exists(ctx, ds.name)
		if err != nil {
			return fmt.Errorf("daemon: checking dataset %s: %w", ds.name, err)
		}
		if ok {
			p, err := z.Get(ctx, ds.name, "jailops:managed")
			if err != nil {
				return fmt.Errorf("daemon: reading dataset %s: %w", ds.name, err)
			}
			if p["jailops:managed"].Value != "on" {
				return fmt.Errorf("daemon: dataset %s exists and is not managed by JailOps; refusing to use it", ds.name)
			}
			continue
		}
		props := map[string]string{"jailops:managed": "on", "jailops:kind": ds.kind}
		switch ds.kind {
		case "root":
			props["mountpoint"] = cfg.MountBase
		case "projects":
			props["canmount"], props["mountpoint"] = "off", "none"
		}
		if err := z.Create(ctx, zfs.CreateOpts{Name: ds.name, Props: props}); err != nil {
			return fmt.Errorf("daemon: creating dataset %s: %w", ds.name, err)
		}
	}
	return nil
}
