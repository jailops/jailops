// Package zfsrepl holds the pure logic of prototype 4: parsing of zfs output, guid
// continuity, resume tokens, classification of `zfs recv` failures and the list of
// assumptions under test. It is unit tested on Linux. No FreeBSD behaviour is asserted
// here; error message texts are matched loosely and are UNVERIFIED: they come from
// OpenZFS source memory, not from a run.
package zfsrepl

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Snap is one line of `zfs list -H -p -t snapshot -o name,guid,createtxg`.
type Snap struct {
	Dataset string
	Name    string // part after '@'
	Guid    string
	Txg     uint64
}

// ParseSnaps parses tab separated name, guid, createtxg lines.
func ParseSnaps(out string) ([]Snap, error) {
	var res []Snap
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		f := strings.Split(l, "\t")
		if len(f) != 3 {
			return nil, fmt.Errorf("bad snapshot line %q", l)
		}
		ds, sn, ok := strings.Cut(f[0], "@")
		if !ok {
			return nil, fmt.Errorf("not a snapshot: %q", f[0])
		}
		txg, err := strconv.ParseUint(f[2], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("bad txg in %q", l)
		}
		res = append(res, Snap{ds, sn, f[1], txg})
	}
	return res, nil
}

// Latest returns the snapshot with the highest txg.
func Latest(s []Snap) (Snap, bool) {
	var best Snap
	found := false
	for _, x := range s {
		if !found || x.Txg > best.Txg {
			best, found = x, true
		}
	}
	return best, found
}

// ByName finds a snapshot by its short name.
func ByName(s []Snap, name string) (Snap, bool) {
	for _, x := range s {
		if x.Name == name {
			return x, true
		}
	}
	return Snap{}, false
}

// ParseNameGuid parses `zfs list -H -p -o name,guid` (snapshots or bookmarks).
func ParseNameGuid(out string) (map[string]string, error) {
	m := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		f := strings.Split(l, "\t")
		if len(f) != 2 {
			return nil, fmt.Errorf("bad line %q", l)
		}
		m[f[0]] = f[1]
	}
	return m, nil
}

// Hold is one line of `zfs holds -H`.
type Hold struct{ Name, Tag string }

// ParseHolds parses `zfs holds -H` output (name, tag, timestamp).
func ParseHolds(out string) []Hold {
	var h []Hold
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(l, "\t")
		if len(f) >= 2 {
			h = append(h, Hold{f[0], f[1]})
		}
	}
	return h
}

// HasHold reports whether tag is on name.
func HasHold(h []Hold, tag string) bool {
	for _, x := range h {
		if x.Tag == tag {
			return true
		}
	}
	return false
}

var tokenRE = regexp.MustCompile(`^1-[0-9a-f]+(-[0-9a-f]+)+$`)

// ResumeToken interprets the receive_resume_token property value. "-" or "" mean none.
// UNVERIFIED: the token shape (1-<hex>-...) is from OpenZFS memory; ValidToken is only a
// sanity check that keeps garbage (and path-like strings) out of argv.
func ResumeToken(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if v == "" || v == "-" {
		return "", false
	}
	return v, true
}

// ValidToken is a conservative syntax check.
func ValidToken(t string) bool { return tokenRE.MatchString(t) }

// Continuity implements the chain rule of design section 9: an incremental may continue a
// chain only when the guid of the incremental base on the source equals the guid of the
// most recent snapshot on the receiver. Names are never compared, only guids.
func Continuity(baseGuid, receiverLatestGuid string) error {
	switch {
	case baseGuid == "" || baseGuid == "-":
		return fmt.Errorf("source base guid unknown")
	case receiverLatestGuid == "" || receiverLatestGuid == "-":
		return fmt.Errorf("receiver has no snapshot: full send required")
	case baseGuid != receiverLatestGuid:
		return fmt.Errorf("fork: base guid %s != receiver latest guid %s", baseGuid, receiverLatestGuid)
	}
	return nil
}

// RecvKind classifies a failed `zfs recv`.
type RecvKind string

const (
	RecvOK            RecvKind = "ok"
	RecvExists        RecvKind = "destination-exists"
	RecvModified      RecvKind = "destination-modified"
	RecvBaseMismatch  RecvKind = "incremental-base-mismatch"
	RecvOriginMissing RecvKind = "origin-missing"
	RecvKeyProblem    RecvKind = "key"
	RecvOther         RecvKind = "other"
)

// ClassifyRecv maps exit status and stderr to a RecvKind. UNVERIFIED: wording of OpenZFS
// messages; matching is loose and case-insensitive, and the prototype always prints the
// raw stderr so a mismatch of wording is visible.
func ClassifyRecv(code int, stderr string) RecvKind {
	if code == 0 {
		return RecvOK
	}
	s := strings.ToLower(stderr)
	switch {
	case strings.Contains(s, "local origin") || strings.Contains(s, "origin") && strings.Contains(s, "does not exist"):
		return RecvOriginMissing
	case strings.Contains(s, "exists") && (strings.Contains(s, "-f") || strings.Contains(s, "overwrite")):
		return RecvExists
	case strings.Contains(s, "has been modified"):
		return RecvModified
	case strings.Contains(s, "does not match incremental source") || strings.Contains(s, "most recent snapshot"):
		return RecvBaseMismatch
	case strings.Contains(s, "key"):
		return RecvKeyProblem
	}
	return RecvOther
}

// Expectation is one assumption under test.
type Expectation struct {
	ID    string
	Claim string
}

// Expectations is the matrix of design 9 / section 16 row 4 settled by this prototype.
func Expectations() []Expectation {
	return []Expectation{
		{"E1", "plain `zfs send` of a clone (no -R) is a full stream and the receiver needs no origin"},
		{"E2", "`zfs send -R` of a clone alone: whether the receiver needs the origin (observed, not asserted)"},
		{"E3", "`zfs send -R` of a tree that contains image and clone recreates the clone relationship on the receiver"},
		{"E4", "a bookmark is a valid incremental base after the source snapshot was destroyed"},
		{"E5", "guid of a received snapshot equals the source guid; guid comparison detects a recreated snapshot of the same name"},
		{"E6", "`zfs hold` makes `zfs destroy` of the snapshot fail; release permits it"},
		{"E7", "interrupted `recv -s` leaves a receive_resume_token; `send -t` completes it; `recv -A` aborts"},
		{"E8", "raw send of a natively encrypted dataset: the receiver never needs the key and cannot mount; load-key later works"},
		{"E9", "recv without -F refuses to overwrite an existing dataset and a divergent (modified) receiver"},
		{"E10", "a plain (non raw) send of an encrypted dataset to an unencrypted parent lands as plaintext (why raw is required)"},
	}
}
