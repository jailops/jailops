package zfsmodel

import (
	"context"
	"fmt"

	"jailops.net/jailops/sdk/api"
)

// The model of native encryption: an encryption root has a passphrase and a key that is loaded or not. A dataset
// whose key is not loaded cannot be mounted; a key cannot be unloaded while the dataset is mounted.

// CreateEncrypted creates an encryption root; its key is loaded afterwards.
func (m *Model) CreateEncrypted(ctx context.Context, name string, props map[string]string, passphrase []byte) error {
	if err := m.Create(ctx, name, props); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.ds[name]
	d.pass, d.keyLoaded = string(passphrase), true
	return nil
}

func (m *Model) root(name string) (*dataset, error) {
	d, err := m.get(name)
	if err != nil {
		return nil, err
	}
	if d.pass == "" {
		return nil, fmt.Errorf("'%s' is not an encryption root", name)
	}
	return d, nil
}

// LoadKey loads the key when the passphrase matches.
func (m *Model) LoadKey(_ context.Context, name string, passphrase []byte) error {
	if err := m.F.Hit("zfs load-key " + name); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	d, err := m.root(name)
	if err != nil {
		return err
	}
	if d.keyLoaded {
		return nil
	}
	if string(passphrase) != d.pass {
		return api.Errorf(api.CodeBadPassphrase, "the passphrase is not correct")
	}
	d.keyLoaded = true
	return nil
}

// UnloadKey unloads the key of an unmounted encryption root.
func (m *Model) UnloadKey(_ context.Context, name string) error {
	if err := m.F.Hit("zfs unload-key " + name); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	d, err := m.root(name)
	if err != nil {
		return err
	}
	if d.mounted {
		return api.Errorf(api.CodeBusy, "cannot unload key for '%s': dataset is busy", name)
	}
	d.keyLoaded = false
	return nil
}

// ChangeKey changes the passphrase of a loaded key after checking the old one.
func (m *Model) ChangeKey(_ context.Context, name string, oldPass, newPass []byte) error {
	if err := m.F.Hit("zfs change-key " + name); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	d, err := m.root(name)
	if err != nil {
		return err
	}
	if !d.keyLoaded {
		return api.Errorf(api.CodeLocked, "key for '%s' is not loaded", name)
	}
	if string(oldPass) != d.pass {
		return api.Errorf(api.CodeBadPassphrase, "the passphrase is not correct")
	}
	d.pass = string(newPass)
	return nil
}

// KeyStatus is "available", "unavailable", or "" for a dataset that is not encrypted.
func (m *Model) KeyStatus(_ context.Context, name string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, err := m.get(name)
	if err != nil {
		return "", err
	}
	switch {
	case d.pass == "":
		return "", nil
	case d.keyLoaded:
		return "available", nil
	}
	return "unavailable", nil
}

// ReplicateRaw is Replicate of an encryption root as stored: the copy has the same passphrase and no loaded key.
func (m *Model) ReplicateRaw(ctx context.Context, snap, dst string, props map[string]string) error {
	if err := m.Replicate(ctx, snap, dst, props); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	src, _, err := m.lookupSnap(snap)
	if err != nil {
		return err
	}
	d := m.ds[dst]
	d.pass, d.keyLoaded, d.mounted = src.pass, false, false
	return nil
}

// Passphrase returns the passphrase of an encryption root (test helper).
func (m *Model) Passphrase(name string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d := m.ds[name]; d != nil {
		return d.pass
	}
	return ""
}

// DropKeys unloads every loaded key, as a host reboot does (test helper).
func (m *Model) DropKeys() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.ds {
		if d.pass != "" {
			d.keyLoaded, d.mounted = false, false
		}
	}
}
