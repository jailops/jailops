package service

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/sdk/api"
)

// stockDevfsRules are the stock devfs rulesets 1 to 4 of FreeBSD (/etc/defaults/devfs.rules: devfsrules_hide_all,
// devfsrules_unhide_basic, devfsrules_unhide_login, devfsrules_jail), as `devfs rule -s N show` prints them without
// their numbers. A FreeBSD jail without devices mounts its /dev with ruleset 4; the private ruleset of a jail with
// devices includes 1 to 3. So what these sets hold is what every FreeBSD jail sees.
var stockDevfsRules = map[int][]string{
	1: {"hide"},
	2: {"path null unhide", "path zero unhide", "path crypto unhide", "path random unhide", "path urandom unhide"},
	3: stockLoginRules(),
	4: {"include 1", "include 2", "include 3", "path fuse unhide", "path zfs unhide"},
}

func stockLoginRules() []string {
	var out []string
	for _, p := range []string{"pty", "tty"} {
		for _, c := range "pqrsPQRSlmnoLMNO" {
			out = append(out, "path "+p+string(c)+"* unhide")
		}
	}
	return append(out, "path ptmx unhide", "path pts unhide", "path pts/* unhide", "path fd unhide", "path fd/* unhide",
		"path stdin unhide", "path stdout unhide", "path stderr unhide")
}

// devfsExtra compares the rules of a stock set with FreeBSD's: the rules that show a jail more (an unhide or an include
// the stock set does not have, once each), and whether a set that should hide everything does not.
func devfsExtra(set int, rules []string) []string {
	var extra []string
	for _, r := range rules {
		r = strings.Join(strings.Fields(strings.ReplaceAll(r, "'", "")), " ")
		if slices.Contains(stockDevfsRules[set], r) || slices.Contains(extra, r) {
			continue
		}
		if f := strings.Fields(r); len(f) > 0 && (f[0] == "include" || f[len(f)-1] == "unhide") {
			extra = append(extra, r)
		}
	}
	if set == 1 && !slices.Contains(rules, "hide") {
		extra = append(extra, "(no hide: nothing is hidden)")
	}
	return extra
}

// parseDevfsShow reads `devfs rule -s N show`: the rules without their numbers.
func parseDevfsShow(out string) []string {
	var rules []string
	for _, ln := range strings.Split(out, "\n") {
		if f := strings.Fields(ln); len(f) >= 2 {
			rules = append(rules, strings.Join(f[1:], " "))
		}
	}
	return rules
}

// doctorDevfsRuleset checks the stock devfs rulesets the jails build on against FreeBSD's own: a FreeBSD jail without
// devices mounts /dev with ruleset 4 as the host has it loaded, and JailOps loads and checks only its private rulesets.
// A rule added to 4 (or to 1 to 3, which the private rulesets include) shows every such jail more of the host's
// devices, such as bpf (the frames of the jail's link), pf or drm, without anyone granting them; a ruleset 4 that is
// not loaded hides nothing at all. The rules are read through the helper's read-only devfs forms.
func (s *Service) doctorDevfsRuleset(ctx context.Context, c *checkSet) {
	const name = "jail.devfs_ruleset"
	var plain, all []string // FreeBSD jails on ruleset 4, and every FreeBSD jail
	s.mu.RLock()
	for _, k := range sortedKeys(s.jails) {
		j := s.jails[k].jail
		if j.Spec.OS == api.OSLinux {
			continue
		}
		n := j.Metadata.Project + "/" + j.Metadata.Name
		all = append(all, n)
		if len(render.SpecDevfsDevices(j.Spec)) == 0 {
			plain = append(plain, n)
		}
	}
	s.mu.RUnlock()
	if len(all) == 0 {
		return
	}
	ok, avail, out := s.runOK(ctx, "devfs", "rule", "showsets")
	if !ok {
		if avail {
			c.add(name, api.DoctorUnknown, "the devfs rulesets could not be read", "")
		}
		return
	}
	loaded := map[int]bool{}
	for _, f := range strings.Fields(out) {
		if n, err := strconv.Atoi(f); err == nil {
			loaded[n] = true
		}
	}
	if !loaded[render.DefaultDevfsRuleset] && len(plain) > 0 {
		c.add(name, api.DoctorFail, fmt.Sprintf("the devfs ruleset %d is not loaded, so it hides nothing: %s see the host's whole /dev, raw disks included, once they mount it", render.DefaultDevfsRuleset, jailList(plain)),
			"load the stock rules: service devfs restart (it reads /etc/defaults/devfs.rules and /etc/devfs.rules); then restart those jails when it suits you")
		return
	}
	var problems []string
	for set := 1; set <= render.DefaultDevfsRuleset; set++ {
		if !loaded[set] {
			continue // a private ruleset refuses to load without the sets it includes
		}
		ok, _, out := s.runOK(ctx, "devfs", "rule", "-s", strconv.Itoa(set), "show")
		if !ok {
			c.add(name, api.DoctorUnknown, fmt.Sprintf("the devfs ruleset %d could not be read", set), "")
			return
		}
		extra := devfsExtra(set, parseDevfsShow(out))
		if len(extra) == 0 {
			continue
		}
		who := all
		if set == render.DefaultDevfsRuleset {
			who = plain
		}
		if len(who) == 0 {
			continue
		}
		problems = append(problems, fmt.Sprintf("ruleset %d has %s, which %s get", set, strings.Join(extra, ", "), jailList(who)))
	}
	if len(problems) == 0 {
		c.add(name, api.DoctorOK, fmt.Sprintf("the stock devfs rulesets 1 to %d are FreeBSD's: a jail sees only the devices it was granted", render.DefaultDevfsRuleset), "")
		return
	}
	c.add(name, api.DoctorWarn, "the stock devfs rulesets show the jails more devices than FreeBSD's: "+strings.Join(problems, "; "),
		"grant a device to the jail that needs it (jailops config set JAIL devfs.NAME.device bpf) instead of changing the stock sets; remove the rule from /etc/devfs.rules and run service devfs restart, then restart the jails named when it suits you, so their /dev is mounted with the stock rules again")
}

// jailList names up to five jails, and how many more.
func jailList(names []string) string {
	if len(names) <= 5 {
		return "the jail(s) " + strings.Join(names, ", ")
	}
	return fmt.Sprintf("the jails %s and %d more", strings.Join(names[:5], ", "), len(names)-5)
}
