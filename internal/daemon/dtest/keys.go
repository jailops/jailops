package dtest

import (
	"context"
	"io"
	"sync"

	"jailops.net/jailops/internal/sys/zfs"
	"jailops.net/jailops/sdk/api"
)

// FakeKeys stands in for the helper's key operations of encrypted volumes (lifecycle.Keys) on top of the fake
// pool: it remembers the passphrase and the key status of each encryption root.
type FakeKeys struct {
	Z  *zfs.Fake
	mu sync.Mutex
	// pass and loaded are per dataset; ops lists the operations without their passphrases.
	pass   map[string]string
	loaded map[string]bool
	ops    []string
}

// NewFakeKeys returns key operations over z.
func NewFakeKeys(z *zfs.Fake) *FakeKeys {
	return &FakeKeys{Z: z, pass: map[string]string{}, loaded: map[string]bool{}}
}

func (k *FakeKeys) note(op string) {
	k.ops = append(k.ops, op)
}

func (k *FakeKeys) CreateEncrypted(ctx context.Context, name string, props map[string]string, passphrase []byte) error {
	if err := k.Z.Create(ctx, zfs.CreateOpts{Name: name, Props: props}); err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.note("create " + name)
	k.pass[name], k.loaded[name] = string(passphrase), true
	return nil
}

func (k *FakeKeys) LoadKey(_ context.Context, ds string, passphrase []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.note("load-key " + ds)
	p, ok := k.pass[ds]
	if !ok {
		return api.Errorf(api.CodeForbidden, "%s is not an encrypted volume", ds)
	}
	if k.loaded[ds] {
		return nil
	}
	if p != string(passphrase) {
		return api.Errorf(api.CodeBadPassphrase, "the passphrase is not correct")
	}
	k.loaded[ds] = true
	return nil
}

func (k *FakeKeys) UnloadKey(_ context.Context, ds string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.note("unload-key " + ds)
	if _, ok := k.pass[ds]; !ok {
		return api.Errorf(api.CodeForbidden, "%s is not an encrypted volume", ds)
	}
	k.loaded[ds] = false
	return nil
}

func (k *FakeKeys) ChangeKey(_ context.Context, ds string, oldPass, newPass []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.note("change-key " + ds)
	if !k.loaded[ds] {
		return api.Errorf(api.CodeLocked, "%s is locked", ds)
	}
	if k.pass[ds] != string(oldPass) {
		return api.Errorf(api.CodeBadPassphrase, "the passphrase is not correct")
	}
	k.pass[ds] = string(newPass)
	return nil
}

func (k *FakeKeys) KeyStatus(_ context.Context, ds string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, ok := k.pass[ds]; !ok {
		return "", nil
	}
	if k.loaded[ds] {
		return "available", nil
	}
	return "unavailable", nil
}

func (k *FakeKeys) ReplicateRaw(ctx context.Context, snapshot, dst string, props map[string]string) error {
	pr, pw := pipe()
	go func() { pw.CloseWithError(k.Z.Send(ctx, zfs.SendOpts{Snapshot: snapshot, Raw: true}, pw)) }()
	if err := k.Z.Receive(ctx, zfs.ReceiveOpts{Target: dst, Props: props}, pr); err != nil {
		return err
	}
	src, _ := zfs.SplitSnapshot(snapshot)
	k.mu.Lock()
	defer k.mu.Unlock()
	k.note("replicate-raw " + snapshot + " " + dst)
	k.pass[dst], k.loaded[dst] = k.pass[src], false
	return nil
}

// Reboot unloads every key, as a host boot does.
func (k *FakeKeys) Reboot() {
	k.mu.Lock()
	defer k.mu.Unlock()
	for ds := range k.loaded {
		k.loaded[ds] = false
	}
}

// Passphrase is the passphrase of an encryption root.
func (k *FakeKeys) Passphrase(ds string) string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.pass[ds]
}

// Ops lists the key operations so far (names and datasets, no passphrase).
func (k *FakeKeys) Ops() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.ops...)
}

func pipe() (*io.PipeReader, *io.PipeWriter) { return io.Pipe() }
