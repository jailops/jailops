package apps

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/sdk/api"
)

// What the installer runs and writes, as data: the service issues exactly
// these argument vectors and paths, and the tests of the privileged helper
// check the same ones against its allow-list.

// DefaultImage is the image an app is installed from when none is named.
const DefaultImage = "freebsd@stable"

// SnapshotName is the snapshot the installer takes of the fresh installation.
const SnapshotName = "app-installed"

// JailopsRepo is the name of the host's pkg repository configuration for
// pkg.jailops.net (docs/install.md); packages with repo jailops come from it.
const JailopsRepo = "jailops"

// PkgInstallArgv is the host command that installs packages into a jail root
// from the FreeBSD repository (repo ports) or from pkg.jailops.net.
func PkgInstallArgv(root, repo string, names []string) []string {
	argv := []string{"pkg", "-r", root, "install", "-y"}
	if repo == JailopsRepo {
		argv = append(argv, "-r", JailopsRepo)
	}
	return append(argv, names...)
}

// PkgVersionArgv is the host command that reports the installed version of
// one package of a jail root.
func PkgVersionArgv(root, name string) []string {
	return []string{"pkg", "-r", root, "query", "%v", name}
}

// ServiceArgv is the in-jail command that runs an rc.d action.
func ServiceArgv(service, action string) []string {
	return []string{"/usr/sbin/service", service, action}
}

// RcConfPath is the rc.conf of a jail root.
func RcConfPath(root string) string { return root + "/etc/rc.conf" }

// RcVars lists what the installer writes into rc.conf: <name>_enable=YES
// and the knobs of every service, in the order of the definition.
func RcVars(svcs []api.AppService) []render.RcVar {
	var out []render.RcVar
	for _, s := range svcs {
		out = append(out, render.RcVar{Name: rcName(s.Name) + "_enable", Value: "YES"})
		for _, k := range sortedKeys(s.RC) {
			out = append(out, render.RcVar{Name: k, Value: s.RC[k]})
		}
	}
	return out
}

// PackageGroups returns the packages to install, grouped by repository in
// the order of the definition (ports first when both are present).
func PackageGroups(pkgs []api.AppPackage) (ports, jailops []string) {
	for _, p := range pkgs {
		if p.Repo == JailopsRepo {
			jailops = append(jailops, p.Name)
		} else {
			ports = append(ports, p.Name)
		}
	}
	return ports, jailops
}

// Volumes turns the data paths into the jail's volumes: one dataset per
// path, named after the path. The names are unique and match the volume
// name rule of the lifecycle.
func Volumes(data []api.AppPath) []api.VolumeMount {
	var out []api.VolumeMount
	seen := map[string]bool{}
	for _, d := range data {
		n := VolumeName(d.Path)
		for i := 2; seen[n]; i++ {
			n = VolumeName(d.Path) + "-" + strconv.Itoa(i)
		}
		seen[n] = true
		out = append(out, api.VolumeMount{Name: n, Mount: d.Path})
	}
	return out
}

// VolumeName derives a dataset name from a data path: lower case letters
// and digits, everything else a dash, at most 40 characters.
func VolumeName(p string) string {
	var b strings.Builder
	dash := true // no leading dash
	for _, r := range strings.ToLower(strings.Trim(p, "/")) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	n := strings.TrimRight(b.String(), "-")
	if len(n) > 40 {
		n = strings.TrimRight(n[:40], "-")
	}
	if n == "" {
		n = "data"
	}
	return n
}

// Limits turns the resource hints of a definition into jail limits.
func Limits(r api.AppResources) api.Resources {
	out := api.Resources{Memory: r.Memory, Disk: r.Disk}
	if r.CPU != "" {
		if f, err := strconv.ParseFloat(r.CPU, 64); err == nil && f > 0 {
			out.CPU = int(math.Ceil(f))
		}
	}
	return out
}

// ReleaseAtLeast reports whether an image release ("15.1-RELEASE",
// "15.1-RELEASE-p3") is at least the definition's release_min ("15.1").
func ReleaseAtLeast(release, minimum string) (bool, error) {
	rel, ok := releaseNumbers(release)
	if !ok {
		return false, fmt.Errorf("%q is not a release", release)
	}
	mn, ok := releaseNumbers(minimum)
	if !ok {
		return false, fmt.Errorf("%q is not a release", minimum)
	}
	return rel[0] > mn[0] || rel[0] == mn[0] && rel[1] >= mn[1], nil
}

func releaseNumbers(s string) ([2]int, bool) {
	s, _, _ = strings.Cut(s, "-")
	maj, min, ok := strings.Cut(s, ".")
	if !ok {
		return [2]int{}, false
	}
	a, err1 := strconv.Atoi(maj)
	b, err2 := strconv.Atoi(min)
	if err1 != nil || err2 != nil || a < 0 || b < 0 {
		return [2]int{}, false
	}
	return [2]int{a, b}, true
}
