package render

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// devfsPaths maps an allow-listed device to the devfs paths it unhides.
var devfsPaths = map[string][]string{
	"tun":  {"tun*"},
	"tap":  {"tap*"},
	"pf":   {"pf"},
	"bpf":  {"bpf*"},
	"fuse": {"fuse"},
	"dri":  {"dri", "dri/*"},
}

// DevfsRuleset renders the devfs ruleset of a jail that is given device
// nodes: its number, derived from the set of devices (so jails with the same
// devices share one), and the text of the ruleset in devfs.rules(5) syntax.
// The base is the stock jail ruleset: hide everything, then unhide the basic
// and login devices, then the allow-listed ones. Only devices on
// api.DevfsAllowList are rendered; anything else is an error, so a path can
// never be injected. The root helper loads the text; UNVERIFIED: the loading
// (devfs rule -s <n> ...) and that jail.conf's devfs_ruleset takes it.
func DevfsRuleset(devices []string) (int, string, error) {
	set := map[string]bool{}
	for _, d := range devices {
		if _, ok := devfsPaths[d]; !ok {
			return 0, "", fmt.Errorf("render: device %q is not on the devfs allow-list", d)
		}
		set[d] = true
	}
	var names []string
	for d := range set {
		names = append(names, d)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return DefaultDevfsRuleset, "", nil
	}
	h := fnv.New32a()
	h.Write([]byte(strings.Join(names, ",")))
	n := 5000 + int(h.Sum32()%50000)
	var b builder
	b.line("%s", Header)
	b.line("[jailops_devfs_%d=%d]", n, n)
	b.line("add include $devfsrules_hide_all")
	b.line("add include $devfsrules_unhide_basic")
	b.line("add include $devfsrules_unhide_login")
	for _, d := range names {
		for _, p := range devfsPaths[d] {
			b.line("add path '%s' unhide", p)
		}
	}
	text, err := b.result()
	return n, text, err
}

// DevfsDevices lists the device nodes of a jail's devfs devices.
func DevfsDevices(devs []api.DevfsDevice) []string {
	var out []string
	for _, d := range devs {
		out = append(out, d.Device)
	}
	return out
}

// linuxDevfsNodes are the device nodes a Linux userland needs. UNVERIFIED:
// a reasoned minimum, not a tested one (it ran under prototype 6).
var linuxDevfsNodes = []string{"null", "zero", "full", "random", "urandom", "tty", "ptmx", "pts", "pts/*", "fd", "fd/*", "stdin", "stdout", "stderr", "console"}

// DevfsUnhidable reports whether a devfs path glob is one a JailOps ruleset may unhide: a node of a Linux
// userland or a path of an allow-listed device. The helper accepts no other `unhide`.
func DevfsUnhidable(glob string) bool {
	for _, n := range linuxDevfsNodes {
		if n == glob {
			return true
		}
	}
	for _, ps := range devfsPaths {
		for _, p := range ps {
			if p == glob {
				return true
			}
		}
	}
	// the stock ruleset's own basic nodes
	switch glob {
	case "null", "zero", "random", "urandom", "stdin", "stdout", "stderr", "fd", "fd/*", "pts", "pts/*":
		return true
	}
	return false
}

// LinuxDevfsRuleset renders the private devfs ruleset of a Linux jail: hide
// everything, unhide the nodes of a Linux userland, then the allow-listed
// devices the jail was given. The number is derived from the set of devices.
func LinuxDevfsRuleset(devices []string) (int, string, error) {
	set := map[string]bool{}
	for _, d := range devices {
		if _, ok := devfsPaths[d]; !ok {
			return 0, "", fmt.Errorf("render: device %q is not on the devfs allow-list", d)
		}
		set[d] = true
	}
	var names []string
	for d := range set {
		names = append(names, d)
	}
	sort.Strings(names)
	h := fnv.New32a()
	h.Write([]byte("linux:" + strings.Join(names, ",")))
	n := 5000 + int(h.Sum32()%50000)
	var b builder
	b.line("%s", Header)
	b.line("[jailops_devfs_%d=%d]", n, n)
	b.line("add hide")
	for _, p := range linuxDevfsNodes {
		b.line("add path '%s' unhide", p)
	}
	for _, d := range names {
		for _, p := range devfsPaths[d] {
			b.line("add path '%s' unhide", p)
		}
	}
	text, err := b.result()
	return n, text, err
}
