package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"jailops.net/jailops/internal/lifecycle/fakes"
	"jailops.net/jailops/internal/lifecycle/fault"
	"jailops.net/jailops/internal/lifecycle/zfsmodel"
	"jailops.net/jailops/internal/plan"
	"jailops.net/jailops/sdk/api"
)

var bg = context.Background()

// fakeImages serves generations that the world creates directly in the model.
type fakeImages struct {
	z        *zfsmodel.Model
	newest   map[string]int // the newest generation of an image: what its bare name resolves to
	gens     map[string]api.ImageGeneration
	collects int
	peeks    int
	ensures  int
	// roots records the storage roots the lifecycle asked about.
	roots []string
}

func (f *fakeImages) Resolve(ctx context.Context, root, ref string) (api.ImageGeneration, error) {
	f.roots = append(f.roots, root)
	if !strings.Contains(ref, "#") {
		n, ok := f.newest[ref]
		if !ok {
			return api.ImageGeneration{}, api.Errorf(api.CodeNotFound, "no image %s", ref)
		}
		ref = fmt.Sprintf("%s#g%d", ref, n)
	}
	g, ok := f.gens[ref]
	if !ok {
		return g, api.Errorf(api.CodeNotFound, "no generation %s", ref)
	}
	return g, nil
}
func (f *fakeImages) Ensure(ctx context.Context, root, ref string) (api.ImageGeneration, error) {
	f.ensures++
	return f.Resolve(ctx, root, ref)
}
func (f *fakeImages) Peek(ctx context.Context, ref string) (api.ImageGeneration, error) {
	f.peeks++
	return f.Resolve(ctx, "", ref)
}
func (f *fakeImages) Collect(context.Context) ([]string, error) { f.collects++; return nil, nil }

type world struct {
	t      *testing.T
	F      *fault.Injector
	Z      *zfsmodel.Model
	Files  *fakes.Files
	Exec   *fakes.Exec
	Jails  *fakes.Jails
	Hooks  *fakes.Hooks
	Images *fakeImages
	J      *plan.MemJournal
	S      *Service
	now    time.Time
	// merge simulates etcupdate: it writes into the jail's etc dataset.
	mergeOut string
	mergeErr error
	// pkgVersion is the version `pkg query` reports for FreeBSD-runtime; pkgFail fails a pkg sub command.
	pkgVersion string
	pkgFail    func(sub string, argv []string) error
	// installed and repo are the FreeBSD-* packages of the jail and of the repository catalogue (name to version);
	// a pkg upgrade or install brings the installed ones to the version of the repository.
	installed, repo map[string]string
	// repoOSVersion is the FreeBSD_version the repository's packages were built for (0: any): pkg update refuses it when
	// OSVERSION is older.
	repoOSVersion int
	// pkgDryRunOut, when set, is what pkg's dry runs print instead of pkgDryRun's answer.
	pkgDryRunOut string
	// suspected, when set, is what the helper's detection finds in the root (pkg -r on it is refused).
	suspected string
	started   []string // generation of the root at each jail start
	// attach is what Config.Attached answers: the volumes attached to each jail key.
	attach map[string][]Attached
}

func newWorld(t *testing.T) *world {
	f := &fault.Injector{}
	w := &world{t: t, F: f, Z: zfsmodel.New(f, "tank"), Files: fakes.NewFiles(f), Exec: &fakes.Exec{F: f},
		Jails: fakes.NewJails(f), Hooks: &fakes.Hooks{F: f}, J: &plan.MemJournal{}, now: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)}
	for _, d := range []string{"tank/jailops", "tank/jailops/images", "tank/jailops/images/fb", "tank/jailops/images/alp", "tank/jailops/images/fc", "tank/jailops/images/fd", "tank/jailops/images/fe", "tank/jailops/images/ff", "tank/jailops/projects", "tank/jailops/projects/alpha", "tank/jailops/projects/alpha/jails"} {
		if err := w.Z.Create(bg, d, map[string]string{"jailops:managed": "on"}); err != nil {
			t.Fatal(err)
		}
	}
	w.Images = &fakeImages{z: w.Z, newest: map[string]int{}, gens: map[string]api.ImageGeneration{}}
	w.addGen("fb", 1, "freebsd", "15.0-RELEASE")
	w.addGen("fb", 2, "freebsd", "15.0-RELEASE")
	w.addGen("alp", 1, "linux", "3.22")
	// images of other releases for the moves: fc is newer than fb, fd the same release, fe older
	w.addGen("fc", 1, "freebsd", "15.1-RELEASE")
	w.addGen("fc", 2, "freebsd", "15.1-RELEASE")
	w.addGen("fd", 1, "freebsd", "15.0-RELEASE")
	w.addGen("fe", 1, "freebsd", "14.2-RELEASE")
	w.addGen("ff", 1, "freebsd", "16.0-RELEASE")
	w.Images.newest["fc"], w.Images.newest["fd"], w.Images.newest["fe"] = 2, 1, 1
	w.Images.newest["ff"] = 1
	w.Images.newest["fb"] = 1
	w.Images.newest["alp"] = 1
	w.Exec.Handler = w.handle
	w.pkgVersion = "15.0p3"
	w.installed = map[string]string{"FreeBSD-runtime": "15.0p2", "FreeBSD-utilities": "15.0"}
	w.repo = map[string]string{"FreeBSD-runtime": "15.0p3", "FreeBSD-utilities": "15.0", "FreeBSD-ssh": "15.0", "FreeBSD-ee": "15.0"}
	w.Files.Trees["/jailops/projects/alpha/jails/web/root/etc"] = []string{"rc.conf"}
	w.attach = map[string][]Attached{}
	w.S = &Service{
		Cfg: Config{Root: "tank/jailops", MountBase: "/jailops", RootOldRetention: 24 * time.Hour,
			Now:      func() time.Time { return w.now },
			Attached: func(key string) ([]Attached, error) { return w.attach[key], nil }},
		ZFS: w.Z, Exec: w.Exec, Jails: w.Jails, Files: w.Files, Hooks: w.Hooks, Images: w.Images,
		Executor: &plan.Executor{Journal: w.J},
	}
	w.Jails.OnStart = func(name string) error {
		w.started = append(w.started, w.Z.PropOrEmpty(w.S.Cfg.at(name).rootDS(), PropGen))
		return nil
	}
	return w
}

func (w *world) addGen(image string, n int, os, rel string) {
	ds := fmt.Sprintf("tank/jailops/images/%s/g%d", image, n)
	err := w.Z.Create(bg, ds, map[string]string{"jailops:managed": "on", PropOS: os, PropOSRelease: rel, "jailops:manifest": fmt.Sprintf("sha256:m%d", n), "mountpoint": "/" + strings.TrimPrefix(ds, "tank/")})
	if err != nil {
		w.t.Fatal(err)
	}
	w.Z.WriteFile(ds, "/etc/version", fmt.Sprintf("g%d", n))
	if err := w.Z.Snapshot(bg, nil, ds+"@ro"); err != nil {
		w.t.Fatal(err)
	}
	name := fmt.Sprintf("%s#g%d", image, n)
	w.Images.gens[name] = api.ImageGeneration{Number: n, Name: name, Dataset: ds, Manifest: fmt.Sprintf("sha256:m%d", n)}
}

// ImageSpec answers the spec of an image from its first generation: the release and OS the generation datasets carry.
func (f *fakeImages) ImageSpec(ctx context.Context, image string) (api.ImageSpec, error) {
	g, ok := f.gens[image+"#g1"]
	if !ok {
		return api.ImageSpec{}, api.Errorf(api.CodeNotFound, "no image %s", image)
	}
	os, _ := f.z.Get(ctx, g.Dataset, PropOS)
	rel, _ := f.z.Get(ctx, g.Dataset, PropOSRelease)
	src := api.ImageSource{Type: "pkgbase"}
	if os == string(api.OSLinux) {
		src.Type = "minirootfs"
	}
	return api.ImageSpec{OS: api.OS(os), Release: rel, Arch: "amd64", Source: src}, nil
}

func (w *world) handle(argv []string) ([]byte, error) {
	if argv[0] == "pkg" && len(argv) > 2 && argv[1] == "-r" && argv[2] == rootMPt && w.suspected != "" {
		// the helper's detection refuses a root that holds what an attack on host-side pkg plants
		return []byte("forbidden: suspected_compromised: " + w.suspected), errors.New("forbidden: suspected_compromised: " + w.suspected)
	}
	if argv[0] == "pkg" {
		// pkg -r <root> -o ... <sub> ...: the sub command is what follows the last option
		sub, rest := "", []string(nil)
		for i := 1; i < len(argv); i++ {
			if argv[i] == "-r" || argv[i] == "-o" || argv[i] == "-j" {
				i++
				continue
			}
			sub, rest = argv[i], argv[i+1:]
			break
		}
		dry := slices.Contains(rest, "-n")
		// pkgFail fails what changes something; pkg's dry runs answer from installed and repo
		if w.pkgFail != nil && !dry {
			if err := w.pkgFail(sub, argv); err != nil {
				return []byte("pkg: " + err.Error()), err
			}
		}
		list := func(m map[string]string, size int) []byte {
			var b strings.Builder
			for n, v := range m {
				fmt.Fprintf(&b, "%s %s %d\n", n, v, size)
			}
			return []byte(b.String())
		}
		switch {
		case sub == "update" && w.repoOSVersion > 0 && slices.ContainsFunc(argv, func(a string) bool {
			v, ok := strings.CutPrefix(a, "OSVERSION=")
			return ok && v < fmt.Sprint(w.repoOSVersion)
		}):
			return []byte("Newer FreeBSD version for package FreeBSD-runtime:\nIgnore the mismatch and continue? [y/N]: pkg: repository jailops contains packages for wrong OS version: FreeBSD:15:amd64\n"), errors.New("exit status 1")
		case dry && (sub == "upgrade" || sub == "install") && w.pkgDryRunOut != "":
			return []byte(w.pkgDryRunOut), nil
		case dry && (sub == "upgrade" || sub == "install"):
			return w.pkgDryRun(sub, rest), nil
		}
		switch sub {
		case "rquery":
			return list(w.repo, 2<<20), nil
		case "query":
			if len(rest) == 2 && rest[0] == "-a" && rest[1] == "%n %Fp %Fs" { // the files of the packages, for an audit
				return []byte("FreeBSD-runtime /bin/sh 1$aa\nFreeBSD-runtime /etc/rc 1$bb\nnginx /usr/local/sbin/nginx 1$cc\n"), nil
			}
			if len(rest) == 2 && rest[1] == "FreeBSD-runtime" {
				return []byte("FreeBSD-runtime " + w.pkgVersion + "\n"), nil
			}
			return list(w.installed, 1<<20), nil
		case "upgrade", "install":
			if sub == "upgrade" {
				for n := range w.installed {
					w.installed[n] = w.repo[n]
				}
			}
			for _, n := range rest {
				if v, ok := w.repo[n]; ok && strings.HasPrefix(n, "FreeBSD-") {
					w.installed[n] = v
				}
			}
		}
		return nil, nil
	}
	if argv[0] != "etcupdate" || argv[1] == "extract" {
		return nil, nil
	}
	// etcupdate -d <work> -t <new-etc.tar> -D <jail root> -L <log>
	args := map[string]string{}
	for i := 1; i+1 < len(argv); i += 2 {
		args[argv[i]] = argv[i+1]
	}
	jail := path.Base(path.Dir(args["-D"]))
	w.Z.WriteFile(fmt.Sprintf("tank/jailops/projects/alpha/jails/%s/etc", jail), "/etc/merged", "from "+args["-t"])
	return []byte(w.mergeOut), w.mergeErr
}

// pkgDryRun answers `pkg upgrade -n` and `pkg install -n` as pkg 2.x does, from installed and repo.
func (w *world) pkgDryRun(sub string, rest []string) []byte {
	var up, re, nw []string
	force := slices.Contains(rest, "-f")
	if sub == "upgrade" {
		for n, v := range w.installed {
			rv, ok := w.repo[n]
			switch {
			case !ok || !strings.HasPrefix(n, "FreeBSD-"):
			case rv != v:
				up = append(up, fmt.Sprintf("\t%s: %s -> %s\n", n, v, rv))
			case force:
				re = append(re, fmt.Sprintf("\t%s-%s\n", n, v))
			}
		}
	} else {
		for _, n := range rest {
			if v, ok := w.repo[n]; ok && strings.HasPrefix(n, "FreeBSD-") && w.installed[n] == "" {
				nw = append(nw, fmt.Sprintf("\t%s: %s [jailops]\n", n, v))
			}
		}
	}
	if len(up)+len(re)+len(nw) == 0 {
		if sub == "install" {
			return []byte("Checking integrity... done (0 conflicting)\nThe most recent versions of packages are already installed\n")
		}
		return []byte(fmt.Sprintf("Checking for upgrades (%d candidates): .......... done\nYour packages are up to date.\n", len(w.installed)))
	}
	var b strings.Builder
	b.WriteString("Checking integrity... done (0 conflicting)\nThe following packages will be affected (of 0 checked):\n\n")
	for _, sec := range []struct {
		head  string
		items []string
	}{{"New packages to be INSTALLED:", nw}, {"Installed packages to be UPGRADED:", up}, {"Installed packages to be REINSTALLED:", re}} {
		if len(sec.items) == 0 {
			continue
		}
		sort.Strings(sec.items)
		b.WriteString(sec.head + "\n" + strings.Join(sec.items, "") + "\n")
	}
	b.WriteString("Number of packages to be upgraded: 1\n\nThe process will require 2 MiB more space.\n1 MiB to be downloaded.\n")
	return []byte(b.String())
}

// dbVol is the volume db of the project alpha, which the test jails attach at /srv/db.
const dbVol = "tank/jailops/projects/alpha/volumes/db"

func spec(image string) api.JailSpec {
	return api.JailSpec{Image: image, Update: api.UpdatePolicy{Healthcheck: "/bin/true"},
		Resources: api.Resources{Disk: "10G"},
		Volumes:   []api.VolumeAttach{{Name: "db", Mount: "/srv/db"}}}
}

func (w *world) jail(name string) api.Jail {
	return api.Jail{Metadata: api.ObjectMeta{Name: name, Project: "alpha", UUID: "uuid-" + name}, Spec: spec("fb")}
}

func (w *world) storage() Storage { return Storage{Root: "tank/jailops", MountBase: "/jailops"} }

// volume makes the volume of the project alpha (owner 5000:5000, mode 0770) when it is not there yet.
func (w *world) volume(name string) {
	w.t.Helper()
	if ok, _ := w.Z.Exists(bg, VolumeDS(w.storage(), "alpha", name)); ok {
		return
	}
	req := VolumeRequest{Storage: w.storage(), Project: "alpha", Name: name, UUID: "vol-" + name, Size: "5G", UID: 5000, GID: 5000, Mode: 0o770}
	if _, err := w.S.CreateVolume(bg, req, nil, false); err != nil {
		w.t.Fatal(err)
	}
}

// attachTo attaches a volume to the jail key at a path, as the service's answer to Config.Attached.
func (w *world) attachTo(key, volume, mount string, ro bool) {
	w.t.Helper()
	w.volume(volume)
	st := w.storage()
	w.attach[key] = append(w.attach[key], Attached{Dataset: VolumeDS(st, "alpha", volume), Mountpoint: VolumeMP(st, "alpha", volume), Path: mount, ReadOnly: ro})
}

// created makes a stopped jail named web (normalised: nothing mounted below the container). The jail attaches the
// volume db at /srv/db.
func (w *world) created(name string) {
	w.t.Helper()
	w.attachTo("alpha_"+name, "db", "/srv/db", false)
	if _, err := w.S.CreateJail(bg, w.jail(name), false); err != nil {
		w.t.Fatal(err)
	}
	if err := w.S.StopJail(bg, "alpha_"+name); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) running(name string) {
	w.t.Helper()
	w.created(name)
	if err := w.S.StartJail(bg, "alpha_"+name); err != nil {
		w.t.Fatal(err)
	}
}

// state captures the pool, the jail runtime and the files inside datasets
// that still exist; a failed operation must restore all of it.
func (w *world) state() string {
	var fs []string
	for _, p := range w.Files.Paths() {
		if strings.HasSuffix(p, ".previous") || !strings.HasPrefix(p, "/jailops/projects/alpha/jails/") {
			continue
		}
		jail := strings.Split(strings.TrimPrefix(p, "/jailops/projects/alpha/jails/"), "/")[0]
		if ok, _ := w.Z.Exists(bg, "tank/jailops/projects/alpha/jails/"+jail); !ok {
			continue
		}
		b, _ := w.Files.ReadFile(bg, p)
		fs = append(fs, p+"="+string(b))
	}
	var up []string
	for n := range w.Jails.Up {
		up = append(up, n)
	}
	// canmount=on is what a dataset has by default: a plan that sets it back has restored it
	return strings.NewReplacer(",canmount=on", "", "props{canmount=on,", "props{", "props{canmount=on}", "props{}").Replace(w.Z.Dump()) + "\nFILES\n" + strings.Join(fs, "\n") + fmt.Sprintf("\nUP %v", up)
}

// sweep fails each mutating step of op in turn and requires the state to be
// restored exactly. setup builds the same fresh world every time.
func sweep(t *testing.T, setup func(t *testing.T) *world, op func(w *world) error) int {
	t.Helper()
	w := setup(t)
	w.F.Reset(0)
	if err := op(w); err != nil {
		t.Fatalf("baseline run failed: %v", err)
	}
	n := w.F.Count()
	if n == 0 {
		t.Fatal("no mutating operations")
	}
	failed := 0
	for k := 1; k <= n; k++ {
		w := setup(t)
		before := w.state()
		w.F.Reset(k)
		err := op(w)
		if err == nil {
			continue // the failure hit best-effort cleanup that is reported as a warning
		}
		failed++
		if !errors.Is(err, fault.ErrInjected) {
			t.Fatalf("op %d (%s): unexpected error %v", k, w.F.Log[k-1], err)
		}
		if after := w.state(); after != before {
			t.Fatalf("failing op %d/%d (%s) left the pool changed\nerr: %v\n--- before\n%s\n--- after\n%s", k, n, w.F.Log[k-1], err, before, after)
		}
		if len(w.J.Recs) != 0 {
			t.Fatalf("op %d: journal not cleaned: %v", k, w.J.Recs)
		}
	}
	if failed == 0 {
		t.Fatalf("none of %d injected failures surfaced as an error", n)
	}
	t.Logf("%d mutating operations, %d injected failures surfaced as errors; the rest hit best-effort cleanup", n, failed)
	return n
}

func wantCode(t *testing.T, err error, code api.Code) {
	t.Helper()
	if !api.IsCode(err, code) {
		t.Fatalf("want code %s, got %v", code, err)
	}
}

// crash simulates a daemon crash while step k (1-based) was in flight: steps
// 1..k have run to completion but the journal says step k was not finished.
func crash(t *testing.T, w *world, id string, p *plan.Plan, k int) {
	t.Helper()
	for i := 0; i < k; i++ {
		if err := p.Steps[i].Do(bg); err != nil {
			t.Fatalf("setup step %s: %v", p.Steps[i].Name, err)
		}
	}
	w.J.Put(bg, plan.Record{ID: id, Plan: p.Name, Steps: p.Names(), Begun: k, Done: k - 1, State: plan.StateRunning})
}

// jc and jroot are the container dataset and its mountpoint of the jail web of project alpha.
const (
	jc    = "tank/jailops/projects/alpha/jails/web"
	jroot = "/jailops/projects/alpha/jails/web"
)

func runningSetup(t *testing.T) *world { w := newWorld(t); w.running("web"); return w }

func upgrade(w *world, req api.JailUpgrade) (UpgradeResult, error) {
	return w.S.UpgradeJail(bg, "alpha_web", UpgradeRequest{JailUpgrade: req, Stamp: "100"})
}

// thickCreated makes a stopped jail named web whose base pkg installed (it has a package database).
func (w *world) thickCreated() {
	w.t.Helper()
	w.created("web")
	w.Files.Dirs[jroot+"/root/var/db/pkg"] = true
}

// thickRunning is a running jail named web with a file below /home.
func thickRunning(t *testing.T) *world {
	w := newWorld(t)
	w.thickCreated()
	if err := w.S.StartJail(bg, "alpha_web"); err != nil {
		t.Fatal(err)
	}
	w.Files.Trees[jroot+"/root/home"] = []string{"user/notes"}
	return w
}
