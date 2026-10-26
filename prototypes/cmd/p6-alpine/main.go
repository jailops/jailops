// Prototype 6: Alpine minirootfs in a Linuxulator jail on FreeBSD 15.
//
// HONESTY: this program was written on Linux without access to FreeBSD. It has never run.
// Every FreeBSD behaviour it assumes is marked UNVERIFIED: and settled (either way) by the
// verdict line it prints. Run with --dry-run first.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"jailops.net/jailops/prototypes/internal/alpine"
	"jailops.net/jailops/prototypes/internal/harness"
)

var (
	alpineURL, alpineSHA, alpineTar, alpineVer, arch, dns, mountBase string
	ruleset                                                          int
	fails                                                            alpine.Failures
)

func main() {
	harness.Exit(harness.Spec{
		N:       6,
		Name:    "alpine",
		Summary: "Alpine minirootfs in a Linuxulator jail: apk, sshd, nginx, epair address survival, rollback.",
		Plan: []string{
			"create datasets <pool>/jailops-proto-6 and .../alpine (mountpoint under --mount-base, default /jop-proto-6)",
			"download Alpine minirootfs with Go net/http (or use --alpine-tarball), verify sha256, extract with tar",
			"kldload linux64 if missing; load devfs ruleset --devfs-ruleset only if that number is unused (devfs rule showsets)",
			"mount devfs(ruleset), fdescfs, tmpfs /dev/shm, linprocfs, linsysfs under the dataset's mountpoint only",
			"phase 1: start a non-vnet bootstrap jail jop6alpine sharing the host network (ip4/ip6 inherit) only to run `apk update` and `apk add openssh nginx`; remove it",
			"snapshot @installed; create bridge jopbr6 and epair jop6h/jop6j; start vnet jail jop6alpine; `ifconfig jop6j vnet jop6alpine`",
			"test whether addresses set on jop6j in the host survive the vnet move; test several ways to run ifconfig for the jail from the host",
			"start sshd and nginx directly (no OpenRC), HTTP GET from the host, check sshd banner",
			"snapshot @good, break (rm -rf <root>/lib), show apk fails, stop jail, unmount, zfs rollback, remount, show it works again",
			"private jail.conf in /var/tmp/jop-proto-6; /etc/jail.conf, /etc/devfs.rules and pf.conf are never read or written",
			"--cleanup: remove jail, mounts, jop6h, jopbr6, our devfs ruleset (only if we loaded it) and the dataset tree",
		},
		Flags: func(e *harness.Env, fs *flag.FlagSet) {
			fs.StringVar(&alpineURL, "alpine-url", "", "minirootfs tarball URL (needs --alpine-sha256)")
			fs.StringVar(&alpineSHA, "alpine-sha256", "", "expected sha256 of the tarball")
			fs.StringVar(&alpineTar, "alpine-tarball", "", "local tarball path for offline use (checksum optional but reported)")
			fs.StringVar(&alpineVer, "alpine-branch", "v3.22", "Alpine branch, used to find the newest minirootfs and for apk repositories")
			fs.StringVar(&arch, "alpine-arch", "x86_64", "Alpine architecture")
			fs.StringVar(&dns, "dns", "9.9.9.9", "nameserver written into the jail's resolv.conf")
			fs.StringVar(&mountBase, "mount-base", "/jop-proto-6", "directory the rootfs dataset is mounted under; last element must start with jop")
			fs.IntVar(&ruleset, "devfs-ruleset", alpine.DefaultRuleset, "devfs ruleset number to load (must be unused)")
		},
		Run:     run,
		Cleanup: cleanup,
	}) // never returns
}

func rootfs() string { return mountBase + "/alpine" }

// step runs a command; on failure it records it in the failure table and returns false.
func step(e *harness.Env, name string, argv ...string) (harness.Result, bool) {
	r := e.Run(argv...)
	if !r.OK() {
		fails.Add(name, strings.Join(argv, " "), r.Code, r.Combined())
		return r, false
	}
	return r, true
}

func jexec(e *harness.Env, name string, argv ...string) (harness.Result, bool) {
	return step(e, name, append([]string{"jexec", alpine.JailName}, argv...)...)
}

func sh(e *harness.Env, name, script string) (harness.Result, bool) {
	return jexec(e, name, "/bin/sh", "-c", script)
}

func writeFile(e *harness.Env, p, content string, mode os.FileMode) bool {
	if e.DryRun {
		e.Note("DRY-RUN would write %s (%d bytes)", p, len(content))
		return true
	}
	if !alpine.Under(rootfs(), p) && !alpine.Under(alpine.StateDir, p) {
		e.Abort("refusing to write outside the prototype tree: %s", p)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		fails.Add("write", p, -1, err.Error())
		return false
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		fails.Add("write", p, -1, err.Error())
		return false
	}
	return true
}

func download(e *harness.Env, url, dst string) (string, error) {
	if e.DryRun {
		e.Note("DRY-RUN would download %s -> %s", url, dst)
		return "", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, 256<<20)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fileSHA(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func getText(url string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return string(b), err
}

func run(e *harness.Env) {
	if err := alpine.ValidMountBase(mountBase); err != nil {
		e.Abort("%v", err)
	}
	e.Require("zfs", "jail", "jexec", "jls", "ifconfig", "mount", "umount", "tar", "devfs", "kldload", "kldstat")
	if !e.DryRun {
		if e.DatasetExists(e.Root) {
			e.Abort("%s exists: run --cleanup first", e.Root)
		}
		if _, err := os.Stat("/rescue/ifconfig"); err != nil {
			e.Abort("/rescue/ifconfig missing (needed as a static ifconfig inside the jail)")
		}
	}
	defer func() { // always leave nothing mounted or running unless the operator wants to inspect
		e.Note("run with --cleanup to remove everything; failures table follows")
		e.Printf("\n%s", fails.Table())
	}()

	// 1. datasets
	e.Step("datasets")
	step(e, "zfs-create", "zfs", "create", "-p", e.Root)
	if _, ok := step(e, "zfs-create-alpine", "zfs", "create", "-o", "mountpoint="+rootfs(), e.Root+"/alpine"); !ok {
		e.Abort("cannot create %s/alpine", e.Root)
	}
	e.Check("dataset", e.DryRun || e.DatasetExists(e.Root+"/alpine"), "created "+e.Root+"/alpine mounted at "+rootfs())

	// 2. fetch and extract
	e.Step("fetch and extract minirootfs")
	tmp := alpine.StateDir
	if !e.DryRun {
		os.MkdirAll(tmp, 0o700)
	}
	tarball, want := alpineTar, strings.ToLower(alpineSHA)
	if tarball == "" {
		url := alpineURL
		if url == "" {
			txt, err := func() (string, error) {
				if e.DryRun {
					e.Note("DRY-RUN would fetch %s", alpine.LatestReleasesURL(alpineVer, arch))
					return "", nil
				}
				return getText(alpine.LatestReleasesURL(alpineVer, arch))
			}()
			if err != nil {
				fails.Add("fetch-index", alpine.LatestReleasesURL(alpineVer, arch), -1, err.Error())
			} else if !e.DryRun {
				rel, err := alpine.ParseLatestReleases(txt, arch)
				if err != nil {
					fails.Add("parse-index", "latest-releases.yaml", -1, err.Error())
				} else {
					url, want = alpine.ReleaseURL(alpineVer, arch, rel.File), rel.SHA256
				}
			}
		}
		tarball = filepath.Join(tmp, "minirootfs.tar.gz")
		if url == "" && !e.DryRun {
			e.Abort("no tarball URL; pass --alpine-url and --alpine-sha256 or --alpine-tarball")
		}
		got, err := download(e, url, tarball)
		if err != nil {
			e.Abort("download: %v", err)
		}
		if !e.DryRun {
			e.Must("tarball-sha256", want != "" && got == want, fmt.Sprintf("got %s want %s (%s)", got, want, url))
		}
	} else if !e.DryRun {
		got, err := fileSHA(tarball)
		if err != nil {
			e.Abort("%v", err)
		}
		if want != "" {
			e.Must("tarball-sha256", got == want, "got "+got+" want "+want)
		} else {
			e.Check("tarball-sha256", true, "NO expected checksum given, computed "+got)
		}
	}
	_, okTar := step(e, "tar-extract", "tar", "-xpzf", tarball, "-C", rootfs())
	e.Must("extract", okTar, "minirootfs extracted into "+rootfs())
	if !e.DryRun {
		ch, al, berr := alpine.BrandELFTree(rootfs())
		e.Check("brand-elf", berr == nil, fmt.Sprintf("set OS/ABI Linux on %d ELF files without a brand; %d were already branded (err=%v)", ch, al, berr))
		e.Resolve(fmt.Sprintf("Alpine ELF files need the Linux brand (changed %d, already %d)", ch, al))
	}

	// 3. kernel modules
	e.Step("Linuxulator modules")
	// UNVERIFIED: on FreeBSD 15 the module for 64-bit Linux is `linux64` and it pulls linux_common.
	if e.Quiet("kldstat", "-qm", "linux64").OK() && !e.DryRun {
		e.Note("linux64 already loaded")
	} else {
		step(e, "kldload-linux64", "kldload", "linux64")
	}
	if !e.DryRun {
		loaded := e.Quiet("kldstat", "-qm", "linux64").Err == nil
		e.Check("linux64-module", loaded, "kldstat -m linux64; UNVERIFIED module name settled")
		e.Resolve("Linuxulator module name on FreeBSD 15 is linux64 (loaded=" + fmt.Sprint(loaded) + ")")
	}

	// 4. devfs ruleset
	e.Step("devfs ruleset %d", ruleset)
	sets := e.Quiet("devfs", "rule", "showsets").Out()
	if !e.DryRun && alpine.RulesetInUse(sets, ruleset) {
		e.Abort("devfs ruleset %d already exists; choose another with --devfs-ruleset (showsets: %q)", ruleset, sets)
	}
	loadedRules := true
	for _, argv := range alpine.DevfsRules(ruleset) {
		if _, ok := step(e, "devfs-rule", argv...); !ok {
			loadedRules = false
		}
	}
	if !e.DryRun {
		os.MkdirAll(alpine.StateDir, 0o700)
		os.WriteFile(filepath.Join(alpine.StateDir, "ruleset"), []byte(fmt.Sprint(ruleset)), 0o600)
	}
	e.Check("devfs-ruleset", loadedRules, "loaded private ruleset via `devfs rule -s N add ...`; showsets before: "+strings.ReplaceAll(sets, "\n", ","))

	// 5. files in the rootfs
	e.Step("configure rootfs")
	root := rootfs()
	writeFile(e, root+"/etc/resolv.conf", alpine.Resolv(dns), 0o644)
	writeFile(e, root+"/etc/apk/repositories", alpine.Repositories(alpineVer), 0o644)
	if !e.DryRun {
		os.MkdirAll(root+"/jop-rescue", 0o755)
	}
	for _, t := range []string{"ifconfig", "route", "netstat"} {
		// UNVERIFIED: /rescue/<tool> are hard links of one static crunched binary that dispatches on argv[0]; copying keeps the name.
		step(e, "copy-rescue-"+t, "cp", "/rescue/"+t, root+"/jop-rescue/"+t)
	}

	// 6. mounts
	e.Step("mounts under %s", root)
	mountOK := true
	for _, m := range alpine.MountList(ruleset) {
		if !e.DryRun {
			os.MkdirAll(filepath.Join(root, m.Rel), 0o755)
		}
		if _, ok := step(e, "mount-"+m.Type, alpine.MountArgv(root, m)...); !ok {
			mountOK = false
		}
	}
	// UNVERIFIED: tmpfs on /dev/shm needs a mount point that exists below devfs; devfs may refuse mkdir.
	e.Check("linux-mounts", mountOK, "devfs, fdescfs, tmpfs /dev/shm, linprocfs, linsysfs; "+fails.Summary("mount-tmpfs"))
	e.Resolve("tmpfs on <root>/dev/shm can be mounted over a devfs mount (ok=" + fmt.Sprint(mountOK) + ")")

	// 7. phase 1: bootstrap jail with inherited network
	e.Step("phase 1: bootstrap jail (shared host network) for apk")
	confFile := filepath.Join(alpine.StateDir, "jail.conf")
	conf1, err := alpine.RenderJailConf(alpine.JailParams{Name: alpine.JailName, Path: root, Hostname: alpine.JailName, InheritIP: true, DevfsRuleset: ruleset})
	if err != nil {
		e.Abort("%v", err)
	}
	writeFile(e, confFile, conf1, 0o600)
	if _, ok := step(e, "jail-create-bootstrap", "jail", "-f", confFile, "-c", alpine.JailName); !ok {
		e.Abort("cannot create bootstrap jail")
	}
	// UNVERIFIED: musl-linked Alpine binaries are accepted by the Linuxulator without brandelf.
	_, shOK := sh(e, "linux-exec", "echo linux-ok; uname -a")
	if !shOK && !e.DryRun {
		e.Note("recovery attempt: brandelf -t Linux on busybox and the musl loader")
		for _, f := range []string{"/bin/busybox", "/lib/ld-musl-" + strings.Replace(arch, "x86_64", "x86_64", 1) + ".so.1"} {
			step(e, "brandelf", "brandelf", "-t", "Linux", root+f)
		}
		_, shOK = sh(e, "linux-exec-after-brandelf", "echo linux-ok")
	}
	e.Check("linux-exec", shOK, "run /bin/sh inside the jail via jexec; "+fails.Summary("linux-exec"))
	e.Resolve("Alpine (musl) binaries execute under the FreeBSD 15 Linuxulator (ok=" + fmt.Sprint(shOK) + ")")

	_, upd := sh(e, "apk-update", "apk update")
	_, add := sh(e, "apk-add", "apk add openssh nginx")
	hasSSHD := false
	hasNginx := false
	if r, ok := sh(e, "check-bins", "test -x /usr/sbin/sshd && test -x /usr/sbin/nginx && echo yes"); ok && (r.Dry || strings.Contains(r.Out(), "yes")) {
		hasSSHD, hasNginx = true, true
	}
	e.Check("apk", upd && add && hasSSHD && hasNginx, "apk update="+fmt.Sprint(upd)+" add="+fmt.Sprint(add)+" binaries="+fmt.Sprint(hasSSHD && hasNginx)+"; "+fails.Summary("apk-update")+fails.Summary("apk-add"))
	writeFile(e, root+"/etc/nginx/jop.conf", alpine.NginxConf(), 0o644)
	writeFile(e, root+"/var/www/jop/index.html", alpine.NginxIndex, 0o644)
	writeFile(e, root+"/etc/ssh/sshd_config_jbp", alpine.SshdConf(), 0o644)
	sh(e, "mk-dirs", "mkdir -p /var/log/nginx /run /var/empty")
	sh(e, "ssh-keygen", "ssh-keygen -A")
	step(e, "jail-remove-bootstrap", "jail", "-f", confFile, "-r", alpine.JailName)
	step(e, "zfs-snapshot-installed", "zfs", "snapshot", e.Root+"/alpine@installed")

	// 8. phase 2: vnet jail
	e.Step("phase 2: vnet jail, bridge %s, epair %s/%s", alpine.Bridge, alpine.EpairA, alpine.EpairB)
	setupNet(e)
	conf2, _ := alpine.RenderJailConf(alpine.JailParams{Name: alpine.JailName, Path: root, Hostname: alpine.JailName, VNet: true, DevfsRuleset: ruleset})
	writeFile(e, confFile, conf2, 0o600)
	if _, ok := step(e, "jail-create-vnet", "jail", "-f", confFile, "-c", alpine.JailName); !ok {
		e.Abort("cannot create vnet jail (is VIMAGE in the kernel?)")
	}
	before := alpine.ParseAddrs(e.Quiet("ifconfig", alpine.EpairB).Stdout)
	_, moved := step(e, "ifconfig-vnet", "ifconfig", alpine.EpairB, "vnet", alpine.JailName)
	e.Check("epair-vnet-move", moved, "ifconfig "+alpine.EpairB+" vnet "+alpine.JailName+"; "+fails.Summary("ifconfig-vnet"))
	netTests(e, before)
	services(e)

	// 9. snapshot, break, rollback
	e.Step("snapshot, break, rollback")
	step(e, "zfs-snapshot-good", "zfs", "snapshot", e.Root+"/alpine@good")
	_, rmOK := step(e, "break", "rm", "-rf", root+"/lib")
	// host-side rm of <root>/lib only; the path is derived from --mount-base and ends in /alpine/lib
	apkBroken := false
	if r := e.Run("jexec", alpine.JailName, "/sbin/apk", "--version"); !r.OK() {
		apkBroken = true
		fails.Add("apk-after-break (expected)", "jexec apk --version", r.Code, r.Combined())
	}
	e.Check("break", rmOK && (apkBroken || e.DryRun), "removed <root>/lib; apk fails afterwards: "+fmt.Sprint(apkBroken))
	teardownMounts(e, true)
	_, rb := step(e, "zfs-rollback", "zfs", "rollback", e.Root+"/alpine@good")
	e.Check("zfs-rollback", rb, "zfs rollback "+e.Root+"/alpine@good with jail stopped and mounts unmounted")
	for _, m := range alpine.MountList(ruleset) {
		step(e, "remount-"+m.Type, alpine.MountArgv(root, m)...)
	}
	if _, ok := step(e, "jail-create-vnet-2", "jail", "-f", confFile, "-c", alpine.JailName); ok {
		// UNVERIFIED: the epair end returns to the host vnet when the jail is removed, so it can be moved again.
		back := e.Quiet("ifconfig", alpine.EpairB)
		e.Resolve("epair jail-side returns to host vnet on jail removal (present=" + fmt.Sprint(back.OK()) + ")")
		step(e, "ifconfig-vnet-2", "ifconfig", alpine.EpairB, "vnet", alpine.JailName)
		for _, a := range []string{alpine.JailAddr4 + "/24"} {
			step(e, "readdr", "jexec", alpine.JailName, "/jop-rescue/ifconfig", alpine.EpairB, "inet", a, "up")
		}
		step(e, "readdr6", "jexec", alpine.JailName, "/jop-rescue/ifconfig", alpine.EpairB, "inet6", alpine.JailAddr6+"/64", "up")
	}
	_, apkOK := jexec(e, "apk-after-rollback", "/sbin/apk", "--version")
	e.Check("rollback-restores", apkOK, "apk --version works again after rollback")
	services(e)
}

func setupNet(e *harness.Env) {
	// Verified on FreeBSD 15.1: a bridge cannot be created under an arbitrary name
	// (`ifconfig jopbr6 create` fails with "Invalid argument"). Create `bridge` and rename it.
	if br, ok := step(e, "bridge-create", "ifconfig", "bridge", "create"); ok {
		step(e, "bridge-rename", "ifconfig", strings.TrimSpace(br.Stdout), "name", alpine.Bridge)
	}
	r, ok := step(e, "epair-create", "ifconfig", "epair", "create")
	name := r.Out()
	if e.DryRun {
		name = "epair0a"
	}
	if !ok || !strings.HasSuffix(name, "a") {
		e.Abort("unexpected epair create output %q", name)
	}
	bname := strings.TrimSuffix(name, "a") + "b"
	// UNVERIFIED: epair interfaces can be renamed after creation and destroying the renamed a side destroys the pair.
	_, r1 := step(e, "rename-a", "ifconfig", name, "name", alpine.EpairA)
	_, r2 := step(e, "rename-b", "ifconfig", bname, "name", alpine.EpairB)
	e.Check("epair-rename", r1 && r2, "renamed "+name+" -> "+alpine.EpairA+", "+bname+" -> "+alpine.EpairB)
	step(e, "bridge-addm", "ifconfig", alpine.Bridge, "addm", alpine.EpairA, "up")
	step(e, "bridge-addr4", "ifconfig", alpine.Bridge, "inet", alpine.HostAddr4+"/24")
	step(e, "bridge-addr6", "ifconfig", alpine.Bridge, "inet6", alpine.HostAddr6, "prefixlen", "64")
	step(e, "epair-a-up", "ifconfig", alpine.EpairA, "up")
	// the address is set on the b side while it is still in the host vnet; this is the experiment.
	step(e, "epair-b-addr4", "ifconfig", alpine.EpairB, "inet", alpine.JailAddr4+"/24", "up")
	step(e, "epair-b-addr6", "ifconfig", alpine.EpairB, "inet6", alpine.JailAddr6, "prefixlen", "64")
}

// netTests answers: do addresses survive the move, and which way of running ifconfig for the jail works.
func netTests(e *harness.Env, before []string) {
	e.Step("address survival and host-side configuration of the jail's interface")
	want := before
	type method struct {
		name string
		argv []string
	}
	methods := []method{
		{"ifconfig -j (host binary)", []string{"ifconfig", "-j", alpine.JailName, alpine.EpairB}},
		{"jexec /sbin/ifconfig (expected to fail: no FreeBSD binaries in the jail)", []string{"jexec", alpine.JailName, "/sbin/ifconfig", alpine.EpairB}},
		{"jexec /jop-rescue/ifconfig (static copy from /rescue)", []string{"jexec", alpine.JailName, "/jop-rescue/ifconfig", alpine.EpairB}},
		{"linprocfs /proc/net/dev", []string{"jexec", alpine.JailName, "/bin/cat", "/proc/net/dev"}},
	}
	results := []string{}
	var seen []string
	for i, m := range methods {
		r := e.Run(m.argv...)
		ok := r.OK()
		if !ok {
			fails.Add("ifconfig-method", strings.Join(m.argv, " "), r.Code, r.Combined())
		}
		d := m.name + ": ok=" + fmt.Sprint(ok)
		if ok && i < 3 {
			a := alpine.ParseAddrs(r.Stdout)
			if i == 2 || len(seen) == 0 {
				seen = a
			}
			d += " addrs=" + strings.Join(a, ",")
		}
		results = append(results, d)
	}
	rescueOK := len(seen) > 0
	missing := alpine.Missing(want, seen)
	survived := rescueOK && len(missing) == 0 && len(want) > 0
	e.Check("addr-survive-vnet-move", rescueOK || e.DryRun, fmt.Sprintf("survived=%v before=%v missing=%v (ok means the question was answered)", survived, want, missing))
	e.Resolve(fmt.Sprintf("addresses set on epair in host survive its move into a vnet jail: %v (missing %v)", survived, missing))
	e.Check("host-side-ifconfig-methods", true, strings.Join(results, " ## "))
	e.Resolve("which host-side way configures a Linux jail's interface: see host-side-ifconfig-methods detail (ifconfig -j, jexec /sbin/ifconfig, static /rescue copy, linprocfs)")
	if !survived {
		e.Note("addresses lost: configuring them again inside the jail with the static ifconfig")
		// Verified on FreeBSD 15.1: `ifconfig -j <jail>` and `route -j <jail>` from the host configure
		// the interface inside the jail; nothing has to be installed in the jail.
		step(e, "readdr4", "ifconfig", "-j", alpine.JailName, alpine.EpairB, "inet", alpine.JailAddr4+"/24", "up")
		step(e, "readdr6", "ifconfig", "-j", alpine.JailName, alpine.EpairB, "inet6", alpine.JailAddr6, "prefixlen", "64")
		step(e, "default-route", "route", "-j", alpine.JailName, "add", "default", alpine.HostAddr4)
	}
	// UNVERIFIED: `route -j <jail>` exists; also try the static route inside.
	step(e, "route-j", "route", "-j", alpine.JailName, "-n", "get", alpine.HostAddr4)
	step(e, "route-static", "jexec", alpine.JailName, "/jop-rescue/route", "-n", "get", alpine.HostAddr4)
	if !e.DryRun {
		rc := e.Run("ping", "-c", "1", "-t", "3", alpine.JailAddr4)
		e.Check("host-to-jail-ping", rc.OK(), "ping "+alpine.JailAddr4+" over jopbr6: "+rc.Combined())
	}
}

func services(e *harness.Env) {
	e.Step("start sshd and nginx directly (no OpenRC), probe from the host")
	_, a := sh(e, "start-nginx", alpine.Detach("/usr/sbin/nginx -c /etc/nginx/jop.conf"))
	_, b := sh(e, "start-sshd", alpine.Detach("/usr/sbin/sshd -f /etc/ssh/sshd_config_jbp"))
	time.Sleep(time.Second)
	httpOK, d := false, "dry-run"
	if !e.DryRun {
		c := http.Client{Timeout: 5 * time.Second}
		resp, err := c.Get("http://" + alpine.JailAddr4 + "/")
		if err != nil {
			d = err.Error()
			fails.Add("http-get", "GET http://"+alpine.JailAddr4+"/", -1, d)
		} else {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			resp.Body.Close()
			httpOK = resp.StatusCode == 200 && strings.Contains(string(body), "jop6-ok")
			d = resp.Status + " " + strings.TrimSpace(string(body))
		}
	}
	e.Check("nginx-http", a && (httpOK || e.DryRun), "start="+fmt.Sprint(a)+" GET: "+d)
	banner, sshOK := "dry-run", false
	if !e.DryRun {
		conn, err := net.DialTimeout("tcp", alpine.JailAddr4+":22", 5*time.Second)
		if err != nil {
			banner = err.Error()
			fails.Add("ssh-dial", alpine.JailAddr4+":22", -1, banner)
		} else {
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			buf := make([]byte, 64)
			n, _ := conn.Read(buf)
			conn.Close()
			banner = strings.TrimSpace(string(buf[:n]))
			sshOK = strings.HasPrefix(banner, "SSH-2.0")
		}
	}
	e.Check("sshd-banner", b && (sshOK || e.DryRun), "start="+fmt.Sprint(b)+" banner: "+banner)
	e.Resolve("Alpine sshd and nginx started directly under the Linuxulator answer from the host (http=" + fmt.Sprint(httpOK) + " ssh=" + fmt.Sprint(sshOK) + ")")
}

// teardownMounts removes the jail and unmounts everything under the rootfs, deepest first.
func teardownMounts(e *harness.Env, keepNet bool) {
	if e.Quiet("jls", "-j", alpine.JailName, "name").OK() && !e.DryRun {
		step(e, "jail-remove", "jail", "-r", alpine.JailName)
	} else if e.DryRun {
		step(e, "jail-remove", "jail", "-r", alpine.JailName)
	}
	out := e.Quiet("mount", "-p").Stdout
	mounts := alpine.MountsUnder(out, rootfs())
	if e.DryRun {
		for _, m := range alpine.MountList(ruleset) {
			mounts = append([]string{rootfs() + "/" + m.Rel}, mounts...)
		}
	}
	for _, m := range mounts {
		step(e, "umount", "umount", m)
	}
}

func cleanup(e *harness.Env) {
	if err := alpine.ValidMountBase(mountBase); err != nil {
		e.Abort("%v", err)
	}
	e.Step("cleanup")
	teardownMounts(e, false)
	if e.Quiet("ifconfig", alpine.EpairA).OK() || e.DryRun {
		step(e, "destroy-epair", "ifconfig", alpine.EpairA, "destroy")
	}
	if e.Quiet("ifconfig", alpine.Bridge).OK() || e.DryRun {
		step(e, "destroy-bridge", "ifconfig", alpine.Bridge, "destroy")
	}
	if b, err := os.ReadFile(filepath.Join(alpine.StateDir, "ruleset")); err == nil || e.DryRun {
		n := strings.TrimSpace(string(b))
		if e.DryRun {
			n = fmt.Sprint(ruleset)
		}
		// only the ruleset recorded by this prototype's run is deleted
		step(e, "devfs-delset", "devfs", "rule", "-s", n, "delset")
	}
	if e.DatasetExists(e.Root) || e.DryRun {
		step(e, "zfs-destroy", "zfs", "destroy", "-r", e.Root)
	}
	if !e.DryRun {
		os.RemoveAll(alpine.StateDir)
	}
	e.Printf("\n%s", fails.Table())
}
