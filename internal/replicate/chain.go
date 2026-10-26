package replicate

import (
	"time"

	"jailops.net/jailops/sdk/api"
)

// Received is one snapshot recorded on a chain.
type Received struct {
	Snapshot string
	GUID     uint64
	// FromGUID is zero for a full send.
	FromGUID uint64
	Full     bool
	// KeyID is the host key current when the snapshot was taken (raw sends).
	KeyID string
	At    time.Time
	Bytes int64
	// Deleted is set when retention or an admin removed the snapshot. The entry
	// stays as history; it can no longer serve as a base.
	Deleted bool
}

// Chain is the receiver's record of one stream of replicas for one dataset.
type Chain struct {
	ID       string
	HostUUID string
	JailUUID string
	// Dataset is the receiving dataset on the target.
	Dataset string
	// Source is the dataset on the host.
	Source   string
	Raw      bool
	Received []Received
}

// Clone returns a deep copy.
func (c Chain) Clone() Chain {
	c.Received = append([]Received(nil), c.Received...)
	return c
}

// Last returns the most recently received snapshot.
func (c Chain) Last() (Received, bool) {
	if len(c.Received) == 0 {
		return Received{}, false
	}
	return c.Received[len(c.Received)-1], true
}

// Live returns entries whose snapshot still exists.
func (c Chain) Live() []Received {
	var l []Received
	for _, r := range c.Received {
		if !r.Deleted {
			l = append(l, r)
		}
	}
	return l
}

// Find returns the entry with the given guid.
func (c Chain) Find(guid uint64) (Received, bool) {
	for _, r := range c.Received {
		if r.GUID == guid {
			return r, true
		}
	}
	return Received{}, false
}

func broken(format string, a ...any) *api.Error { return api.Errorf(api.CodeChainBroken, format, a...) }

// CheckContinuity decides whether a stream with the given source guid may be
// received next. A chain continues only when fromGUID equals the guid of the
// last received snapshot. Everything else is api.CodeChainBroken: the chain
// forks (the host's history diverged) or the caller must start a new chain
// with a full send.
func (c Chain) CheckContinuity(fromGUID uint64) error {
	last, ok := c.Last()
	switch {
	case !ok && fromGUID == 0:
		return nil
	case !ok:
		return broken("chain %q is empty but the stream is incremental", c.ID).
			WithHint("send a full stream")
	case fromGUID == 0:
		return broken("chain %q already has snapshots; a full stream would overwrite it", c.ID).
			WithHint("start a new chain")
	case last.Deleted:
		return broken("chain %q lost its last snapshot", c.ID).WithHint("start a new chain")
	case fromGUID != last.GUID:
		return broken("incremental source guid %d does not match last received guid %d", fromGUID, last.GUID).
			WithDetail("expected", itoa(last.GUID)).WithDetail("got", itoa(fromGUID)).
			WithHint("the host was restored or its snapshots were replaced; start a new chain with a full send")
	}
	return nil
}

// Append records a received snapshot after checking continuity.
func (c *Chain) Append(r Received) error {
	if err := c.CheckContinuity(r.FromGUID); err != nil {
		return err
	}
	if _, dup := c.Find(r.GUID); dup {
		return broken("guid %d is already on chain %q", r.GUID, c.ID)
	}
	r.Full = r.FromGUID == 0
	c.Received = append(c.Received, r)
	return nil
}

// ForkKind classifies how a source relates to a chain.
type ForkKind string

const (
	ForkNone ForkKind = ""
	// ForkRestored: the last received snapshot is gone from the source but an
	// earlier one is still there, or snapshot names exist with different guids.
	// This is what a host restored from older state looks like.
	ForkRestored ForkKind = "restored"
	// ForkNoCommon: nothing received is on the source any more.
	ForkNoCommon ForkKind = "no_common"
)

// ForkReport is the result of DetectFork.
type ForkReport struct {
	Kind ForkKind
	// CommonGUID is the newest received snapshot still present on the source
	// (zero if none).
	CommonGUID uint64
	// Lost lists live received snapshots newer than the common one: replicas
	// of history the host no longer has.
	Lost []string
	// Collisions lists snapshot names present on both sides with different guids.
	Collisions []string
}

// DetectFork compares a chain with the source's snapshots and bookmarks.
func DetectFork(c Chain, snaps, bookmarks []SnapInfo) ForkReport {
	last, ok := c.Last()
	if !ok {
		return ForkReport{}
	}
	onSource := map[uint64]bool{}
	byName := map[string]uint64{}
	for _, s := range snaps {
		onSource[s.GUID] = true
		byName[s.Name] = s.GUID
	}
	for _, b := range bookmarks {
		onSource[b.GUID] = true
	}
	if !last.Deleted && onSource[last.GUID] {
		return ForkReport{}
	}
	var rep ForkReport
	live := c.Live()
	idx := -1
	for i := len(live) - 1; i >= 0; i-- {
		if onSource[live[i].GUID] {
			rep.CommonGUID, idx = live[i].GUID, i
			break
		}
	}
	for _, r := range live[idx+1:] {
		rep.Lost = append(rep.Lost, r.Snapshot)
	}
	for _, r := range live {
		if g, ok := byName[r.Snapshot]; ok && g != r.GUID {
			rep.Collisions = append(rep.Collisions, r.Snapshot)
		}
	}
	if rep.CommonGUID != 0 || len(rep.Collisions) > 0 {
		rep.Kind = ForkRestored
	} else {
		rep.Kind = ForkNoCommon
	}
	return rep
}
