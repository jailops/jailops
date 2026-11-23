package service_test

import (
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk/api"
)

// doctor says per storage whether a new jail shares its image's blocks: OK with the cost, WARN for a pool that has not
// enabled block cloning and for a host that turned it off, INFO for an encrypted storage; an unreadable value says nothing.
func TestDoctorBlockCloning(t *testing.T) {
	e := dtest.New(t, dtest.Options{})
	e.Seed()
	s := e.D.Service()
	ctx := as(globalAdmin, "")
	row := func() (api.DoctorCheck, bool) { return doctorCheck(s.Doctor(ctx), "zfs.block_cloning") }
	// the fake knows no vfs.zfs.bclone_enabled: unreadable, so no warning; the feature is enabled
	if c, _ := row(); c.Status != api.DoctorOK || !strings.Contains(c.Message, "about 3 MB") || !strings.Contains(c.Message, "pool tank") {
		t.Fatalf("%+v", c)
	}
	e.SetSysctl("vfs.zfs.bclone_enabled", "0")
	if c, _ := row(); c.Status != api.DoctorWarn || !strings.Contains(c.Message, "vfs.zfs.bclone_enabled=0") || !strings.Contains(c.Message, "full copies") {
		t.Fatalf("%+v", c)
	}
	e.SetSysctl("vfs.zfs.bclone_enabled", "1")
	if c, _ := row(); c.Status != api.DoctorOK {
		t.Fatalf("%+v", c)
	}
	e.ZFS.SetFeatureState("tank", "block_cloning", "disabled")
	if c, _ := row(); c.Status != api.DoctorWarn || c.Message != "pool tank has not enabled block cloning: every new jail is a full copy of its image (about 50 MB each). 'zpool upgrade tank' enables it; after that, systems older than OpenZFS 2.2 cannot import the pool." {
		t.Fatalf("%+v", c)
	}
	// a ZFS that does not know the feature: nothing is said
	e.ZFS.SetFeatureState("tank", "block_cloning", "")
	if c, ok := row(); ok {
		t.Fatalf("%+v", c)
	}
	if err := e.ZFS.Set(ctx, "tank/jailops", map[string]string{"encryption": "aes-256-gcm"}); err != nil {
		t.Fatal(err)
	}
	if c, _ := row(); c.Status != api.DoctorInfo || !strings.Contains(c.Message, "storage default is encrypted") {
		t.Fatalf("%+v", c)
	}
}
