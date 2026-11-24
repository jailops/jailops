package jailaudit

import (
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// JailParams are the jail parameters the checks read from the jail.conf JailOps renders for a jail.
type JailParams struct {
	// Read is false when the file could not be read; Err says why.
	Read bool
	Err  string
	// EnforceStatfs is 2 when the file does not set it (jail(8)'s default).
	EnforceStatfs int
	// SecureLevel is nil when the file does not set it.
	SecureLevel *int
	// Allow are the allow.* parameters set, without the prefix, sorted.
	Allow []string
	// DevfsRuleset is 0 when the file does not set it.
	DevfsRuleset int
}

var reConfParam = regexp.MustCompile(`^\s*([a-z_.]+)\s*(?:=\s*(-?[0-9]+)\s*)?;`)

// ParseJailConf reads the parameters the checks need from the text of a jail.conf as JailOps renders it: one
// parameter per line, NAME = NUMBER; or NAME;.
func ParseJailConf(text string) JailParams {
	p := JailParams{Read: true, EnforceStatfs: 2}
	for _, l := range strings.Split(text, "\n") {
		m := reConfParam.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[2])
		num := m[2] != "" && err == nil
		switch {
		case m[1] == "enforce_statfs" && num:
			p.EnforceStatfs = n
		case m[1] == "securelevel" && num:
			p.SecureLevel = &n
		case m[1] == "devfs_ruleset" && num:
			p.DevfsRuleset = n
		case strings.HasPrefix(m[1], "allow.") && m[2] == "":
			p.Allow = append(p.Allow, strings.TrimPrefix(m[1], "allow."))
		}
	}
	slices.Sort(p.Allow)
	p.Allow = slices.Compact(p.Allow)
	return p
}

// ParsePkgAudit reads what pkg audit prints for vulnerable packages ("NAME-VERSION is vulnerable:", then per advisory
// a description, "CVE: ..." lines and a "WWW: ..." line): the packages and the ids of their advisories, the CVE ids, or
// the VuXML id where an advisory names no CVE.
func ParsePkgAudit(out string) []VulnPackage {
	var pkgs []VulnPackage
	var cur *VulnPackage
	cves := 0 // CVE ids of the current advisory
	add := func(id string) {
		if cur != nil && id != "" && !slices.Contains(cur.IDs, id) {
			cur.IDs = append(cur.IDs, id)
		}
	}
	for _, l := range strings.Split(out, "\n") {
		t := strings.TrimSpace(l)
		switch {
		case strings.HasSuffix(t, " is vulnerable:") && !strings.HasPrefix(l, " "):
			nv := strings.TrimSuffix(t, " is vulnerable:")
			i := strings.LastIndexByte(nv, '-')
			if i <= 0 {
				cur = nil
				continue
			}
			pkgs = append(pkgs, VulnPackage{Name: nv[:i], Version: nv[i+1:]})
			cur, cves = &pkgs[len(pkgs)-1], 0
		case cur == nil:
		case strings.HasPrefix(t, "CVE:"):
			for _, c := range strings.Fields(strings.TrimPrefix(t, "CVE:")) {
				add(strings.TrimRight(c, ","))
				cves++
			}
		case strings.HasPrefix(t, "WWW:"):
			if cves == 0 {
				add(strings.TrimSuffix(path.Base(strings.TrimSpace(strings.TrimPrefix(t, "WWW:"))), ".html"))
			}
			cves = 0
		case t == "" || !strings.HasPrefix(l, " "):
			if t != "" {
				cur = nil // the summary line, or anything after the list
			}
		}
	}
	return pkgs
}
