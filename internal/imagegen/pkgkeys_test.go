package imagegen

import (
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// the FreeBSD-base block of /etc/pkg/FreeBSD.conf as 16.0-CURRENT and 15.1-RELEASE ship it
const (
	confCurrent = `#
# To disable a repository, instead of modifying or removing this file,
# create or edit /usr/local/etc/pkg/repos/FreeBSD.conf, e.g.:
#   echo "FreeBSD-base: { enabled: no }" >> /usr/local/etc/pkg/repos/FreeBSD.conf
#

FreeBSD-ports: {
  url: "pkg+https://pkg.FreeBSD.org/${ABI}/latest",
  mirror_type: "srv",
  signature_type: "fingerprints",
  fingerprints: "/usr/share/keys/pkg",
  enabled: yes
}
FreeBSD-base: {
  url: "pkg+https://pkg.FreeBSD.org/${ABI}/base_latest",
  mirror_type: "srv",
  signature_type: "fingerprints",
  fingerprints: "/usr/share/keys/pkg",
  enabled: yes
}
`
	confRelease = `FreeBSD-ports: {
  url: "pkg+https://pkg.FreeBSD.org/${ABI}/quarterly",
  fingerprints: "/usr/share/keys/pkg",
  enabled: yes
}
FreeBSD-base: {
  url: "pkg+https://pkg.FreeBSD.org/${ABI}/base_release_${VERSION_MINOR}",
  mirror_type: "srv",
  signature_type: "fingerprints",
  fingerprints: "/usr/share/keys/pkgbase-${VERSION_MAJOR}",
  enabled: no
}
`
)

func TestBaseRepoReadsTheFreeBSDBaseBlock(t *testing.T) {
	for name, c := range map[string]struct{ conf, url, keys string }{
		"16.0-CURRENT":                     {confCurrent, "pkg+https://pkg.FreeBSD.org/${ABI}/base_latest", "/usr/share/keys/pkg"},
		"15.1-RELEASE":                     {confRelease, "pkg+https://pkg.FreeBSD.org/${ABI}/base_release_${VERSION_MINOR}", "/usr/share/keys/pkgbase-${VERSION_MAJOR}"},
		"an override that only enables it": {"FreeBSD-base: { enabled: yes }\n", "", ""},
		"a commented block":                {"# FreeBSD-base: { fingerprints: \"/usr/share/keys/evil\" }\n", "", ""},
		"pkg -vv style":                    {"  FreeBSD-base: { \n    url             : \"pkg+https://m.example.org/x/base_latest\",\n    fingerprints    : \"/usr/share/keys/pkg\"\n  }\n", "pkg+https://m.example.org/x/base_latest", "/usr/share/keys/pkg"},
	} {
		if u, k := baseRepo(c.conf); u != c.url || k != c.keys {
			t.Errorf("%s: %q %q, want %q %q", name, u, k, c.url, c.keys)
		}
	}
}

func TestPkgBaseKeysFollowsTheHost(t *testing.T) {
	const mirror = "https://mirror.example.org/FreeBSD-base"
	for _, c := range []struct {
		name, rel, url, keys string
		hostConf, override   string
		hostKeys             []string
		want                 string
	}{
		// the reported case: --repo on a 16.0-CURRENT host, which has no pkgbase-16
		{name: "16 CURRENT, --repo", rel: "16.0-CURRENT", url: mirror, hostConf: confCurrent, hostKeys: []string{"pkg", "pkgbase-15"}, want: latestKeys},
		{name: "16 CURRENT, no host conf", rel: "16.0-CURRENT", url: mirror, hostKeys: []string{"pkg", "pkgbase-15"}, want: latestKeys},
		{name: "16 CURRENT, base_latest", rel: "16.0-CURRENT", url: "pkg+https://pkg.FreeBSD.org/${ABI}/base_latest", hostConf: confCurrent, hostKeys: []string{"pkg"}, want: latestKeys},
		// a RELEASE built on a CURRENT host: the host's base_latest keys are not those of a release
		{name: "15.1 RELEASE on a CURRENT host", rel: "15.1-RELEASE", url: "pkg+https://pkg.FreeBSD.org/${ABI}/base_release_1", hostConf: confCurrent, hostKeys: []string{"pkg", "pkgbase-15"}, want: releaseKeys},
		{name: "15.1 RELEASE, --repo", rel: "15.1-RELEASE", url: mirror, hostConf: confRelease, hostKeys: []string{"pkg", "pkgbase-15"}, want: releaseKeys},
		{name: "a RELEASE without its keys falls back", rel: "16.0-RELEASE", url: "pkg+https://pkg.FreeBSD.org/${ABI}/base_release_0", hostConf: confCurrent, hostKeys: []string{"pkg", "pkgbase-15"}, want: latestKeys},
		// a directory FreeBSD names in its own repository file is followed
		{name: "the host names a new directory", rel: "16.0-CURRENT", url: mirror, hostKeys: []string{"pkg", "pkgbase-next"},
			hostConf: confCurrent, override: "FreeBSD-base: { fingerprints: \"/usr/share/keys/pkgbase-next\" }\n", want: "/usr/share/keys/pkgbase-next"},
		{name: "outside /usr/share/keys is not taken from the host", rel: "16.0-CURRENT", url: mirror, hostKeys: []string{"pkg"},
			override: "FreeBSD-base: { url: \"https://x.example.org/base_latest\", fingerprints: \"/tmp/keys\" }\n", want: latestKeys},
		{name: "--keys", rel: "16.0-CURRENT", url: mirror, keys: "/usr/local/etc/jailops/keys/base", hostKeys: []string{}, want: "/usr/local/etc/jailops/keys/base"},
	} {
		w := newWorld(t)
		w.Files.HostKeys = c.hostKeys
		if c.hostConf != "" {
			w.Files.Put(HostRepoConfs[0], []byte(c.hostConf))
		}
		if c.override != "" {
			w.Files.Put(HostRepoConfs[1], []byte(c.override))
		}
		if err := w.Files.SeedKeys(bg, "/r"); err != nil {
			t.Fatal(err)
		}
		spec := api.ImageSpec{OS: api.OSFreeBSD, Release: c.rel, Arch: "amd64", Source: api.ImageSource{Type: "pkgbase", Repo: c.url, Keys: c.keys}}
		got, err := PkgBaseKeys(bg, w.Files, "/r", spec, c.url)
		if err != nil || got != c.want {
			t.Errorf("%s: %q %v, want %q", c.name, got, err, c.want)
		}
	}
}

func TestPkgBaseKeysNamesWhatItTried(t *testing.T) {
	w := newWorld(t)
	w.Files.HostKeys = []string{}
	w.Files.Put(HostRepoConfs[0], []byte(confCurrent))
	if err := w.Files.SeedKeys(bg, "/r"); err != nil {
		t.Fatal(err)
	}
	spec := api.ImageSpec{OS: api.OSFreeBSD, Release: "16.0-CURRENT", Arch: "amd64", Source: api.ImageSource{Type: "pkgbase"}}
	_, err := PkgBaseKeys(bg, w.Files, "/r", spec, "https://mirror.example.org/FreeBSD-base")
	if !api.IsCode(err, api.CodePreconditionFailed) || !strings.Contains(err.Error(), "/usr/share/keys/pkg, /usr/share/keys/pkgbase-16") {
		t.Fatalf("%v", err)
	}
}

func TestPkgBaseBuildWritesTheChosenKeys(t *testing.T) {
	w := newWorld(t)
	w.Files.HostKeys = []string{"pkg"}
	w.Files.Put(HostRepoConfs[0], []byte(confCurrent))
	b := &Build{M: w.M, Spec: api.ImageSpec{OS: api.OSFreeBSD, Release: "16.0-CURRENT", Arch: "amd64",
		Source: api.ImageSource{Type: "pkgbase", Repo: "https://mirror.example.org/FreeBSD-base"}}, Work: "/w", Root: "/r"}
	steps, err := PkgBase{}.Install(b)
	if err != nil {
		t.Fatal(err)
	}
	seeded := false
	for _, st := range steps {
		if st.Name == "seed-keys" {
			seeded = true
		}
		if st.Name != "write-repo-conf" {
			continue
		}
		if !seeded {
			t.Fatal("the repository file is written before the host's keys are in the root")
		}
		if err := w.Files.SeedKeys(bg, b.Root); err != nil {
			t.Fatal(err)
		}
		if err := st.Do(bg); err != nil {
			t.Fatal(err)
		}
		if got := string(w.Files.Data["/w/repos/jailops.conf"]); !strings.Contains(got, `fingerprints: "/usr/share/keys/pkg"`) {
			t.Errorf("%s", got)
		}
		return
	}
	t.Fatal("no write-repo-conf step")
}

// A new image has sysrc: in FreeBSD-bsdconfig on 15 (the minimal jail set lacks it), in FreeBSD-rc on 16 (the set holds it).
func TestDefaultSetsHaveSysrc(t *testing.T) {
	has := func(release string) bool {
		for _, s := range (PkgBase{}).sets(api.ImageSpec{Release: release}) {
			if s == "FreeBSD-bsdconfig" {
				return true
			}
		}
		return false
	}
	if !has("15.1-RELEASE") || !has("15.2-STABLE") {
		t.Error("a 15 image has no FreeBSD-bsdconfig, so no sysrc")
	}
	if has("16.0-CURRENT") {
		t.Error("a 16 image names FreeBSD-bsdconfig: sysrc is in FreeBSD-rc, which the minimal jail set holds")
	}
}
