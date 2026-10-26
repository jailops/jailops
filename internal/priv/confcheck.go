package priv

import (
	"context"
	"fmt"
	"jailops.net/jailops/internal/render"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var (
	blockOpenRE = regexp.MustCompile(`^([A-Za-z0-9_-]{1,64}) \{$`)
	paramRE     = regexp.MustCompile(`^([a-z][a-z0-9_.]{0,63})(?: = (.+))?;$`)
	quotedRE    = regexp.MustCompile(`^"([^"\\;$\x00-\x1f]*)"$`)
	intRE       = regexp.MustCompile(`^-?[0-9]{1,9}$`)
	hostnameRE  = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,253})$`)
	osreleaseRE = regexp.MustCompile(`^[0-9]+\.[0-9]+-[A-Za-z0-9_]+$`)
	fsOptsRE    = regexp.MustCompile(`^[a-z0-9_]+(=[A-Za-z0-9_]+)?(,[a-z0-9_]+(=[A-Za-z0-9_]+)?)*$`)
)

// checkConfContent validates what is written into ConfDir (jail.conf and
// fstab files) and into jail directories (fstab files). Other paths are not
// content-checked.
func (l *Local) checkConfContent(ctx context.Context, r resolved, path string, data []byte) error {
	base := filepath.Base(path)
	inConf := within(l.cfg.ConfDir, filepath.Clean(path))
	switch {
	case strings.HasSuffix(base, ".conf") && inConf:
		return l.cfg.checkJailConf(ctx, jailNameOfConf(l.cfg.ConfDir, path), string(data))
	case base == "fstab" || strings.HasSuffix(base, ".fstab"):
		// A file that is rewritten unchanged, as every start of a jail does, keeps the host mounts an administrator
		// admitted; a new or changed one needs the administrator again. The operator's allow-list applies to both.
		var existing string
		if b, err := l.ReadFile(ctx, path); err == nil {
			existing = string(b)
		}
		if err := l.cfg.checkFstab(ctx, string(data), existing); err != nil {
			return err
		}
		return l.cfg.checkFstabPaths(string(data), "")
	case base == "devfs.rules" && inConf:
		return checkDevfsRulesFile(string(data))
	case inConf:
		return forbidden("only .conf, .fstab and devfs.rules files may be written to %s", l.cfg.ConfDir)
	}
	return nil
}

// jailNameOfConf derives the jail name from <dir>/<name>.conf or
// <dir>/<name>/jail.conf; "" when the layout does not match.
func jailNameOfConf(dir, path string) string {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return ""
	}
	if n, ok := strings.CutSuffix(rel, ".conf"); ok && !strings.Contains(n, "/") {
		return n
	}
	if d, f := filepath.Split(rel); f == "jail.conf" {
		return strings.TrimSuffix(d, "/")
	}
	return ""
}

func unquote(v string) (string, bool) {
	m := quotedRE.FindStringSubmatch(v)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// CheckJailConf validates the text of a jail.conf as the renderer produces it:
// exactly one block named like the jail, one parameter per line, only the
// listed parameters, and nothing that could expand a variable except the fixed
// hook command lines. Anything dangerous needs the admin assertion and an
// AdminPolicy entry.
func (c Config) checkJailConf(ctx context.Context, name, text string) error {
	if name == "" {
		return invalid("jail.conf must be named <name>.conf or <name>/jail.conf")
	}
	if err := ValidJailName(name); err != nil {
		return err
	}
	if len(text) > 64<<10 {
		return invalid("jail.conf too large")
	}
	admin := isAdmin(ctx)
	var open, closed, hasVnet, hasPath bool
	for i, line := range strings.Split(text, "\n") {
		ln := i + 1
		line = strings.TrimRight(line, "\r")
		t := strings.TrimLeft(line, "\t")
		switch {
		case strings.TrimSpace(t) == "" || strings.HasPrefix(t, "#"):
			continue
		case closed:
			return invalid("jail.conf line %d: text after the block", ln)
		case !open:
			m := blockOpenRE.FindStringSubmatch(t)
			if m == nil || m[1] != name || line != t {
				return invalid("jail.conf line %d: expected the block %q", ln, name+" {")
			}
			open = true
			continue
		case t == "}" && line == t:
			closed = true
			continue
		case !strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "\t\t"):
			return invalid("jail.conf line %d: parameters are indented with one tab", ln)
		}
		m := paramRE.FindStringSubmatch(t)
		if m == nil {
			return invalid("jail.conf line %d: not a parameter line", ln)
		}
		key, val := m[1], m[2]
		hasVal := strings.Contains(t, " = ")
		if err := c.checkJailParam(ctx, name, key, val, hasVal, admin); err != nil {
			return fmt.Errorf("jail.conf line %d: %w", ln, err)
		}
		hasVnet = hasVnet || key == "vnet" && !hasVal
		hasPath = hasPath || key == "path"
	}
	switch {
	case !open || !closed:
		return invalid("jail.conf has no complete block")
	case !hasVnet:
		return forbidden("jail.conf must set vnet; shared-IP jails are not supported")
	case !hasPath:
		return invalid("jail.conf has no path")
	}
	return nil
}

func (c Config) checkJailParam(ctx context.Context, name, key, val string, hasVal, admin bool) error {
	bare := func() error {
		if hasVal {
			return invalid("%s takes no value", key)
		}
		return nil
	}
	str := func() (string, error) {
		if !hasVal {
			return "", invalid("%s needs a value", key)
		}
		s, ok := unquote(val)
		if !ok {
			return "", invalid("%s: value must be a quoted string without $ ; or backslash", key)
		}
		return s, nil
	}
	num := func() (int, error) {
		if !hasVal || !intRE.MatchString(val) {
			return 0, invalid("%s needs a number", key)
		}
		n, _ := strconv.Atoi(val)
		return n, nil
	}
	hook := func(phase string) error {
		if !hasVal {
			return invalid("%s needs a value", key)
		}
		want := fmt.Sprintf("%q", c.HelperPath+" hook "+phase+" ${name}")
		if val != want {
			return forbidden("%s must be exactly %s", key, want)
		}
		return nil
	}
	switch key {
	case "path":
		s, err := str()
		if err != nil {
			return err
		}
		if !filepath.IsAbs(s) || filepath.Clean(s) != s || c.isMountRoot(s) || !c.inMounts(s) {
			return forbidden("path %q is not below %s", s, c.mountBaseDesc())
		}
		return nil
	case "host.hostname":
		s, err := str()
		if err != nil {
			return err
		}
		if !hostnameRE.MatchString(s) {
			return invalid("invalid hostname %q", s)
		}
		return nil
	case "vnet", "mount.devfs", "persist", "exec.clean":
		return bare()
	case "vnet.interface":
		if !hasVal {
			return invalid("vnet.interface needs a value")
		}
		for _, part := range strings.Split(val, ", ") {
			s, ok := unquote(part)
			if !ok {
				return invalid("vnet.interface: bad value")
			}
			if err := c.ifOwned(s); err != nil {
				return err
			}
		}
		return nil
	case "devfs_ruleset":
		n, err := num()
		if err != nil {
			return err
		}
		if n != 4 && !(n >= DevfsRulesetMin && n <= DevfsRulesetMax) && !(admin && slices.Contains(c.Admin.DevfsRulesets, n)) {
			// the private range holds only rulesets defined through the helper's devfs grammar
			return forbidden("devfs ruleset %d needs the admin assertion and an allow-list entry", n)
		}
		return nil
	case "mount.fstab":
		s, err := str()
		if err != nil {
			return err
		}
		if !filepath.IsAbs(s) || filepath.Clean(s) != s || !within(c.ConfDir, s) {
			return forbidden("fstab %q is outside the allowed roots", s)
		}
		return nil
	case "enforce_statfs":
		n, err := num()
		if err != nil {
			return err
		}
		if n != 2 {
			return c.rawParam(admin, key)
		}
		return nil
	case "securelevel":
		n, err := num()
		if err != nil {
			return err
		}
		if n < 0 || n > 3 {
			return c.rawParam(admin, key)
		}
		return nil
	case "osrelease":
		s, err := str()
		if err != nil {
			return err
		}
		if !osreleaseRE.MatchString(s) {
			return invalid("invalid osrelease %q", s)
		}
		return nil
	case "exec.prestart", "exec.poststart", "exec.prestop", "exec.poststop":
		return hook(strings.TrimPrefix(key, "exec."))
	case "exec.start":
		if val != `"/bin/sh /etc/rc"` {
			return c.rawParam(admin, key)
		}
		return nil
	case "exec.stop":
		if val != `"/bin/sh /etc/rc.shutdown jail"` {
			return c.rawParam(admin, key)
		}
		return nil
	}
	if flag, ok := strings.CutPrefix(key, "allow."); ok {
		if err := bare(); err != nil {
			return err
		}
		if !admin || !slices.Contains(c.Admin.AllowFlags, flag) {
			return forbidden("allow.%s needs the admin assertion and an allow-list entry", flag)
		}
		return nil
	}
	if err := c.rawParam(admin, key); err != nil {
		return err
	}
	if hasVal && !intRE.MatchString(val) {
		if _, ok := unquote(val); !ok {
			return invalid("%s: unsafe value", key)
		}
	}
	return nil
}

func (c Config) rawParam(admin bool, key string) error {
	if admin && slices.Contains(c.Admin.RawJailParams, key) {
		return nil
	}
	return forbidden("jail parameter %s needs the admin assertion and an allow-list entry", key)
}

var fstabTypes = map[string]bool{"devfs": true, "tmpfs": true, "fdescfs": true, "linprocfs": true, "linsysfs": true, "procfs": true, "nullfs": true}

// checkFstab validates a jail fstab. Mount points must be below a storage
// root's mountpoint. A nullfs source must be below one too, or, with the admin assertion,
// below an AdminPolicy host mount root.
func (c Config) checkFstab(ctx context.Context, text, existing string) error {
	if len(text) > 64<<10 {
		return invalid("fstab too large")
	}
	for i, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		f := strings.Fields(t)
		if len(f) != 6 {
			return invalid("fstab line %d: want 6 fields", i+1)
		}
		dev, mp, typ, opts := f[0], f[1], f[2], f[3]
		if !fstabTypes[typ] {
			return forbidden("fstab line %d: filesystem type %q is not allowed", i+1, typ)
		}
		if !fsOptsRE.MatchString(opts) {
			return invalid("fstab line %d: invalid options", i+1)
		}
		for _, n := range f[4:] {
			if n != "0" && n != "1" && n != "2" {
				return invalid("fstab line %d: invalid dump or pass", i+1)
			}
		}
		if !cleanAbs(mp) || !c.inMounts(mp) {
			return forbidden("fstab line %d: mount point %q is outside %s", i+1, mp, c.mountBaseDesc())
		}
		if typ != "nullfs" {
			if dev != typ {
				return invalid("fstab line %d: device of a %s mount must be %q", i+1, typ, typ)
			}
			continue
		}
		if !cleanAbs(dev) {
			return invalid("fstab line %d: invalid source %q", i+1, dev)
		}
		if c.inMounts(dev) {
			continue
		}
		listed := false
		for _, r := range c.Admin.HostMountRoots {
			listed = listed || within(r, dev)
		}
		admitted := isAdmin(ctx)
		if !admitted {
			for _, e := range parseFstab(existing) {
				admitted = admitted || e.line == strings.Join(f, " ")
			}
		}
		if !(listed && admitted) {
			return forbidden("fstab line %d: host mount of %q needs the admin assertion and an allow-list entry", i+1, dev)
		}
	}
	return nil
}

func cleanAbs(p string) bool {
	return filepath.IsAbs(p) && filepath.Clean(p) == p && !strings.ContainsAny(p, "\x00\n\t ")
}

var (
	reDevfsSection = regexp.MustCompile(`^\[jailops_devfs_([0-9]{1,6})=([0-9]{1,6})\]$`)
	reDevfsAddPath = regexp.MustCompile(`^add path '([a-z0-9*?_./-]{1,64})' (hide|unhide)$`)
	reDevfsInclude = regexp.MustCompile(`^add include \$devfsrules_(hide_all|unhide_basic|unhide_login)$`)
)

// checkDevfsRulesFile validates the devfs ruleset the daemon leaves beside a jail's configuration: the rules JailOps
// renders and nothing else (hide everything, include the stock sets, unhide known device paths).
func checkDevfsRulesFile(text string) error {
	if len(text) > 16<<10 {
		return invalid("devfs.rules is too large")
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "", strings.HasPrefix(line, "#"), line == "add hide", reDevfsInclude.MatchString(line):
		case reDevfsSection.MatchString(line):
			m := reDevfsSection.FindStringSubmatch(line)
			n, _ := strconv.Atoi(m[1])
			if m[1] != m[2] || n < DevfsRulesetMin || n > DevfsRulesetMax {
				return forbidden("devfs.rules: ruleset %q is not in the private range", line)
			}
		case reDevfsAddPath.MatchString(line):
			m := reDevfsAddPath.FindStringSubmatch(line)
			if m[2] == "unhide" && !render.DevfsUnhidable(m[1]) {
				return forbidden("devfs.rules: %q is not a device JailOps may unhide", m[1])
			}
		default:
			return forbidden("devfs.rules: line %q is not one JailOps writes", line)
		}
	}
	return nil
}
