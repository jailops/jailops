package replicate

import (
	"fmt"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// Kind is the type of transfer a plan describes.
type Kind string

const (
	KindNoop        Kind = "noop" // the target snapshot is already the chain head
	KindFull        Kind = "full"
	KindIncremental Kind = "incremental"
)

// Ref points at a snapshot or bookmark on the source.
type Ref struct {
	Name     string
	GUID     uint64
	Bookmark bool
}

// BrokenPolicy says what to do when the chain cannot continue.
type BrokenPolicy int

const (
	// FailOnBroken returns api.CodeChainBroken.
	FailOnBroken BrokenPolicy = iota
	// NewChainOnBroken starts a new chain with a full send. The old chain and
	// all its replicas stay untouched.
	NewChainOnBroken
)

// Op is a source-side operation.
type Op string

const (
	OpHold            Op = "hold"
	OpRelease         Op = "release"
	OpBookmark        Op = "bookmark"
	OpDestroyBookmark Op = "destroy-bookmark"
)

// Step is one source-side operation. Snapshot or Bookmark is the target.
type Step struct {
	Op       Op
	Snapshot string
	Tag      string
	Bookmark string
}

func (s Step) String() string {
	return fmt.Sprintf("%s snap=%s tag=%s bm=%s", s.Op, s.Snapshot, s.Tag, s.Bookmark)
}

// PlanInput is everything BuildPlan needs; it performs no I/O.
type PlanInput struct {
	Chain     Chain
	Snapshots []SnapInfo // source snapshots, oldest first
	Bookmarks []SnapInfo
	// To is the short snapshot name to send; empty means the newest.
	To  string
	Raw bool
	// Resume is the receiver's interrupted state, if any.
	Resume   *ResumeInfo
	OnBroken BrokenPolicy
	// SourceControl is true when this process may hold and bookmark on the
	// source (push mode, or a local source). A pulling target with a read-only
	// credential cannot, and the host's snapshot routine must bookmark.
	SourceControl bool
}

// Plan describes one transfer. It is data: nothing here has run.
type Plan struct {
	Kind    Kind
	ChainID string
	// NewChain is true for a first full or after a fork under NewChainOnBroken.
	NewChain bool
	From     *Ref
	To       Ref
	Raw      bool
	// Resume is set only when the receiver's interrupted state matches this
	// exact transfer.
	Resume *ResumeInfo
	// AbortResume is set when the receiver has partial state for a different
	// transfer that must be discarded first.
	AbortResume bool
	Fork        ForkReport
	// Before runs on the source before sending; After after a verified
	// receive; OnFailure when the transfer fails. Ordering matters: the new
	// bookmark is created before any hold is released.
	Before, After, OnFailure []Step
}

// SendTag is the source-side hold tag of a chain.
func SendTag(chain string) string { return "jailops-send-" + chain }

// BookmarkName is the source bookmark that keeps the base for chain at guid.
func BookmarkName(chain string, guid uint64) string {
	return "jailops-" + chain + "-" + hex16(guid)
}

// IsOwnBookmark reports whether name is a JailOps base bookmark of chain.
func IsOwnBookmark(chain, name string) bool {
	p := "jailops-" + chain + "-"
	if !strings.HasPrefix(name, p) || len(name) != len(p)+16 {
		return false
	}
	for _, c := range name[len(p):] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// BuildPlan decides full versus incremental and the ordered source steps.
func BuildPlan(in PlanInput) (Plan, error) {
	c := in.Chain
	if !ValidID(c.ID) {
		return Plan{}, invalid("chain id %q is not valid", c.ID)
	}
	var to *SnapInfo
	if in.To == "" {
		if len(in.Snapshots) == 0 {
			return Plan{}, api.Errorf(api.CodeNotFound, "source has no snapshots")
		}
		to = &in.Snapshots[len(in.Snapshots)-1]
	} else {
		for i := range in.Snapshots {
			if in.Snapshots[i].Name == in.To {
				to = &in.Snapshots[i]
			}
		}
		if to == nil {
			return Plan{}, api.Errorf(api.CodeNotFound, "snapshot %q not on source", in.To)
		}
	}
	p := Plan{ChainID: c.ID, Raw: in.Raw, To: Ref{Name: to.Name, GUID: to.GUID}}
	if len(c.Received) > 0 && c.Raw != in.Raw {
		return Plan{}, invalid("chain %q is raw=%v; a stream with raw=%v cannot continue it", c.ID, c.Raw, in.Raw)
	}
	last, hasLast := c.Last()
	switch {
	case !hasLast:
		p.Kind, p.NewChain = KindFull, true
	case last.GUID == to.GUID && !last.Deleted:
		p.Kind = KindNoop
		return p, nil
	default:
		p.Fork = DetectFork(c, in.Snapshots, in.Bookmarks)
		if p.Fork.Kind != ForkNone {
			if in.OnBroken != NewChainOnBroken {
				return Plan{}, broken("chain %q cannot continue: source history diverged (%s)", c.ID, p.Fork.Kind).
					WithDetail("fork", string(p.Fork.Kind)).
					WithHint("start a new chain with a full send; the old chain is kept")
			}
			p.ChainID = c.ID + "-f" + hex16(to.GUID)[8:]
			p.Kind, p.NewChain = KindFull, true
			break
		}
		if _, dup := c.Find(to.GUID); dup {
			return Plan{}, invalid("snapshot %q is already on chain %q", to.Name, c.ID)
		}
		var from *Ref
		for _, s := range in.Snapshots {
			if s.GUID == last.GUID {
				from = &Ref{Name: s.Name, GUID: s.GUID}
				fromInfo := s
				if !to.Created.After(fromInfo.Created) {
					return Plan{}, invalid("snapshot %q is not newer than the chain head %q", to.Name, s.Name)
				}
			}
		}
		if from == nil {
			for _, b := range in.Bookmarks {
				if b.GUID == last.GUID {
					from = &Ref{Name: b.Name, GUID: b.GUID, Bookmark: true}
					if !to.Created.After(b.Created) {
						return Plan{}, invalid("snapshot %q is not newer than the chain head", to.Name)
					}
				}
			}
		}
		p.Kind, p.From = KindIncremental, from
	}
	if r := in.Resume; r != nil {
		var fg uint64
		if p.From != nil {
			fg = p.From.GUID
		}
		if r.ToGUID == p.To.GUID && r.FromGUID == fg && !(p.NewChain && p.ChainID != c.ID) {
			p.Resume = r
		} else {
			p.AbortResume = true
		}
	}
	if in.SourceControl {
		tag := SendTag(p.ChainID)
		p.Before = append(p.Before, Step{Op: OpHold, Snapshot: p.To.Name, Tag: tag})
		p.OnFailure = append(p.OnFailure, Step{Op: OpRelease, Snapshot: p.To.Name, Tag: tag})
		after := []Step{{Op: OpBookmark, Snapshot: p.To.Name, Bookmark: BookmarkName(p.ChainID, p.To.GUID)}}
		after = append(after, Step{Op: OpRelease, Snapshot: p.To.Name, Tag: tag})
		if p.From != nil && !p.From.Bookmark {
			p.Before = append(p.Before, Step{Op: OpHold, Snapshot: p.From.Name, Tag: tag})
			p.OnFailure = append(p.OnFailure, Step{Op: OpRelease, Snapshot: p.From.Name, Tag: tag})
			after = append(after, Step{Op: OpRelease, Snapshot: p.From.Name, Tag: tag})
		}
		for _, b := range in.Bookmarks {
			if IsOwnBookmark(p.ChainID, b.Name) && b.GUID != p.To.GUID {
				after = append(after, Step{Op: OpDestroyBookmark, Bookmark: b.Name})
			}
		}
		p.After = after
	}
	return p, nil
}
