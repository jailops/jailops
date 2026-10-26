package zfs

import (
	"strconv"
	"strings"
	"time"

	"jailops.net/jailops/sdk/api"
)

// The parsers below are pure functions over the tab separated -H output.

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimRight(l, "\r")
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func parseErr(what, line string) error {
	if len(line) > 200 {
		line = line[:200]
	}
	return api.Errorf(api.CodeInternal, "cannot parse %s output: %q", what, line)
}

// ParseList parses `zfs list -H -p -o name,type,<props...>` output.
func ParseList(out string, props []string) ([]Dataset, error) {
	var ds []Dataset
	for _, l := range lines(out) {
		f := strings.Split(l, "\t")
		if len(f) != 2+len(props) {
			return nil, parseErr("zfs list", l)
		}
		d := Dataset{Name: f[0], Type: f[1], Props: map[string]string{}}
		for i, p := range props {
			if v := f[2+i]; v != "-" {
				d.Props[p] = v
			}
		}
		ds = append(ds, d)
	}
	return ds, nil
}

// ParseGet parses `zfs get -H -p -o property,value,source` output.
func ParseGet(out string) (map[string]Prop, error) {
	m := map[string]Prop{}
	for _, l := range lines(out) {
		f := strings.Split(l, "\t")
		if len(f) != 3 {
			return nil, parseErr("zfs get", l)
		}
		m[f[0]] = Prop{Value: f[1], Source: f[2]}
	}
	return m, nil
}

// ParseHolds parses `zfs holds -H -p` output: name, tag, timestamp (epoch
// with -p).
func ParseHolds(out string) ([]Hold, error) {
	var hs []Hold
	for _, l := range lines(out) {
		f := strings.Split(l, "\t")
		if len(f) != 3 {
			return nil, parseErr("zfs holds", l)
		}
		h := Hold{Tag: f[1]}
		if n, err := strconv.ParseInt(f[2], 10, 64); err == nil {
			h.Created = time.Unix(n, 0).UTC()
		}
		hs = append(hs, h)
	}
	return hs, nil
}

// ParseUint parses a single parseable numeric property value.
func ParseUint(s string) (uint64, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, parseErr("number", s)
	}
	return n, nil
}

// ParseClones parses the value of the "clones" property: a comma separated
// list of dataset names, or "-" / "" for none.
func ParseClones(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" || v == "-" {
		return nil
	}
	var out []string
	for _, c := range strings.Split(v, ",") {
		if c != "" {
			out = append(out, c)
		}
	}
	return out
}

// ParsePools parses `zpool list -H -p -o name,size,allocated,free,health`.
func ParsePools(out string) ([]Pool, error) {
	var ps []Pool
	for _, l := range lines(out) {
		f := strings.Split(l, "\t")
		if len(f) != 5 {
			return nil, parseErr("zpool list", l)
		}
		p := Pool{Name: f[0], Health: f[4]}
		var err error
		if p.Size, err = ParseUint(f[1]); err != nil {
			return nil, err
		}
		// allocated and free are "-" on a faulted pool
		p.Allocated, _ = strconv.ParseUint(f[2], 10, 64)
		p.Free, _ = strconv.ParseUint(f[3], 10, 64)
		ps = append(ps, p)
	}
	return ps, nil
}

// ParseActiveFeatures parses `zpool get -H -p -o property,value all <pool>`
// and returns the names of features whose value is "active", sorted as
// printed.
func ParseActiveFeatures(out string) ([]string, error) {
	var feats []string
	for _, l := range lines(out) {
		f := strings.Split(l, "\t")
		if len(f) < 2 {
			return nil, parseErr("zpool get", l)
		}
		if name, ok := strings.CutPrefix(f[0], "feature@"); ok && f[1] == "active" {
			feats = append(feats, name)
		}
	}
	return feats, nil
}
