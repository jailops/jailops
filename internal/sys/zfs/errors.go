package zfs

import (
	"errors"
	"strings"

	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/sdk/api"
)

// Detail "reason" values carried by the *api.Error returned for failed
// commands. Use the Is* helpers instead of comparing strings.
const (
	ReasonBusy       = "busy"
	ReasonHasClones  = "has_clones"
	ReasonHasKids    = "has_children"
	ReasonHeld       = "held"
	ReasonNewerSnaps = "newer_snapshots"
	ReasonNoSpace    = "no_space"
	ReasonDiverged   = "diverged"
)

func reason(err error) string {
	var e *api.Error
	if errors.As(err, &e) {
		return e.Detail["reason"]
	}
	return ""
}

// IsBusy reports a "dataset is busy" failure (mounted, in use).
func IsBusy(err error) bool { return reason(err) == ReasonBusy }

// IsHasClones reports "snapshot has dependent clones".
func IsHasClones(err error) bool { return reason(err) == ReasonHasClones }

// IsHeld reports a snapshot that is held.
func IsHeld(err error) bool { return reason(err) == ReasonHeld }

// IsNotFound reports a missing dataset, snapshot or pool.
func IsNotFound(err error) bool { return api.IsCode(err, api.CodeNotFound) }

// mapError converts a runner error to an *api.Error. It matches the stable
// parts of zfs(8) diagnostics; unknown failures become CodeInternal with the
// stderr text. UNVERIFIED: wording matched against OpenZFS 2.x messages from
// memory, not run on FreeBSD.
func mapError(err error) error {
	if err == nil {
		return nil
	}
	var ae *api.Error
	if errors.As(err, &ae) {
		return err
	}
	ee, ok := run.IsExit(err)
	if !ok {
		return api.Errorf(api.CodeInternal, "zfs: %v", err)
	}
	msg := strings.TrimSpace(ee.Stderr)
	low := strings.ToLower(msg)
	mk := func(code api.Code, why string) error {
		e := api.Errorf(code, "%s", msg)
		if why != "" {
			e = e.WithDetail("reason", why)
		}
		return e
	}
	switch {
	case strings.Contains(low, "dataset does not exist"), strings.Contains(low, "no such pool"),
		strings.Contains(low, "does not exist"), strings.Contains(low, "no such snapshot"):
		return mk(api.CodeNotFound, "")
	case strings.Contains(low, "already exists"):
		return mk(api.CodeAlreadyExists, "")
	case strings.Contains(low, "dependent clones"), strings.Contains(low, "has clones"):
		return mk(api.CodeBusy, ReasonHasClones)
	case strings.Contains(low, "it's being held"), strings.Contains(low, "snapshot is cloned"):
		if strings.Contains(low, "cloned") {
			return mk(api.CodeBusy, ReasonHasClones)
		}
		return mk(api.CodeBusy, ReasonHeld)
	case strings.Contains(low, "has children"):
		return mk(api.CodeBusy, ReasonHasKids)
	case strings.Contains(low, "more recent snapshots"):
		return mk(api.CodeBusy, ReasonNewerSnaps)
	case strings.Contains(low, "is busy"), strings.Contains(low, "pool is busy"):
		return mk(api.CodeBusy, ReasonBusy)
	case strings.Contains(low, "most recent snapshot") && strings.Contains(low, "does not match incremental source"),
		strings.Contains(low, "destination has been modified"):
		return mk(api.CodeChainBroken, ReasonDiverged)
	case strings.Contains(low, "out of space"), strings.Contains(low, "no space left"), strings.Contains(low, "quota exceeded"):
		return mk(api.CodePoolExhausted, ReasonNoSpace)
	case strings.Contains(low, "permission denied"), strings.Contains(low, "operation not permitted"):
		return mk(api.CodeForbidden, "")
	case strings.Contains(low, "invalid"), strings.Contains(low, "bad property"), strings.Contains(low, "invalid character"):
		return mk(api.CodeInvalid, "")
	}
	if msg == "" {
		msg = ee.Error()
	}
	return api.Errorf(api.CodeInternal, "%s", msg)
}
