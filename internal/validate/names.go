// Package validate checks user-supplied specs and names. Validators return
// *api.Error with Code api.CodeInvalid and Detail["field"] naming the field.
// Other packages (notably render) rely on these shapes to keep configuration
// files free of injected syntax.
package validate

import (
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// Invalid builds a CodeInvalid error naming field.
func Invalid(field, format string, args ...any) *api.Error {
	return api.Errorf(api.CodeInvalid, format, args...).WithDetail("field", field)
}

var (
	nameRE     = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	labelRE    = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)
	ifnameRE   = regexp.MustCompile(`^[a-z][a-z0-9]{0,14}$`)
	setNameRE  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,23}$`)
	tagRE      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,23}$`)
	ruleNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,63}$`)
	pkgRE      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9+._-]{0,127}$`)
	envKeyRE   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	macRE      = regexp.MustCompile(`^[0-9a-f]{2}(:[0-9a-f]{2}){5}$`)
	imageNmRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	aliasRE    = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,62}$`)
	osrelRE    = regexp.MustCompile(`^[0-9]{1,3}\.[0-9]{1,3}(-[A-Za-z0-9]{1,16}(-p[0-9]{1,4})?)?$`)
)

// Name validates a jail, network or policy name: DNS-label-like, lower case.
func Name(field, s string) error {
	if !nameRE.MatchString(s) {
		return Invalid(field, "%s %q must match ^[a-z][a-z0-9-]{0,62}$", field, s)
	}
	return nil
}

// JailName validates a jail name.
func JailName(s string) error { return Name("name", s) }

// Hostname validates a DNS hostname of at most 253 characters.
func Hostname(field, s string) error {
	if s == "" || len(s) > 253 {
		return Invalid(field, "%s must be 1-253 characters", field)
	}
	for _, l := range strings.Split(s, ".") {
		if !labelRE.MatchString(l) {
			return Invalid(field, "%s %q is not a valid DNS name", field, s)
		}
	}
	return nil
}

// IfName validates a FreeBSD interface name as JailOps accepts it
// (lower case letters and digits, at most 15 characters).
func IfName(field, s string) error {
	if !ifnameRE.MatchString(s) {
		return Invalid(field, "%s %q is not a valid interface name", field, s)
	}
	return nil
}

// SetName validates an address set name (a PF table name component).
func SetName(field, s string) error {
	if !setNameRE.MatchString(s) {
		return Invalid(field, "%s %q must match ^[a-z][a-z0-9_]{0,23}$", field, s)
	}
	return nil
}

// Tag validates a firewall tag.
func Tag(field, s string) error {
	if !tagRE.MatchString(s) || strings.HasSuffix(s, "-") {
		return Invalid(field, "%s %q must match ^[a-z0-9][a-z0-9-]{0,23}$", field, s)
	}
	return nil
}

// RuleName validates a free-form rule label.
func RuleName(field, s string) error {
	if !ruleNameRE.MatchString(s) {
		return Invalid(field, "%s %q must match ^[A-Za-z0-9][A-Za-z0-9 ._-]{0,63}$", field, s)
	}
	return nil
}

// MAC validates a lower-case colon separated MAC address.
func MAC(field, s string) error {
	if !macRE.MatchString(s) {
		return Invalid(field, "%s %q must be lower case aa:bb:cc:dd:ee:ff", field, s)
	}
	b0, _ := strconv.ParseUint(s[:2], 16, 8)
	if b0&1 != 0 {
		return Invalid(field, "%s %q is a multicast address", field, s)
	}
	return nil
}

// OSRelease validates a jail osrelease such as "15.0-RELEASE" or "14.2-RELEASE-p3".
func OSRelease(field, s string) error {
	if !osrelRE.MatchString(s) {
		return Invalid(field, "%s %q is not a release such as 15.0-RELEASE", field, s)
	}
	return nil
}

// AbsPath validates an absolute path restricted to a conservative character
// set, without "." or ".." segments. Such a path needs no quoting in
// jail.conf, fstab or shell.
func AbsPath(field, s string) error {
	if len(s) == 0 || len(s) > 1024 || s[0] != '/' {
		return Invalid(field, "%s must be an absolute path", field)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("/._+@:=,-", c) >= 0
		if !ok {
			return Invalid(field, "%s contains the unsupported character %q", field, string(c))
		}
	}
	if s != "/" {
		for _, seg := range strings.Split(s[1:], "/") {
			if seg == "" || seg == "." || seg == ".." {
				return Invalid(field, "%s must be a clean path without empty, '.' or '..' segments", field)
			}
		}
	}
	return nil
}

// ImageRef is a parsed image reference.
type ImageRef struct {
	Name string
	// Alias is the movable alias ("stable"); empty when Generation or the
	// default alias is meant.
	Alias string
	// Generation is the pinned generation number; zero when not pinned.
	Generation int
	Pinned     bool
}

// ParseImageRef parses "name", "name@alias" or "name#g<N>".
func ParseImageRef(s string) (ImageRef, error) {
	const f = "image"
	if i := strings.IndexByte(s, '#'); i >= 0 {
		name, gen := s[:i], s[i+1:]
		if !imageNmRE.MatchString(name) {
			return ImageRef{}, Invalid(f, "image name %q is invalid", name)
		}
		if len(gen) < 2 || gen[0] != 'g' || len(gen) > 10 || (gen[1] == '0') {
			return ImageRef{}, Invalid(f, "generation %q must look like g3", gen)
		}
		n, err := strconv.Atoi(gen[1:])
		if err != nil || n < 1 || gen[1] == '+' || gen[1] == '-' {
			return ImageRef{}, Invalid(f, "generation %q must look like g3", gen)
		}
		return ImageRef{Name: name, Generation: n, Pinned: true}, nil
	}
	name, alias, has := strings.Cut(s, "@")
	if !imageNmRE.MatchString(name) {
		return ImageRef{}, Invalid(f, "image reference %q is invalid", s)
	}
	if has && !aliasRE.MatchString(alias) {
		return ImageRef{}, Invalid(f, "image alias %q is invalid", alias)
	}
	return ImageRef{Name: name, Alias: alias}, nil
}

// String formats the reference canonically.
func (r ImageRef) String() string {
	switch {
	case r.Pinned:
		return fmt.Sprintf("%s#g%d", r.Name, r.Generation)
	case r.Alias != "":
		return r.Name + "@" + r.Alias
	}
	return r.Name
}

// ParseSize parses "512M", "2G", "100" (bytes), optionally with a trailing
// "B" or "iB". Units are binary (K=1024). Zero is rejected.
func ParseSize(s string) (uint64, error) {
	t := s
	switch {
	case strings.HasSuffix(t, "iB"):
		t = t[:len(t)-2]
		if t == "" || t[len(t)-1] < 'A' {
			return 0, Invalid("size", "size %q is invalid", s)
		}
	case strings.HasSuffix(t, "B"):
		t = t[:len(t)-1]
	}
	mult := uint64(1)
	if t != "" {
		switch t[len(t)-1] {
		case 'K', 'k':
			mult = 1 << 10
		case 'M', 'm':
			mult = 1 << 20
		case 'G', 'g':
			mult = 1 << 30
		case 'T', 't':
			mult = 1 << 40
		case 'P', 'p':
			mult = 1 << 50
		}
		if mult != 1 {
			t = t[:len(t)-1]
		}
	}
	if t == "" || len(t) > 18 {
		return 0, Invalid("size", "size %q is invalid", s)
	}
	for i := 0; i < len(t); i++ {
		if t[i] < '0' || t[i] > '9' {
			return 0, Invalid("size", "size %q is invalid", s)
		}
	}
	n, _ := strconv.ParseUint(t, 10, 64)
	if n == 0 {
		return 0, Invalid("size", "size %q must be greater than zero", s)
	}
	if n > ^uint64(0)/mult {
		return 0, Invalid("size", "size %q is too large", s)
	}
	return n * mult, nil
}

// addr parses an IP address without zone.
func addr(field, s string) (netip.Addr, error) {
	a, err := netip.ParseAddr(s)
	if err != nil || a.Zone() != "" || a.Is4In6() {
		return netip.Addr{}, Invalid(field, "%s %q is not a valid IP address", field, s)
	}
	return a, nil
}
