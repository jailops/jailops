package service_test

import (
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
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
