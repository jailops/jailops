package zfsadapt

import (
	"context"

	"jailops.net/jailops/internal/sys/zfs"
)

// KeyHost is the part of the helper that handles encrypted volumes (priv.Host has it).
type KeyHost interface {
	CreateEncrypted(ctx context.Context, o zfs.CreateOpts, passphrase []byte) error
	LoadKey(ctx context.Context, dataset string, passphrase []byte) error
	UnloadKey(ctx context.Context, dataset string) error
	ChangeKey(ctx context.Context, dataset string, oldPass, newPass []byte) error
	ReplicateRaw(ctx context.Context, snapshot, dst string, props map[string]string) error
}

// Keys implements lifecycle.Keys: the key operations of the helper, and the key status read with zfs get.
type Keys struct {
	H KeyHost
	C zfs.Client
}

func (k *Keys) CreateEncrypted(ctx context.Context, name string, props map[string]string, passphrase []byte) error {
	return k.H.CreateEncrypted(ctx, zfs.CreateOpts{Name: name, Props: props}, passphrase)
}

func (k *Keys) LoadKey(ctx context.Context, dataset string, passphrase []byte) error {
	return k.H.LoadKey(ctx, dataset, passphrase)
}

func (k *Keys) UnloadKey(ctx context.Context, dataset string) error {
	return k.H.UnloadKey(ctx, dataset)
}

func (k *Keys) ChangeKey(ctx context.Context, dataset string, oldPass, newPass []byte) error {
	return k.H.ChangeKey(ctx, dataset, oldPass, newPass)
}

func (k *Keys) ReplicateRaw(ctx context.Context, snapshot, dst string, props map[string]string) error {
	return k.H.ReplicateRaw(ctx, snapshot, dst, props)
}

// KeyStatus reads the keystatus property: "available", "unavailable", or "" for a dataset that is not encrypted.
func (k *Keys) KeyStatus(ctx context.Context, dataset string) (string, error) {
	m, err := k.C.Get(ctx, dataset, "keystatus")
	if err != nil {
		return "", err
	}
	switch v := m["keystatus"].Value; v {
	case "available", "unavailable":
		return v, nil
	}
	return "", nil
}
