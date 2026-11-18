package reconcile_test

import (
	"errors"
	"strings"
	"testing"

	"jailops.net/jailops/internal/reconcile"
)

// A running jail whose fstab holds a mount that the host does not show is drifted, and says how to repair it: the
// jail started before its fstab gained the mount, or the mount was taken off by hand.
func TestRunningJailMissingAMountIsDrifted(t *testing.T) {
	w := newWorld(t, reconcile.Config{})
	j := jailDesired("lin")
	j.Mounts = []string{"/jails/lin/root/dev", "/jails/lin/root/compat/linux/proc", "/jails/lin/root/compat/linux/srv/vol"}
	w.des = reconcile.Desired{Networks: []reconcile.NetworkDesired{lan()}, Jails: []reconcile.JailDesired{j}}
	w.R.Pass(bg)
	_ = w.Jails.Create(bg, "/etc/jailops/jails/lin/jail.conf", "lin")
	w.mounts = []string{"/", "/jails/lin/root/dev"}
	rep := w.R.Pass(bg)
	got := strings.Join(rep.Drift["lin"], ";")
	for _, want := range []string{
		"mount /jails/lin/root/compat/linux/proc is missing; restart the jail to mount it",
		"mount /jails/lin/root/compat/linux/srv/vol is missing; restart the jail to mount it",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in %q", want, got)
		}
	}
	if strings.Contains(got, "root/dev is missing") {
		t.Errorf("a mount that is there is reported: %q", got)
	}
	// every mount present: the mount drift is gone (the epair drift of this fixture stays)
	w.mounts = append(w.mounts, j.Mounts...)
	if got := strings.Join(w.R.Pass(bg).Drift["lin"], ";"); strings.Contains(got, "mount ") {
		t.Errorf("%q", got)
	}
}

// A mount list that cannot be read says nothing about the mounts, and a stopped jail has none to miss.
func TestMountsUnreadableOrJailStopped(t *testing.T) {
	w := newWorld(t, reconcile.Config{})
	j := jailDesired("lin")
	j.Mounts = []string{"/jails/lin/root/srv/vol"}
	w.des = reconcile.Desired{Networks: []reconcile.NetworkDesired{lan()}, Jails: []reconcile.JailDesired{j}}
	w.R.Pass(bg)
	if got := strings.Join(w.R.Pass(bg).Drift["lin"], ";"); strings.Contains(got, "mount ") {
		t.Errorf("a stopped jail: %q", got)
	}
	_ = w.Jails.Create(bg, "/etc/jailops/jails/lin/jail.conf", "lin")
	w.mountsErr = errors.New("mount: not available")
	if got := strings.Join(w.R.Pass(bg).Drift["lin"], ";"); strings.Contains(got, "mount ") {
		t.Errorf("an unreadable list: %q", got)
	}
}
