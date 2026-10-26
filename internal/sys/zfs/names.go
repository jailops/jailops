// Package zfs wraps the zfs(8) and zpool(8) command line behind a Client
// interface. Every name and property that reaches an argv is validated here.
package zfs

import (
	"strings"

	"jailops.net/jailops/sdk/api"
)

const maxNameLen = 255

// Kind of a ZFS object name.
type Kind int

const (
	KindDataset  Kind = iota // pool/a/b
	KindSnapshot             // pool/a/b@snap
	KindBookmark             // pool/a/b#mark
)

func nameChar(r byte) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
		r == '_' || r == '-' || r == '.' || r == ':'
}

func validComponent(c string) bool {
	if c == "" || c == "." || c == ".." || c[0] == '-' {
		return false
	}
	for i := 0; i < len(c); i++ {
		if !nameChar(c[i]) {
			return false
		}
	}
	return true
}

// validDatasetPath checks pool/comp/comp. The pool name must start with a
// letter. Spaces and '%' are legal in ZFS but are rejected by JailOps.
func validDatasetPath(p string) bool {
	if p == "" {
		return false
	}
	parts := strings.Split(p, "/")
	for i, c := range parts {
		if !validComponent(c) {
			return false
		}
		if i == 0 && !(c[0] >= 'a' && c[0] <= 'z' || c[0] >= 'A' && c[0] <= 'Z') {
			return false
		}
	}
	return true
}

func invalidName(kind, name string) error {
	return api.Errorf(api.CodeInvalid, "invalid zfs %s name %q", kind, name)
}

// ValidateDataset accepts filesystem or volume names (and pool names).
func ValidateDataset(name string) error {
	if len(name) > maxNameLen || !validDatasetPath(name) {
		return invalidName("dataset", name)
	}
	return nil
}

// ValidateSnapshot accepts dataset@snap.
func ValidateSnapshot(name string) error {
	ds, snap, ok := strings.Cut(name, "@")
	if !ok || len(name) > maxNameLen || !validDatasetPath(ds) || !validComponent(snap) {
		return invalidName("snapshot", name)
	}
	return nil
}

// ValidateBookmark accepts dataset#mark.
func ValidateBookmark(name string) error {
	ds, mark, ok := strings.Cut(name, "#")
	if !ok || len(name) > maxNameLen || !validDatasetPath(ds) || !validComponent(mark) {
		return invalidName("bookmark", name)
	}
	return nil
}

// ValidateAny accepts any of the three and returns which one it was.
func ValidateAny(name string) (Kind, error) {
	switch {
	case strings.Contains(name, "@"):
		return KindSnapshot, ValidateSnapshot(name)
	case strings.Contains(name, "#"):
		return KindBookmark, ValidateBookmark(name)
	}
	return KindDataset, ValidateDataset(name)
}

// SplitSnapshot splits dataset@snap.
func SplitSnapshot(name string) (dataset, snap string) {
	dataset, snap, _ = strings.Cut(name, "@")
	return
}

// ValidateProperty checks a property name. User properties need a colon and
// lowercase letters, digits and "_-.:" (as ZFS requires); native names are
// lowercase words, optionally with "@snapshot" for written@.
func ValidateProperty(p string) error {
	bad := func() error { return api.Errorf(api.CodeInvalid, "invalid zfs property name %q", p) }
	if p == "" || len(p) > 255 || p[0] == '-' {
		return bad()
	}
	base, suffix, hasAt := strings.Cut(p, "@")
	if hasAt && !(base == "written" || base == "feature") && !strings.HasPrefix(base, "used") {
		return bad()
	}
	for i := 0; i < len(base); i++ {
		c := base[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.' || c == ':') {
			return bad()
		}
	}
	if hasAt && !validComponent(suffix) {
		// written@snap uses a short snapshot name; feature@name may contain dots.
		return bad()
	}
	return nil
}

// ValidatePropValue rejects values that cannot be passed safely: NUL, newline
// and tab would corrupt the parseable tab-separated output and any later
// reading of the value. Values are passed as one argv element prop=value, so
// a leading '-' is harmless.
func ValidatePropValue(v string) error {
	if len(v) > 8192 || strings.ContainsAny(v, "\x00\n\r\t") {
		return api.Errorf(api.CodeInvalid, "invalid zfs property value")
	}
	return nil
}

func validateProps(m map[string]string) error {
	for k, v := range m {
		if err := ValidateProperty(k); err != nil {
			return err
		}
		if err := ValidatePropValue(v); err != nil {
			return err
		}
	}
	return nil
}

// ValidateTag checks a hold tag.
func ValidateTag(tag string) error {
	if tag == "" || len(tag) > 255 || !validComponent(tag) {
		return api.Errorf(api.CodeInvalid, "invalid zfs hold tag %q", tag)
	}
	return nil
}

// ValidateResumeToken accepts the opaque token from receive_resume_token.
func ValidateResumeToken(t string) error {
	if t == "" || len(t) > 8192 || t[0] == '-' {
		return api.Errorf(api.CodeInvalid, "invalid resume token")
	}
	for i := 0; i < len(t); i++ {
		c := t[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return api.Errorf(api.CodeInvalid, "invalid resume token")
		}
	}
	return nil
}

// ValidatePool checks a pool name.
func ValidatePool(p string) error {
	if strings.Contains(p, "/") || ValidateDataset(p) != nil {
		return invalidName("pool", p)
	}
	return nil
}
