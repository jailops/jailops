package lifecycle

import (
	"errors"
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

func TestCreateVolumeLayoutOwnerAndMode(t *testing.T) {
	w := newWorld(t)
	req := VolumeRequest{Storage: w.storage(), Project: "alpha", Name: "maildir", UUID: "vol-1", Size: "5G", UID: 5000, GID: 5000, Mode: 0o770}
	plan, err := w.S.CreateVolume(bg, req, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 3 {
		t.Fatalf("plan %v", plan)
	}
	ds := "tank/jailops/projects/alpha/volumes/maildir"
	for k, v := range map[string]string{"mountpoint": "/jailops/projects/alpha/volumes/maildir", "canmount": "on", "devices": "off", "quota": "5G",
		PropManaged: "on", PropKind: KindVolume, PropName: "maildir", PropProject: "alpha"} {
		if got := w.Z.PropOrEmpty(ds, k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	// the parent holds nothing and mounts nowhere
	if w.Z.PropOrEmpty("tank/jailops/projects/alpha/volumes", "canmount") != "off" {
		t.Error("the volumes parent is mountable")
	}
	// owner and mode are set once, through the helper's operation, on the mountpoint
	if got := w.Files.Owners["/jailops/projects/alpha/volumes/maildir"]; got != "5000:5000:0770" {
		t.Fatalf("owner %q", got)
	}
	// a second volume reuses the parent
	req2 := req
	req2.Name, req2.UUID = "second", "vol-2"
	plan, err = w.S.CreateVolume(bg, req2, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.S.CreateVolume(bg, req, nil, false); !api.IsCode(err, api.CodeAlreadyExists) {
		t.Fatalf("a volume made twice: %v", err)
	}
}

func TestCreateVolumeRefusals(t *testing.T) {
	w := newWorld(t)
	base := VolumeRequest{Storage: w.storage(), Project: "alpha", Name: "v", UUID: "u"}
	for name, mut := range map[string]func(*VolumeRequest){
		"bad name":     func(r *VolumeRequest) { r.Name = "a/b" },
		"bad size":     func(r *VolumeRequest) { r.Size = "lots" },
		"negative uid": func(r *VolumeRequest) { r.UID = -1 },
		"setuid bit":   func(r *VolumeRequest) { r.Mode = 0o4755 },
		"sticky bit":   func(r *VolumeRequest) { r.Mode = 0o1777 },
	} {
		r := base
		mut(&r)
		if _, err := w.S.CreateVolume(bg, r, nil, false); !api.IsCode(err, api.CodeInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if ok, _ := w.Z.Exists(bg, VolumeDS(w.storage(), "alpha", "v")); ok {
		t.Fatal("a refused creation left a dataset")
	}
}

func TestCreateVolumeFailureSweep(t *testing.T) {
	sweep(t, newWorld, func(w *world) error {
		_, err := w.S.CreateVolume(bg, VolumeRequest{Storage: w.storage(), Project: "alpha", Name: "v", UUID: "u", UID: 1, GID: 1, Mode: 0o755}, nil, false)
		return err
	})
}

func TestVolumeSnapshotRollbackResizeDelete(t *testing.T) {
	w := newWorld(t)
	w.volume("db")
	st := w.storage()
	w.Z.WriteFile(dbVol, "/a", "one")
	if err := w.S.SnapshotVolume(bg, st, "alpha", "db", "s1", map[string]string{PropComment: "before"}); err != nil {
		t.Fatal(err)
	}
	if err := w.S.SnapshotVolume(bg, st, "alpha", "db", "s1", nil); !api.IsCode(err, api.CodeAlreadyExists) {
		t.Fatalf("a snapshot made twice: %v", err)
	}
	w.Z.WriteFile(dbVol, "/a", "two-two")
	info, err := w.S.VolumeInfo(bg, st, "alpha", "db")
	if err != nil || len(info.Snapshots) != 1 || info.Snapshots[0].Name != "s1" || info.Snapshots[0].Comment != "before" || info.Snapshots[0].Written != int64(len("two-two")) {
		t.Fatalf("%+v %v", info, err)
	}
	if err := w.S.RollbackVolume(bg, st, "alpha", "db", "s1"); err != nil {
		t.Fatal(err)
	}
	if v, _ := w.Z.ReadFile(dbVol, "/a"); v != "one" {
		t.Fatalf("rolled back to %q", v)
	}
	if err := w.S.ResizeVolume(bg, st, "alpha", "db", "7G"); err != nil || w.Z.PropOrEmpty(dbVol, "quota") != "7G" {
		t.Fatalf("resize: %v %q", err, w.Z.PropOrEmpty(dbVol, "quota"))
	}
	if err := w.S.DeleteVolumeSnapshot(bg, st, "alpha", "db", "s1"); err != nil {
		t.Fatal(err)
	}
	if err := w.S.DestroyVolume(bg, st, "alpha", "db"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := w.Z.Exists(bg, dbVol); ok {
		t.Fatal("not destroyed")
	}
	// only a volume of JailOps is destroyed
	w.Z.Create(bg, "tank/jailops/projects/alpha/volumes/foreign", map[string]string{})
	if err := w.S.DestroyVolume(bg, st, "alpha", "foreign"); !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("a dataset that is not a volume of JailOps: %v", err)
	}
}

// A jail whose attached volume cannot be found does not start, and everything else about it still works.
func TestJailWithAMissingVolumeDoesNotStart(t *testing.T) {
	w := newWorld(t)
	w.created("web")
	missing := errors.New("the volume db of this jail does not exist")
	w.S.Cfg.Attached = func(string) ([]Attached, error) { return nil, missing }
	if err := w.S.StartJail(bg, "alpha_web"); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("start: %v", err)
	}
	if w.Jails.Up["alpha_web"] {
		t.Fatal("started without its volume")
	}
	if _, err := w.S.SnapshotJail(bg, "alpha_web", "s", ""); err != nil {
		t.Fatalf("a snapshot of the jail needs no volume: %v", err)
	}
}

func TestCheckVolumes(t *testing.T) {
	w := newWorld(t)
	ok := []api.VolumeAttach{{Name: "maildir", Mount: "/var/mail"}, {Name: "docs", Mount: "/srv/docs", ReadOnly: true}, {Name: "x", Mount: "/devices"},
		{Name: "pg", Mount: "/var/db/postgres"}, {Name: "home", Mount: "/home"}}
	if got, err := w.S.Cfg.CheckVolumes(ok); err != nil || len(got) != 5 {
		t.Fatalf("a path below a directory of the system is fine: %v", err)
	}
	for name, vs := range map[string][]api.VolumeAttach{
		"over /etc":                  {{Name: "a", Mount: "/etc"}},
		"over /var/db":               {{Name: "a", Mount: "/var/db"}},
		"twice":                      {{Name: "a", Mount: "/x"}, {Name: "a", Mount: "/y"}},
		"one path twice":             {{Name: "a", Mount: "/x"}, {Name: "b", Mount: "/x"}},
		"relative":                   {{Name: "a", Mount: "x"}},
		"dotdot":                     {{Name: "a", Mount: "/x/../etc"}},
		"root":                       {{Name: "a", Mount: "/"}},
		"bad name":                   {{Name: "a b", Mount: "/x"}},
		"over /var":                  {{Name: "a", Mount: "/var"}},
		"above /usr/local":           {{Name: "a", Mount: "/usr"}},
		"devfs":                      {{Name: "a", Mount: "/dev/x"}},
		"proc":                       {{Name: "a", Mount: "/proc"}},
		"sys":                        {{Name: "a", Mount: "/sys/kernel"}},
		"the mirror of a Linux jail": {{Name: "a", Mount: "/compat/linux/x"}},
	} {
		if _, err := w.S.Cfg.CheckVolumes(vs); !api.IsCode(err, api.CodeInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestCopyIntoVolume(t *testing.T) {
	w := newWorld(t)
	w.created("web")
	w.volume("fresh")
	w.Files.Dirs["/jailops/projects/alpha/jails/web/root/srv/old"] = true
	if err := w.S.CopyIntoVolume(bg, "alpha_web", "/srv/old", w.storage(), "alpha", "fresh"); err != nil {
		t.Fatal(err)
	}
	copied := false
	for _, l := range w.F.Log {
		copied = copied || strings.HasPrefix(l, "copytree /jailops/projects/alpha/jails/web/root/srv/old /jailops/projects/alpha/volumes/fresh")
	}
	if !copied {
		t.Fatal(w.F.Log)
	}
	// a running jail is refused
	w.running("live")
	wantCode(t, w.S.CopyIntoVolume(bg, "alpha_live", "/srv/old", w.storage(), "alpha", "fresh"), api.CodeBusy)
}

func TestAVolumeMadeForARestoreIsIncompleteUntilCompleted(t *testing.T) {
	w := newWorld(t)
	ds := "tank/jailops/projects/alpha/volumes/restored"
	req := VolumeRequest{Storage: w.storage(), Project: "alpha", Name: "restored", UUID: "vol-r", UID: 5000, GID: 5000, Mode: 0o770, Restoring: true}
	if _, err := w.S.CreateVolume(bg, req, nil, false); err != nil {
		t.Fatal(err)
	}
	if got := w.Z.PropOrEmpty(ds, PropComplete); got != "off" {
		t.Fatalf("a volume made for a restore is %q, want incomplete", got)
	}
	if err := w.S.CompleteVolume(bg, w.storage(), "alpha", "restored"); err != nil {
		t.Fatal(err)
	}
	if got := w.Z.PropOrEmpty(ds, PropComplete); got != "on" {
		t.Fatalf("after CompleteVolume: %q", got)
	}
	// a volume made the usual way carries no such property
	req2 := VolumeRequest{Storage: w.storage(), Project: "alpha", Name: "plain", UUID: "vol-p", Mode: 0o755}
	if _, err := w.S.CreateVolume(bg, req2, nil, false); err != nil {
		t.Fatal(err)
	}
	if got := w.Z.PropOrEmpty("tank/jailops/projects/alpha/volumes/plain", PropComplete); got != "" {
		t.Fatalf("an ordinary volume has %s=%q", PropComplete, got)
	}
	if err := w.S.CompleteVolume(bg, w.storage(), "alpha", "nothing"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("completing a volume that is not there: %v", err)
	}
}

// A box that failed is unmounted by force by the system, and the nullfs mounts on it go with it: jail(8) cannot unmount
// the fstab line then and refuses to remove the jail. The stop gives each target it names a placeholder (a small tmpfs)
// that jail(8) unmounts, and tries again.
func TestStopOfAJailWhoseHostDirectoryMountsAreGone(t *testing.T) {
	w := newWorld(t)
	w.created("web")
	w.Files.Sources["/mnt/box/backups"] = true
	w.attach["alpha_web"] = append(w.attach["alpha_web"], Attached{Name: "box", HostDir: true, Mountpoint: "/mnt/box/backups", Path: "/srv/box"})
	if err := w.S.StartJail(bg, "alpha_web"); err != nil {
		t.Fatal(err)
	}
	target := "/jailops/projects/alpha/jails/web/root/srv/box"
	gone := true
	w.Jails.OnStop = func(name string) error {
		if gone {
			return errors.New("jail: " + name + ": mount.fstab: " + target + ": not a mount point")
		}
		return nil
	}
	w.Exec.Handler = func(argv []string) ([]byte, error) {
		if strings.Join(argv, " ") == "mount -t tmpfs -o size=1m tmpfs "+target {
			gone = false // the placeholder is a mount point: jail(8) can unmount it
		}
		return w.handle(argv)
	}
	if err := w.S.StopJail(bg, "alpha_web"); err != nil {
		t.Fatal(err)
	}
	if w.Jails.IsUp("alpha_web") {
		t.Fatal("the jail still runs")
	}
	placed := 0
	for _, c := range w.Exec.Calls {
		if len(c) > 2 && c[0] == "mount" && c[2] == "tmpfs" {
			placed++
		}
	}
	if placed != 1 {
		t.Fatalf("%d placeholders: %v", placed, w.Exec.Calls)
	}
	last := w.Exec.Calls[len(w.Exec.Calls)-1]
	if strings.Join(last, " ") != "umount -f "+target {
		t.Fatalf("the placeholder was left mounted: %v", w.Exec.Calls)
	}
	// a stop that fails for another reason is not retried with placeholders
	w.Files.Sources["/mnt/box/backups"] = true
	if err := w.S.StartJail(bg, "alpha_web"); err != nil {
		t.Fatal(err)
	}
	w.Jails.OnStop = func(string) error { return errors.New("jail: web: some other failure") }
	before := len(w.Exec.Calls)
	if err := w.S.StopJail(bg, "alpha_web"); err == nil || !strings.Contains(err.Error(), "some other failure") {
		t.Fatalf("%v", err)
	}
	if len(w.Exec.Calls) != before {
		t.Fatal("a placeholder was mounted for an unrelated failure")
	}
}

// jail(8) names one line it cannot unmount at a time. Whatever mount of the fstab is gone below the root of the jail (the
// twin of a volume below compat/linux, the twin of /proc, a volume taken off by hand) gets a placeholder, one after the
// other, and the stop succeeds with every placeholder removed again; a target outside the root is never given one.
func TestStopOfAJailWithLostMounts(t *testing.T) {
	root := "/jailops/projects/alpha/jails/web/root"
	for _, tc := range []struct {
		name  string
		lost  []string // below the root, in the order jail(8) meets them
		wants []string // targets given a placeholder
		fails bool
	}{
		{"the twin of a volume below compat/linux", []string{root + "/compat/linux/srv/vol"}, []string{root + "/compat/linux/srv/vol"}, false},
		{"the twin of /proc", []string{root + "/compat/linux/proc"}, []string{root + "/compat/linux/proc"}, false},
		{"a volume and its twin", []string{root + "/compat/linux/srv/vol", root + "/srv/vol"}, []string{root + "/compat/linux/srv/vol", root + "/srv/vol"}, false},
		{"nothing is lost", nil, nil, false},
		{"a target outside the root", []string{"/jailops/projects/beta/jails/db/root/srv/vol"}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.created("web")
			if err := w.S.StartJail(bg, "alpha_web"); err != nil {
				t.Fatal(err)
			}
			lost := append([]string(nil), tc.lost...)
			w.Jails.OnStop = func(name string) error {
				if len(lost) > 0 {
					return errors.New("jail: " + name + ": mount.fstab: " + lost[0] + ": not a mount point")
				}
				return nil
			}
			w.Exec.Handler = func(argv []string) ([]byte, error) {
				if len(argv) == 7 && argv[0] == "mount" && argv[2] == "tmpfs" && len(lost) > 0 && argv[6] == lost[0] {
					lost = lost[1:] // the placeholder is a mount point: jail(8) can unmount it
				}
				return w.handle(argv)
			}
			err := w.S.StopJail(bg, "alpha_web")
			if tc.fails {
				if err == nil || !strings.Contains(err.Error(), "not a mount point") {
					t.Fatalf("a target outside the root was given a placeholder or the stop passed: %v", err)
				}
				for _, c := range w.Exec.Calls {
					if len(c) > 2 && c[0] == "mount" && c[2] == "tmpfs" {
						t.Fatalf("a placeholder for a target outside the root: %v", c)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if w.Jails.IsUp("alpha_web") {
				t.Fatal("the jail still runs")
			}
			var placed, removed []string
			for _, c := range w.Exec.Calls {
				switch {
				case len(c) == 7 && c[0] == "mount" && c[2] == "tmpfs":
					placed = append(placed, c[6])
				case len(c) == 3 && c[0] == "umount" && c[1] == "-f":
					removed = append(removed, c[2])
				}
			}
			if strings.Join(placed, " ") != strings.Join(tc.wants, " ") {
				t.Fatalf("placeholders %v, want %v", placed, tc.wants)
			}
			if strings.Join(removed, " ") != strings.Join(tc.wants, " ") {
				t.Fatalf("placeholders removed %v, want %v: a placeholder was left mounted", removed, tc.wants)
			}
			// a mount point that was not there (jail(8) had taken the mirror below compat/linux away) was made for the
			// placeholder and the highest directory made on the way is removed again (the fake removes one path, a real
			// remove takes what is below it too)
			for _, d := range []string{root + "/compat", root + "/srv"} {
				if w.Files.Dirs[d] {
					t.Errorf("%s is left behind", d)
				}
			}
		})
	}
	// the same target named again after its placeholder is not given a second one: the stop fails as it did
	w := newWorld(t)
	w.created("web")
	if err := w.S.StartJail(bg, "alpha_web"); err != nil {
		t.Fatal(err)
	}
	target := root + "/srv/vol"
	w.Jails.OnStop = func(name string) error {
		return errors.New("jail: " + name + ": mount.fstab: " + target + ": not a mount point")
	}
	placed := 0
	w.Exec.Handler = func(argv []string) ([]byte, error) {
		if len(argv) == 7 && argv[0] == "mount" && argv[2] == "tmpfs" {
			placed++
		}
		return w.handle(argv)
	}
	if err := w.S.StopJail(bg, "alpha_web"); err == nil {
		t.Fatal("a stop that keeps failing passed")
	}
	if placed != 1 {
		t.Fatalf("%d placeholders for one target", placed)
	}
}

// A jail that has never run gets its volumes bound into its root, so the files the host installs land in them, and
// unbound again before the start mounts them itself.
func TestBindVolumesIntoAStoppedJail(t *testing.T) {
	w := newWorld(t)
	w.created("web")
	w.attachTo("alpha_web", "www", "/usr/local/www", false)
	w.attachTo("alpha_web", "data", "/var/db/web", false)
	if err := w.S.BindVolumes(bg, "alpha_web"); err != nil {
		t.Fatal(err)
	}
	cmds := strings.Join(w.Exec.Commands(), "\n")
	for _, want := range []string{
		"mount -t nullfs -o nosuid /jailops/projects/alpha/volumes/www /jailops/projects/alpha/jails/web/root/usr/local/www",
		"mount -t nullfs -o nosuid /jailops/projects/alpha/volumes/data /jailops/projects/alpha/jails/web/root/var/db/web",
	} {
		if !strings.Contains(cmds, want) {
			t.Errorf("no %q in\n%s", want, cmds)
		}
	}
	if strings.Contains(cmds, "-o ro") {
		t.Errorf("a read-write bind was read-only:\n%s", cmds)
	}
	if err := w.S.UnbindVolumes(bg, "alpha_web"); err != nil {
		t.Fatal(err)
	}
	cmds = strings.Join(w.Exec.Commands(), "\n")
	data := strings.Index(cmds, "umount /jailops/projects/alpha/jails/web/root/var/db/web")
	www := strings.Index(cmds, "umount /jailops/projects/alpha/jails/web/root/usr/local/www")
	if data < 0 || www < 0 || data > www {
		t.Errorf("the volumes are unmounted in reverse order, the last first:\n%s", cmds)
	}
}

// A daemon that dies in the middle of a volume's creation takes the dataset away again at its next start, whatever
// step it died after, and leaves the project's other datasets alone.
func TestCreateVolumeCrashRecovery(t *testing.T) {
	req := VolumeRequest{Project: "alpha", Name: "maildir", UUID: "vol-1", Size: "5G", UID: 5000, GID: 5000, Mode: 0o770}
	w0 := newWorld(t)
	req.Storage = w0.storage()
	steps := len(w0.S.volumePlan(req, nil).Steps)
	for k := 1; k <= steps; k++ {
		w := newWorld(t)
		req.Storage = w.storage()
		crash(t, w, "create-volume/alpha_maildir", w.S.volumePlan(req, nil), k)
		if found, err := w.S.RecoverCreateVolume(bg, req); !found || err != nil {
			t.Fatalf("after %d steps: %v %v", k, found, err)
		}
		// the dataset is gone (the empty volumes parent of the project stays: it is the project's, not the volume's)
		if ok, _ := w.Z.Exists(bg, "tank/jailops/projects/alpha/volumes/maildir"); ok {
			t.Fatalf("crash after %d steps: the dataset of the volume is still there\n%s", k, w.Z.Dump())
		}
		// the name is free again
		if _, err := w.S.CreateVolume(bg, req, nil, false); err != nil {
			t.Fatalf("after the recovery: %v", err)
		}
	}
}

// A remount that cannot be made (the box is back but the mount fails) must not leave the jail on the empty directory of
// its own root: a small read-only tmpfs stands in, and the error is returned so that the watch tries again.
func TestRemountOfAHostDirectoryThatFailsLeavesAPlaceholder(t *testing.T) {
	w := newWorld(t)
	w.created("web")
	w.Files.Sources["/mnt/box/backups"] = true
	a := Attached{Name: "box", HostDir: true, Mountpoint: "/mnt/box/backups", Path: "/srv/box"}
	w.attach["alpha_web"] = append(w.attach["alpha_web"], a)
	target := "/jailops/projects/alpha/jails/web/root/srv/box"
	w.Exec.Handler = func(argv []string) ([]byte, error) {
		if len(argv) > 3 && argv[0] == "mount" && argv[2] == "nullfs" {
			return []byte("mount_nullfs: Device busy"), errors.New("exit 1")
		}
		return w.handle(argv)
	}
	err := w.S.RemountHostDir(bg, "alpha_web", a)
	if err == nil || !strings.Contains(err.Error(), "Device busy") {
		t.Fatalf("a failed mount was not reported: %v", err)
	}
	last := strings.Join(w.Exec.Calls[len(w.Exec.Calls)-1], " ")
	if last != "mount -t tmpfs -o size=1m,ro tmpfs "+target {
		t.Fatalf("no stand-in on the target: %v", w.Exec.Calls)
	}
}

// A Linux jail has the mount of a host directory twice, and a directory that comes back needs both made again: the
// mount below the mirror is a nullfs over a mount that went away as well. The second goes off first and on last.
func TestRemountOfAHostDirectoryOfALinuxJailDoesBothMounts(t *testing.T) {
	w := newWorld(t)
	w.created("web")
	w.Files.Sources["/mnt/box/backups"] = true
	a := Attached{Name: "box", HostDir: true, Mountpoint: "/mnt/box/backups", Path: "/srv/box", ReadOnly: true, Linux: true}
	w.attach["alpha_web"] = append(w.attach["alpha_web"], a)
	root := "/jailops/projects/alpha/jails/web/root"
	if err := w.S.RemountHostDir(bg, "alpha_web", a); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range w.Exec.Commands() {
		if strings.Contains(c, "/srv/box") {
			got = append(got, c)
		}
	}
	want := []string{
		"umount -f " + root + "/compat/linux/srv/box",
		"umount -f " + root + "/srv/box",
		"mount -t nullfs -o ro,nosuid /mnt/box/backups " + root + "/srv/box",
		"mount -t nullfs -o ro,nosuid /mnt/box/backups " + root + "/compat/linux/srv/box",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// the second mount fails: the stand-in is on it, the first stays
	w.Exec.Handler = func(argv []string) ([]byte, error) {
		if len(argv) > 3 && argv[0] == "mount" && argv[2] == "nullfs" && strings.HasSuffix(argv[len(argv)-1], "/compat/linux/srv/box") {
			return []byte("mount_nullfs: Device busy"), errors.New("exit 1")
		}
		return w.handle(argv)
	}
	if err := w.S.RemountHostDir(bg, "alpha_web", a); err == nil {
		t.Fatal("a failed mount was not reported")
	}
	if last := strings.Join(w.Exec.Calls[len(w.Exec.Calls)-1], " "); last != "mount -t tmpfs -o size=1m,ro tmpfs "+root+"/compat/linux/srv/box" {
		t.Fatalf("no stand-in below the mirror: %v", w.Exec.Calls[len(w.Exec.Calls)-1])
	}
}

// A running jail gets a volume mounted at once (read-only when asked) and gives it back; a mount that is in use is
// refused as busy and the volumes this call had already taken off are mounted again.
func TestVolumesOfARunningJail(t *testing.T) {
	w := newWorld(t)
	w.created("web")
	root := "/jailops/projects/alpha/jails/web/root"
	w.volume("www")
	w.volume("data")
	st := w.storage()
	www := Attached{Name: "www", Dataset: VolumeDS(st, "alpha", "www"), Mountpoint: VolumeMP(st, "alpha", "www"), Path: "/usr/local/www", ReadOnly: true}
	data := Attached{Name: "data", Dataset: VolumeDS(st, "alpha", "data"), Mountpoint: VolumeMP(st, "alpha", "data"), Path: "/var/db/web"}
	if err := w.S.MountLive(bg, "alpha_web", []Attached{www, data}); err != nil {
		t.Fatal(err)
	}
	cmds := strings.Join(w.Exec.Commands(), "\n")
	for _, want := range []string{
		"mount -t nullfs -o ro,nosuid /jailops/projects/alpha/volumes/www " + root + "/usr/local/www",
		"mount -t nullfs -o nosuid /jailops/projects/alpha/volumes/data " + root + "/var/db/web",
	} {
		if !strings.Contains(cmds, want) {
			t.Errorf("no %q in\n%s", want, cmds)
		}
	}
	// the second mount of the list is in use: nothing is changed
	w.Exec.Handler = func(argv []string) ([]byte, error) {
		if len(argv) == 2 && argv[0] == "umount" && strings.HasSuffix(argv[1], "/var/db/web") {
			return []byte("umount: " + argv[1] + ": Device busy"), errors.New("exit 1")
		}
		return w.handle(argv)
	}
	before := len(w.Exec.Calls)
	err := w.S.UnmountLive(bg, "alpha_web", []Attached{www, data})
	var busy *VolumeBusyError
	if !errors.As(err, &busy) || busy.Path != "/var/db/web" {
		t.Fatalf("a mount in use is not busy: %v", err)
	}
	after := strings.Join(w.Exec.Commands()[before:], "\n")
	// the first goes, the second is in use, and the first is mounted again (read-only as it was)
	if !strings.Contains(after, "umount "+root+"/usr/local/www") || !strings.Contains(after, "mount -t nullfs -o ro,nosuid /jailops/projects/alpha/volumes/www "+root+"/usr/local/www") {
		t.Fatalf("the volume taken off before the refusal was not mounted again:\n%s", after)
	}
	// a mount that is not there is fine
	w.Exec.Handler = func(argv []string) ([]byte, error) {
		if len(argv) == 2 && argv[0] == "umount" {
			return []byte("umount: " + argv[1] + ": not a file system root directory"), errors.New("exit 1")
		}
		return w.handle(argv)
	}
	if err := w.S.UnmountLive(bg, "alpha_web", []Attached{www}); err != nil {
		t.Fatalf("a mount that is not there: %v", err)
	}
}

// A volume of a Linux jail is mounted twice: in the root and below the compat/linux mirror, which Linux processes
// resolve every path through first. Taking it off goes the other way round, the mount below the mirror first; a mount
// that is in use on either level is busy and the level that was taken off is mounted again.
func TestVolumesOfARunningLinuxJail(t *testing.T) {
	w := newWorld(t)
	w.created("web")
	root := "/jailops/projects/alpha/jails/web/root"
	w.volume("mail")
	st := w.storage()
	mail := Attached{Name: "mail", Dataset: VolumeDS(st, "alpha", "mail"), Mountpoint: VolumeMP(st, "alpha", "mail"), Path: "/srv/mail", ReadOnly: true, Linux: true}
	if err := w.S.MountLive(bg, "alpha_web", []Attached{mail}); err != nil {
		t.Fatal(err)
	}
	mounts := []string{}
	for _, c := range w.Exec.Commands() {
		if strings.HasPrefix(c, "mount -t nullfs") {
			mounts = append(mounts, c)
		}
	}
	want := []string{
		"mount -t nullfs -o ro,nosuid /jailops/projects/alpha/volumes/mail " + root + "/srv/mail",
		"mount -t nullfs -o ro,nosuid /jailops/projects/alpha/volumes/mail " + root + "/compat/linux/srv/mail",
	}
	if strings.Join(mounts, "\n") != strings.Join(want, "\n") {
		t.Fatalf("mounts:\n%s\nwant:\n%s", strings.Join(mounts, "\n"), strings.Join(want, "\n"))
	}

	before := len(w.Exec.Calls)
	if err := w.S.UnmountLive(bg, "alpha_web", []Attached{mail}); err != nil {
		t.Fatal(err)
	}
	var umounts []string
	for _, c := range w.Exec.Commands()[before:] {
		if strings.HasPrefix(c, "umount ") {
			umounts = append(umounts, c)
		}
	}
	if strings.Join(umounts, "\n") != "umount "+root+"/compat/linux/srv/mail\numount "+root+"/srv/mail" {
		t.Fatalf("the mount below the mirror does not come off first: %v", umounts)
	}

	// the mount in the root is in use after the one below the mirror came off: the latter is mounted again
	w.Exec.Handler = func(argv []string) ([]byte, error) {
		if len(argv) == 2 && argv[0] == "umount" && argv[1] == root+"/srv/mail" {
			return []byte("umount: " + argv[1] + ": Device busy"), errors.New("exit 1")
		}
		return w.handle(argv)
	}
	before = len(w.Exec.Calls)
	err := w.S.UnmountLive(bg, "alpha_web", []Attached{mail})
	var busy *VolumeBusyError
	if !errors.As(err, &busy) || busy.Path != "/srv/mail" {
		t.Fatalf("a mount in use is not busy: %v", err)
	}
	after := strings.Join(w.Exec.Commands()[before:], "\n")
	if !strings.Contains(after, "mount -t nullfs -o ro,nosuid /jailops/projects/alpha/volumes/mail "+root+"/compat/linux/srv/mail") {
		t.Fatalf("the mount below the mirror was not made again:\n%s", after)
	}

	// a FreeBSD jail has the one mount only
	fb := mail
	fb.Linux = false
	before = len(w.Exec.Calls)
	w.Exec.Handler = nil
	if err := w.S.MountLive(bg, "alpha_web", []Attached{fb}); err != nil {
		t.Fatal(err)
	}
	for _, c := range w.Exec.Commands()[before:] {
		if strings.Contains(c, "compat/linux") {
			t.Fatalf("a FreeBSD jail mounted below compat/linux: %s", c)
		}
	}
}

// A Linux jail that started under an older version has no mount below the mirror: the helper refuses that umount with
// "is not a mount point", which is nothing to take off, not a failed detach.
func TestDetachOfAMountThatIsGoneIsNotAnError(t *testing.T) {
	w := newWorld(t)
	w.created("web")
	root := "/jailops/projects/alpha/jails/web/root"
	w.volume("mail")
	st := w.storage()
	mail := Attached{Name: "mail", Dataset: VolumeDS(st, "alpha", "mail"), Mountpoint: VolumeMP(st, "alpha", "mail"), Path: "/srv/mail", Linux: true}
	w.Exec.Handler = func(argv []string) ([]byte, error) {
		if len(argv) == 2 && argv[0] == "umount" && argv[1] == root+"/compat/linux/srv/mail" {
			return nil, errors.New("umount: " + argv[1] + " is not a mount point")
		}
		return w.handle(argv)
	}
	if err := w.S.UnmountLive(bg, "alpha_web", []Attached{mail}); err != nil {
		t.Fatalf("detach of a mount that is not there: %v", err)
	}
	umounted := false
	for _, c := range w.Exec.Commands() {
		umounted = umounted || c == "umount "+root+"/srv/mail"
	}
	if !umounted {
		t.Fatal("the mount in the root was not taken off")
	}
}
