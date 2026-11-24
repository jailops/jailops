package imagegen

import (
	"context"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"jailops.net/jailops/sdk/api"
)

const (
	// keysDir is where the base system ships the key directories of FreeBSD's package repositories.
	keysDir = "/usr/share/keys/"
	// releaseKeys verify base_release_<minor> of a RELEASE.
	releaseKeys = keysDir + "pkgbase-${VERSION_MAJOR}"
	// latestKeys verify base_latest (STABLE, PRERELEASE, CURRENT): it is signed with the key of the ports repositories
	// (on 15.2-PRERELEASE pkgbase-15 answers "No trusted public keys found"; 16.0-CURRENT ships no pkgbase-16).
	latestKeys = keysDir + "pkg"
)

// HostRepoConfs are the host's files that define its FreeBSD-base repository, a later one overriding an earlier one.
var HostRepoConfs = []string{"/etc/pkg/FreeBSD.conf", "/usr/local/etc/pkg/repos/FreeBSD.conf"}

// repoKind is "release" for a base_release_<minor> repository, "latest" for base_latest and "" for a URL that names
// neither (a mirror of another layout).
func repoKind(url string) string {
	switch {
	case strings.Contains(url, "/base_release"):
		return "release"
	case strings.Contains(url, "/base_latest"):
		return "latest"
	}
	return ""
}

var (
	reBaseBlock = regexp.MustCompile(`(?is)(?:^|[\s;,}])"?FreeBSD-base"?\s*[:=]?\s*\{((?:"[^"]*"|[^"}])*)\}`)
	reURLKey    = regexp.MustCompile(`(?i)(?:^|[\s{,;])url\s*[:=]\s*"([^"]*)"`)
	reKeysKey   = regexp.MustCompile(`(?i)(?:^|[\s{,;])fingerprints\s*[:=]\s*"([^"]*)"`)
)

// baseRepo reads the url and the fingerprints of the FreeBSD-base block of a pkg repository file ("" for what it
// does not set). Comment lines are skipped; a brace inside a quoted value (${ABI}) does not end the block.
func baseRepo(conf string) (url, keys string) {
	var b strings.Builder
	for _, l := range strings.Split(conf, "\n") {
		if t := strings.TrimSpace(l); strings.HasPrefix(t, "#") || strings.HasPrefix(t, "//") {
			continue
		}
		b.WriteString(l)
		b.WriteByte('\n')
	}
	for _, m := range reBaseBlock.FindAllStringSubmatch(b.String(), -1) {
		if u := reURLKey.FindStringSubmatch(m[1]); u != nil {
			url = u[1]
		}
		if k := reKeysKey.FindStringSubmatch(m[1]); k != nil {
			keys = k[1]
		}
	}
	return url, keys
}

// hostBaseRepo is the url and the key directory of the host's own FreeBSD-base repository, read from the files pkg
// reads them from; "" when the host names none.
func hostBaseRepo(ctx context.Context, f Files) (url, keys string) {
	for _, p := range HostRepoConfs {
		b, err := f.ReadFile(ctx, p)
		if err != nil {
			continue
		}
		u, k := baseRepo(string(b))
		if u != "" {
			url = u
		}
		if k != "" {
			keys = k
		}
	}
	return url, keys
}

// keyCandidates are the key directories that may verify the repository url of an image of release rel, in the order
// they are tried. The host's own FreeBSD-base repository comes first when it is of the same kind (FreeBSD ships the
// right directory with it, whatever the next release calls it); then the directory of the kind, then the other one.
func keyCandidates(url, rel, hostURL, hostKeys string) []string {
	kind := repoKind(url)
	if kind == "" {
		kind = "latest"
		if isRelease(rel) {
			kind = "release"
		}
	}
	var out []string
	add := func(k string) {
		if k != "" && strings.HasPrefix(k, keysDir) && !strings.Contains(k, "..") && !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	if hk := repoKind(hostURL); hk == kind || (hk == "" && repoKind(url) == "") {
		add(hostKeys)
	}
	if kind == "release" {
		add(releaseKeys)
		add(latestKeys)
	} else {
		add(latestKeys)
		add(releaseKeys)
	}
	return out
}

// expandKeys writes out the variables pkg expands in a key path for the image's ABI ("FreeBSD:16:amd64"); ok is
// false when one is left that only pkg knows.
func expandKeys(keys, abi, rel string) (string, bool) {
	major, minor, _ := parseRelease(rel)
	arch := abi[strings.LastIndex(abi, ":")+1:]
	r := strings.NewReplacer("${VERSION_MAJOR}", strconv.Itoa(major), "${VERSION_MINOR}", strconv.Itoa(minor),
		"${ABI}", abi, "${ARCH}", arch, "${OSNAME}", "FreeBSD")
	out := r.Replace(keys)
	return out, !strings.Contains(out, "${")
}

// PkgBaseKeys chooses the key directory of an image's repository once the host's keys are in the root (pkg -r looks
// for it there): the one --keys names, else the first of keyCandidates that the host ships.
func PkgBaseKeys(ctx context.Context, f Files, root string, spec api.ImageSpec, url string) (string, error) {
	if spec.Source.Keys != "" {
		return spec.Source.Keys, nil
	}
	abi, err := PkgBaseABI(spec)
	if err != nil {
		return "", err
	}
	hostURL, hostKeys := hostBaseRepo(ctx, f)
	cands := keyCandidates(url, spec.Release, hostURL, hostKeys)
	tried := make([]string, 0, len(cands))
	for _, k := range cands {
		dir, ok := expandKeys(k, abi, spec.Release)
		if !ok {
			return k, nil
		}
		there, err := f.Exists(ctx, path.Join(root, dir))
		if err != nil {
			return "", err
		}
		if there {
			return k, nil
		}
		tried = append(tried, dir)
	}
	return "", api.Errorf(api.CodePreconditionFailed, "no key directory to verify the repository %s: the host has none of %s",
		url, strings.Join(tried, ", ")).
		WithHint("name the key directory with --keys, or install the keys of the release under /usr/share/keys")
}
