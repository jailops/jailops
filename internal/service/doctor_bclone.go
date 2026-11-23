package service

import (
	"context"

	"jailops.net/jailops/internal/sys/zfs"
	"jailops.net/jailops/sdk/api"
)

// bcloneSysctl turns block cloning on and off for the whole host (OpenZFS 2.2 and later).
const bcloneSysctl = "vfs.zfs.bclone_enabled"

// doctorBlockCloning says, per storage, whether a new jail shares the blocks of its image (block cloning) or is a full
// copy of it. A root costs a few MB only when the pool has enabled feature@block_cloning, the host has not turned
// it off, and the storage is not encrypted (ZFS shares no blocks between datasets that do not share a key). A value that
// cannot be read gives no row: the check never warns on a guess.
func (s *Service) doctorBlockCloning(ctx context.Context, c *checkSet) {
	fs, ok := s.d.ZClient.(zfs.FeatureStater)
	if s.d.ZClient == nil || !ok {
		return
	}
	sysctlOff := false
	if v, err := s.sysctl(ctx, bcloneSysctl); err == nil && v == "0" {
		sysctlOff = true
	}
	for _, si := range s.storageList() {
		name := "zfs.block_cloning"
		if si.Name != DefaultStorage {
			name += "." + si.Name
		}
		pool := poolOf(si.Root)
		if m, err := s.d.ZClient.Get(ctx, si.Root, "encryption"); err == nil {
			if p, ok := m["encryption"]; ok && p.Value != "" && p.Value != "off" && p.Value != "-" {
				c.add(name, api.DoctorInfo, "storage "+si.Name+" is encrypted: jails on it are full copies of their image (ZFS does not share blocks between datasets that do not share a key), about 50 MB each", "")
				continue
			}
		}
		st, err := fs.FeatureState(ctx, pool, "block_cloning")
		switch {
		case err != nil || st == "":
			continue // unreadable, or a ZFS that does not know the feature: nothing is said on a guess
		case st == zfs.FeatureDisabled:
			c.add(name, api.DoctorWarn, "pool "+pool+" has not enabled block cloning: every new jail is a full copy of its image (about 50 MB each). 'zpool upgrade "+pool+"' enables it; after that, systems older than OpenZFS 2.2 cannot import the pool.",
				"zpool upgrade "+pool+" (JailOps never changes pool features)")
		case sysctlOff:
			c.add(name, api.DoctorWarn, "block cloning is turned off on this host ("+bcloneSysctl+"=0): new jails are full copies", "sysctl "+bcloneSysctl+"=1 and add it to /etc/sysctl.conf")
		default:
			c.add(name, api.DoctorOK, "storage "+si.Name+" (pool "+pool+"): block cloning on; a new jail costs about 3 MB until it diverges from its image", "")
		}
	}
}
