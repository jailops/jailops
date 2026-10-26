package replicate

import (
	"context"
	"errors"
	"io"

	"jailops.net/jailops/sdk/api"
)

// Sender is the source side of a transfer.
type Sender interface {
	Snapshots(ctx context.Context, dataset string) ([]SnapInfo, error)
	Bookmarks(ctx context.Context, dataset string) ([]SnapInfo, error)
	Send(ctx context.Context, o SendOpts) (Stream, error)
}

// SourceKeeper holds and bookmarks on the source. A pulling target with a
// read-only credential has none; a pushing host has its local ZFS.
type SourceKeeper interface {
	Hold(ctx context.Context, dataset, snapshot, tag string) error
	Release(ctx context.Context, dataset, snapshot, tag string) error
	CreateBookmark(ctx context.Context, dataset, snapshot, bookmark string) error
	DestroyBookmark(ctx context.Context, dataset, bookmark string) error
}

// LocalSource adapts a ZFS to Sender and SourceKeeper.
type LocalSource struct{ ZFS ZFS }

func (l LocalSource) Snapshots(ctx context.Context, ds string) ([]SnapInfo, error) {
	return l.ZFS.Snapshots(ctx, ds)
}
func (l LocalSource) Bookmarks(ctx context.Context, ds string) ([]SnapInfo, error) {
	return l.ZFS.Bookmarks(ctx, ds)
}
func (l LocalSource) Send(ctx context.Context, o SendOpts) (Stream, error) {
	return l.ZFS.Send(ctx, o)
}
func (l LocalSource) Hold(ctx context.Context, ds, s, t string) error {
	return l.ZFS.Hold(ctx, ds, s, t)
}
func (l LocalSource) Release(ctx context.Context, ds, s, t string) error {
	return l.ZFS.Release(ctx, ds, s, t)
}
func (l LocalSource) CreateBookmark(ctx context.Context, ds, s, b string) error {
	return l.ZFS.CreateBookmark(ctx, ds, s, b)
}
func (l LocalSource) DestroyBookmark(ctx context.Context, ds, b string) error {
	return l.ZFS.DestroyBookmark(ctx, ds, b)
}

// Job is one replication of one dataset.
type Job struct {
	Sender   Sender
	Receiver Receiver
	// Keeper is used by Push only. Pull never holds or bookmarks on the
	// source (read-only credential): the host's snapshot routine must create
	// the base bookmark when it takes the snapshot.
	Keeper SourceKeeper
	// Source is the dataset on the host; Snapshot the short name to send
	// (empty means the newest).
	Source   string
	Snapshot string
	ChainID  string
	// HostUUID is only used to label the chain; the Receiver is already bound
	// to the authenticated host.
	HostUUID, JailUUID, Jail, Policy string
	Raw, Encrypted                   bool
	Keys                             KeyStatus
	Bundle                           api.RestoreBundle
	OnBroken                         BrokenPolicy
	Compat                           CompatChecker
	// ResumeAttempts is how many times one call resumes an interrupted
	// transfer before giving up. Zero means the caller resumes by calling again.
	ResumeAttempts int
}

// Result reports a transfer.
type Result struct {
	Plan     Plan
	Replica  Replica
	UpToDate bool
	Bytes    int64
	Compat   CompatResult
	// Resumed is true when a resume token was used.
	Resumed bool
	// Resumable is true after a failure that left resumable state.
	Resumable bool
	Warnings  []string
}

// Pull runs a transfer initiated by the receiving host.
func Pull(ctx context.Context, j Job) (Result, error) {
	j.Keeper = nil
	return run(ctx, j, false)
}

// Push runs a transfer initiated by the host.
func Push(ctx context.Context, j Job) (Result, error) { return run(ctx, j, j.Keeper != nil) }

const copyBuf = 256 << 10

func run(ctx context.Context, j Job, control bool) (res Result, err error) {
	if j.Sender == nil || j.Receiver == nil {
		return res, invalid("job needs a sender and a receiver")
	}
	if err := CheckRecoverable(j.Raw, j.Encrypted, j.Keys); err != nil {
		return res, err
	}
	st, err := j.Receiver.State(ctx, j.ChainID)
	if err != nil {
		return res, err
	}
	snaps, err := j.Sender.Snapshots(ctx, j.Source)
	if err != nil {
		return res, err
	}
	bms, err := j.Sender.Bookmarks(ctx, j.Source)
	if err != nil {
		return res, err
	}
	chain := st.Chain
	if chain.ID == "" {
		chain.ID = j.ChainID
	}
	plan, err := BuildPlan(PlanInput{Chain: chain, Snapshots: snaps, Bookmarks: bms, To: j.Snapshot, Raw: j.Raw,
		Resume: st.Resume, OnBroken: j.OnBroken, SourceControl: control})
	if err != nil {
		return res, err
	}
	res.Plan = plan
	if plan.Kind == KindNoop {
		res.UpToDate = true
		return res, nil
	}
	if res.Compat, err = runCompat(ctx, j.Compat, CompatRequest{Source: j.Source, Chain: plan.ChainID, Raw: j.Raw}); err != nil {
		return res, err
	}
	if plan.AbortResume {
		if err := j.Receiver.AbortResume(ctx, j.ChainID); err != nil {
			return res, err
		}
	}

	var rev Reverter
	defer func() {
		if rerr := rev.Revert(); rerr != nil {
			res.Warnings = append(res.Warnings, "cleanup: "+rerr.Error())
		}
	}()
	for _, s := range plan.Before {
		s := s
		if err := j.applyStep(ctx, s); err != nil {
			return res, err
		}
		if s.Op == OpHold {
			rev.Add(func() error {
				return j.Keeper.Release(context.WithoutCancel(ctx), j.Source, s.Snapshot, s.Tag)
			})
		}
	}

	for attempt := 0; ; attempt++ {
		var rr ReceiveResult
		rr, err = j.once(ctx, plan)
		if err == nil {
			res.Replica, res.Bytes = rr.Replica, rr.Bytes
			break
		}
		var ae *api.Error
		if errors.As(err, &ae) || ctx.Err() != nil {
			break // refused or cancelled, not a transient stream failure
		}
		cur, serr := j.Receiver.State(context.WithoutCancel(ctx), plan.ChainID)
		if serr != nil || cur.Resume == nil || cur.Resume.ToGUID != plan.To.GUID {
			break
		}
		res.Resumable = true
		if attempt >= j.ResumeAttempts {
			break
		}
		plan.Resume = cur.Resume
		res.Plan = plan
	}
	if err != nil {
		if cur, serr := j.Receiver.State(context.WithoutCancel(ctx), plan.ChainID); serr == nil && cur.Resume != nil {
			res.Resumable = true
		}
		return res, err
	}
	res.Resumable = false
	res.Resumed = plan.Resume != nil
	// Success: bookmark before releasing holds, then the deferred releases of
	// the reverter are redundant; run After in plan order and disarm.
	for _, s := range plan.After {
		if err := j.applyStep(ctx, s); err != nil {
			res.Warnings = append(res.Warnings, "source step "+s.String()+": "+err.Error())
			if s.Op == OpBookmark {
				// Keep the holds: without the bookmark the holds are the only
				// thing protecting the next incremental base.
				return res, nil
			}
		}
	}
	rev.Success()
	return res, nil
}

func (j Job) applyStep(ctx context.Context, s Step) error {
	if j.Keeper == nil {
		return nil
	}
	switch s.Op {
	case OpHold:
		return j.Keeper.Hold(ctx, j.Source, s.Snapshot, s.Tag)
	case OpRelease:
		return j.Keeper.Release(ctx, j.Source, s.Snapshot, s.Tag)
	case OpBookmark:
		return j.Keeper.CreateBookmark(ctx, j.Source, s.Snapshot, s.Bookmark)
	case OpDestroyBookmark:
		return j.Keeper.DestroyBookmark(ctx, j.Source, s.Bookmark)
	}
	return invalid("unknown step %q", s.Op)
}

// once streams one send into one receive through a pipe. Memory is bounded by
// the copy buffer; the stream is never held whole.
func (j Job) once(ctx context.Context, p Plan) (ReceiveResult, error) {
	o := SendOpts{Dataset: j.Source, Snapshot: p.To.Name, Raw: p.Raw}
	if p.From != nil {
		o.From, o.FromBookmark = p.From.Name, p.From.Bookmark
	}
	if p.Resume != nil {
		o.ResumeToken = p.Resume.Token
	}
	s, err := j.Sender.Send(ctx, o)
	if err != nil {
		return ReceiveResult{}, err
	}
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, copyBuf)
		for {
			if err := ctx.Err(); err != nil {
				pw.CloseWithError(err)
				return
			}
			n, rerr := s.Read(buf)
			if n > 0 {
				if _, werr := pw.Write(buf[:n]); werr != nil {
					return
				}
			}
			if rerr != nil {
				if rerr == io.EOF {
					pw.Close()
				} else {
					pw.CloseWithError(rerr)
				}
				return
			}
		}
	}()
	var from uint64
	if p.From != nil {
		from = p.From.GUID
	}
	req := ReceiveRequest{JailUUID: j.JailUUID, Jail: j.Jail, Policy: j.Policy, ChainID: p.ChainID, SourceDataset: j.Source,
		Snapshot: p.To.Name, FromGUID: from, ToGUID: p.To.GUID, Raw: p.Raw, Size: s.Size, Bundle: j.Bundle,
		Encrypted: j.Encrypted, Keys: j.Keys}
	rr, rerr := j.Receiver.Receive(ctx, req, pr)
	pr.CloseWithError(io.ErrClosedPipe)
	<-done
	s.Close()
	return rr, rerr
}
