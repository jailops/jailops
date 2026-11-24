package service_test

import (
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/service"
	"jailops.net/jailops/sdk/api"
)

// A read-only host-directory volume, once a global administrator has attached it, may not be turned writable or moved by
// a project administrator: either would grant more of the host's files than the global administrator allowed. (audit e/H3)
func TestHostDirVolumeIsWidenedByAGlobalAdministratorOnly(t *testing.T) {
	e, s := newEnv(t)
	admin := as(globalAdmin, dtest.SeedProject)
	proj := as(alphaOnly, dtest.SeedProject) // a project administrator, not a global one
	newBoxVolume(t, e, s, api.VolumeSpec{})
	op, err := s.CreateJail(admin, dtest.WebJail("backup"))
	mustOp(t, s, op, err)
	op, err = s.AttachVolume(admin, "backup", api.VolumeAttachRequest{Volume: "box", Mount: "/srv/backups", ReadOnly: true})
	mustOp(t, s, op, err)

	// turning the host directory writable is refused for a project administrator
	op, err = s.ConfigureJail(proj, "backup", api.ConfigChange{Set: map[string]string{"volumes.box.read_only": "false"}}, false)
	failedWith(t, s, op, err, api.CodeForbidden)
	// so is moving where it is mounted
	op, err = s.ConfigureJail(proj, "backup", api.ConfigChange{Set: map[string]string{"volumes.box.mount": "/srv/elsewhere"}}, false)
	failedWith(t, s, op, err, api.CodeForbidden)
	// a global administrator may
	op, err = s.ConfigureJail(admin, "backup", api.ConfigChange{Set: map[string]string{"volumes.box.read_only": "false"}}, false)
	mustOp(t, s, op, err)
}

// bpf and dri reach past the project boundary, so granting them needs a global administrator; the jail-confined devices
// stay a project administrator's to grant. (audit e/M4)
func TestSensitiveDevicesNeedAGlobalAdministrator(t *testing.T) {
	_, s := newEnv(t)
	admin := as(globalAdmin, dtest.SeedProject)
	proj := as(alphaOnly, dtest.SeedProject)
	op, err := s.CreateJail(admin, dtest.WebJail("web"))
	mustOp(t, s, op, err)

	// a jail-confined device is fine for a project administrator
	op, err = s.ConfigureJail(proj, "web", api.ConfigChange{Set: map[string]string{"devfs.vpn.device": "tun"}}, false)
	mustOp(t, s, op, err)
	// bpf (the frames on the jail's link) is not
	op, err = s.ConfigureJail(proj, "web", api.ConfigChange{Set: map[string]string{"devfs.sniff.device": "bpf"}}, false)
	failedWith(t, s, op, err, api.CodeForbidden)
	// nor is dri (the host's shared GPU)
	op, err = s.ConfigureJail(proj, "web", api.ConfigChange{Set: map[string]string{"devfs.gpu.device": "dri"}}, false)
	failedWith(t, s, op, err, api.CodeForbidden)
	// a global administrator may grant bpf
	op, err = s.ConfigureJail(admin, "web", api.ConfigChange{Set: map[string]string{"devfs.sniff.device": "bpf"}}, false)
	mustOp(t, s, op, err)
}

// Every attachment of a host directory is held on its own, by the directory it carries: a reordered list keeps what the
// jail had, and a second attachment of the same directory (by another volume of it) that moves it or makes it writable
// needs a global administrator like the first.
func TestHostDirAdmissionHoldsEveryAttachment(t *testing.T) {
	e, s := newEnv(t)
	proj := as(alphaOnly, dtest.SeedProject)
	newBoxVolume(t, e, s, api.VolumeSpec{})
	newVolume(t, s, as(globalAdmin, dtest.SeedProject), "box2", api.VolumeSpec{Path: boxDir})
	newVolume(t, s, as(globalAdmin, dtest.SeedProject), "plain", api.VolumeSpec{})
	was := []api.VolumeAttach{
		{Name: "plain", Mount: "/srv/data"},
		{Name: "box", Mount: "/srv/backups", ReadOnly: true},
		{Name: "box2", Mount: "/srv/rw"},
	}
	admit := func(now ...api.VolumeAttach) error {
		return service.HostDirAdmission(s, proj, dtest.SeedProject, now, was)
	}
	// the same attachments in another order, and one made narrower, stay
	if err := admit(was[2], was[1], was[0]); err != nil {
		t.Fatalf("reordered: %v", err)
	}
	if err := admit(was[1], api.VolumeAttach{Name: "box2", Mount: "/srv/rw", ReadOnly: true}); err != nil {
		t.Fatalf("narrowed: %v", err)
	}
	// the other volume of the same directory, where and as the jail had it, stays
	if err := admit(api.VolumeAttach{Name: "box2", Mount: "/srv/backups", ReadOnly: true}, api.VolumeAttach{Name: "box", Mount: "/srv/rw"}); err != nil {
		t.Fatalf("same directory, same mount: %v", err)
	}
	for name, now := range map[string][]api.VolumeAttach{
		"the read-only one made writable":      {{Name: "box", Mount: "/srv/backups"}, was[2]},
		"a second attachment that is writable": {was[1], {Name: "box2", Mount: "/srv/backups"}},
		"a second attachment on a new mount":   {was[1], was[2], {Name: "box", Mount: "/srv/third", ReadOnly: true}},
		"the writable one moved":               {was[1], {Name: "box2", Mount: "/srv/moved"}},
	} {
		err := admit(now...)
		if err == nil {
			t.Errorf("%s: admitted", name)
			continue
		}
		wantCode(t, err, api.CodeForbidden)
	}
}
