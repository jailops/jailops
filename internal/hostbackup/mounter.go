package hostbackup

import (
	"context"
	"os"
	"path/filepath"

	"jailops.net/jailops/internal/sys/zfs"
	"jailops.net/jailops/sdk/api"
)

// OSMounter reads snapshots through <mountpoint>/.zfs/snapshot/<name> and
// finds the directory of a mounted dataset with its mountpoint property.
// UNVERIFIED: the .zfs/snapshot automount behaviour on FreeBSD 15 for
// datasets whose snapdir is hidden, and that the access does not need a
// separate mount of the snapshot.
type OSMounter struct {
	ZFS zfs.Client
	// Remote: the daemon is not root and a file agent reads the snapshot, so the directory is not looked at
	// from here (a jail's /root is not traversable for the daemon's user).
	Remote bool
}

// SnapshotDir implements Mounter.
func (m *OSMounter) SnapshotDir(ctx context.Context, d Dataset, snap string) (string, error) {
	mp := d.Mountpoint
	if mp == "" {
		p, err := m.ZFS.Get(ctx, d.Name, "mountpoint")
		if err != nil {
			return "", err
		}
		mp = p["mountpoint"].Value
	}
	if mp == "" || mp[0] != '/' {
		return "", api.Errorf(api.CodeInvalid, "dataset %s has no mountpoint (%q), so its snapshot cannot be read", d.Name, mp)
	}
	dir := filepath.Join(mp, ".zfs", "snapshot", snap)
	if m.Remote {
		return dir, nil
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return "", api.Errorf(api.CodeInternal, "snapshot %s@%s is not readable at %s: %v", d.Name, snap, dir, err)
	}
	if !fi.IsDir() {
		return "", api.Errorf(api.CodeInternal, "%s is not a directory", dir)
	}
	return dir, nil
}

// MountDir implements Mounter.
func (m *OSMounter) MountDir(ctx context.Context, dataset string) (string, error) {
	p, err := m.ZFS.Get(ctx, dataset, "mountpoint", "mounted")
	if err != nil {
		return "", err
	}
	mp := p["mountpoint"].Value
	if mp == "" || mp[0] != '/' {
		return "", api.Errorf(api.CodeInvalid, "dataset %s has no mountpoint (%q)", dataset, mp)
	}
	if v := p["mounted"].Value; v != "yes" {
		return "", api.Errorf(api.CodeInternal, "dataset %s is not mounted (mounted=%s)", dataset, v)
	}
	return mp, nil
}
