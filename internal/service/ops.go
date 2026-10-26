package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"jailops.net/jailops/internal/plan"
	"jailops.net/jailops/internal/store"
	"jailops.net/jailops/sdk/api"
)

// opManager runs mutating requests as persisted operations.
type opManager struct {
	s *Service

	mu    sync.Mutex
	live  map[string]*liveOp
	byRun map[string]*liveOp
}

type liveOp struct {
	mu        sync.Mutex
	op        api.Operation
	done      chan struct{}
	cancel    context.CancelFunc
	cancelled bool
	runs      []string
	actor     Actor
}

func newOpManager(s *Service) *opManager {
	return &opManager{s: s, live: map[string]*liveOp{}, byRun: map[string]*liveOp{}}
}

// OpSpec describes one operation.
type OpSpec struct {
	Description string
	// Project is the project the operation works in; empty for host-level
	// operations (networks, images, storage), which only global identities see.
	Project string
	// Resources lists the affected objects by kind, e.g. {"jails": {"/1.0/jails/web"}}.
	Resources map[string][]string
	// Locks are taken all at once at submit time; busy resources fail the
	// submit with api.CodeBusy.
	Locks []string
	// Run does the work. Its result becomes operation.metadata.
	Run func(ctx context.Context, h *OpHandle) (any, error)
}

// OpHandle lets a running operation report progress and bind plan runs.
type OpHandle struct {
	m  *opManager
	lo *liveOp
}

// ID is the operation id.
func (h *OpHandle) ID() string { return h.lo.op.ID }

// Bind associates a journal run id with the operation: plan events of that
// run update the operation's progress, and req is stored so the plan can be
// rebuilt after a crash.
func (h *OpHandle) Bind(runID string, req *opRequest) error {
	h.m.mu.Lock()
	h.m.byRun[runID] = h.lo
	h.m.mu.Unlock()
	h.lo.mu.Lock()
	h.lo.runs = append(h.lo.runs, runID)
	h.lo.mu.Unlock()
	if req != nil {
		req.OpID = h.lo.op.ID
		return h.m.s.journal.putRequest(runID, *req)
	}
	return nil
}

// Progress sets the progress estimate (0..99).
func (h *OpHandle) Progress(p int) { h.m.setProgress(h.lo, p) }

// Submit runs spec as an operation and returns its initial record.
func (s *Service) Submit(ctx context.Context, spec OpSpec) (api.Operation, error) {
	return s.ops.submit(ctx, spec)
}

func (m *opManager) submit(ctx context.Context, spec OpSpec) (api.Operation, error) {
	s := m.s
	actor := ActorFrom(ctx)
	release, err := s.locks.lockPatient(ctx, spec.Description, spec.Locks...)
	if err != nil {
		return api.Operation{}, err
	}
	now := s.now().UTC()
	lo := &liveOp{
		op: api.Operation{ID: newOpID(), Description: spec.Description, Status: api.OpPending, Resources: spec.Resources,
			MayCancel: true, CreatedAt: now, UpdatedAt: now, Project: spec.Project},
		done: make(chan struct{}), actor: actor,
	}
	opctx, cancel := context.WithCancel(WithActor(s.baseCtx, actor))
	lo.cancel = cancel
	m.mu.Lock()
	m.live[lo.op.ID] = lo
	m.mu.Unlock()
	if err := s.d.DB.Operations.Put(ctx, lo.op); err != nil {
		cancel()
		release()
		m.mu.Lock()
		delete(m.live, lo.op.ID)
		m.mu.Unlock()
		return api.Operation{}, internal(err)
	}
	s.emitOperation(lo.op)
	snap := lo.op
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer release()
		defer cancel()
		m.run(opctx, lo, spec, release)
	}()
	return snap, nil
}

func (m *opManager) run(ctx context.Context, lo *liveOp, spec OpSpec, release func()) {
	s := m.s
	m.update(lo, func(o *api.Operation) { o.Status = api.OpRunning })
	var (
		res any
		err error
	)
	func() {
		defer func() {
			if r := recover(); r != nil {
				s.log.Error("operation panicked", "op", lo.op.ID, "panic", r)
				err = api.Errorf(api.CodeInternal, "operation panicked: %v", r)
			}
		}()
		res, err = spec.Run(ctx, &OpHandle{m: m, lo: lo})
	}()
	lo.mu.Lock()
	cancelled := lo.cancelled
	runs := append([]string(nil), lo.runs...)
	lo.mu.Unlock()
	status, aerr := api.OpSuccess, (*api.Error)(nil)
	var meta json.RawMessage
	switch {
	case err != nil && cancelled:
		status = api.OpCancelled
	case err != nil:
		status, aerr = api.OpFailure, toAPIError(err)
	case res != nil:
		meta, _ = json.Marshal(res)
	}
	// Locks go before the final status is visible, so a client that sees the
	// operation finish can start the next one at once.
	release()
	m.update(lo, func(o *api.Operation) {
		o.Status, o.Error, o.MayCancel = status, aerr, false
		if status == api.OpSuccess {
			o.Progress = 100
			o.Metadata = meta
		}
	})
	for _, id := range runs {
		if _, ok, _ := s.journal.Get(context.Background(), id); !ok {
			s.journal.deleteRequest(id)
		}
		m.mu.Lock()
		delete(m.byRun, id)
		m.mu.Unlock()
	}
	m.audit(lo, status, aerr)
	close(lo.done)
	m.mu.Lock()
	delete(m.live, lo.op.ID)
	m.mu.Unlock()
}

func (m *opManager) audit(lo *liveOp, status api.OperationStatus, aerr *api.Error) {
	var objs []string
	for _, v := range lo.op.Resources {
		objs = append(objs, v...)
	}
	result := "ok"
	detail := lo.op.Description
	switch status {
	case api.OpFailure:
		result = "error"
		if aerr != nil {
			detail += ": " + string(aerr.Code)
		}
	case api.OpCancelled:
		result = "error"
		detail += ": cancelled"
	}
	_, _ = m.s.d.DB.Audit.Append(context.Background(), store.AuditEntry{
		Time: m.s.now().UTC(), Actor: lo.actor.Name, ActorKind: lo.actor.Kind, Method: "OPERATION",
		Route: lo.op.ID, Object: strings.Join(objs, ","), Project: lo.op.Project, Result: result, Detail: detail, Remote: lo.actor.Remote,
	})
}

func (m *opManager) update(lo *liveOp, fn func(*api.Operation)) {
	lo.mu.Lock()
	fn(&lo.op)
	lo.op.UpdatedAt = m.s.now().UTC()
	snap := lo.op
	lo.mu.Unlock()
	if err := m.s.d.DB.Operations.Put(context.Background(), snap); err != nil {
		m.s.log.Error("persisting operation failed", "op", snap.ID, "err", err)
	}
	m.s.emitOperation(snap)
}

func (m *opManager) setProgress(lo *liveOp, p int) {
	p = max(0, min(p, 99))
	lo.mu.Lock()
	same := lo.op.Progress == p || lo.op.Status.Final()
	lo.mu.Unlock()
	if same {
		return
	}
	m.update(lo, func(o *api.Operation) { o.Progress = p })
}

// planEvent is the plan.Executor progress callback.
func (m *opManager) planEvent(ev plan.Event) {
	if ev.Phase != plan.PhaseDone || ev.Total == 0 {
		return
	}
	m.mu.Lock()
	lo := m.byRun[ev.ID]
	m.mu.Unlock()
	if lo != nil {
		m.setProgress(lo, (ev.Index+1)*100/ev.Total)
	}
}

// toAPIError converts any error into an *api.Error, keeping codes that a
// plan error wraps.
func toAPIError(err error) *api.Error {
	var ae *api.Error
	if errors.As(err, &ae) {
		out := *ae
		var pe *plan.Error
		if errors.As(err, &pe) && len(pe.UndoErrs) > 0 {
			out.Hint = strings.TrimSpace(out.Hint + " The rollback of this operation was incomplete; run `jailops doctor`.")
		}
		return &out
	}
	if errors.Is(err, context.Canceled) {
		return api.Errorf(api.CodeBusy, "operation was cancelled")
	}
	return api.Errorf(api.CodeInternal, "%v", err)
}

// ---- reads ----

// opVisible says whether the caller may see an operation: an operation of a
// project belongs to the identities that may use the project, an operation
// without a project (networks, images, storage) to global identities.
func opVisible(a Actor, op api.Operation) bool { return canSeeProject(a, op.Project) }

// opFor returns an operation the caller may see, or not_found.
func (s *Service) opFor(ctx context.Context, id string) (api.Operation, error) {
	a := ActorFrom(ctx)
	if !a.HasScope() {
		return api.Operation{}, api.Errorf(api.CodeForbidden, "this identity has no projects and is not global")
	}
	op, err := s.rawOperation(ctx, id)
	if err != nil || !opVisible(a, op) {
		return api.Operation{}, notFound("operation", id)
	}
	return op, nil
}

func (s *Service) rawOperation(ctx context.Context, id string) (api.Operation, error) {
	s.ops.mu.Lock()
	lo := s.ops.live[id]
	s.ops.mu.Unlock()
	if lo != nil {
		lo.mu.Lock()
		defer lo.mu.Unlock()
		return lo.op, nil
	}
	op, err := s.d.DB.Operations.Get(ctx, id)
	if err != nil {
		return api.Operation{}, notFound("operation", id)
	}
	return *op, nil
}

// GetOperation returns one operation the caller may see.
func (s *Service) GetOperation(ctx context.Context, id string) (api.Operation, error) {
	return s.opFor(ctx, id)
}

// ListOperations returns the operations the caller may see, newest first.
func (s *Service) ListOperations(ctx context.Context) ([]api.Operation, error) {
	all, projects, err := s.visibleProjects(ctx)
	if err != nil {
		return nil, err
	}
	var ops []*api.Operation
	if ProjectFrom(ctx) != "" {
		ops, err = s.d.DB.Operations.ListForProjects(ctx, false, projects, 500)
	} else {
		ops, err = s.d.DB.Operations.ListForProjects(ctx, all, projects, 500)
	}
	if err != nil {
		return nil, internal(err)
	}
	out := make([]api.Operation, 0, len(ops))
	for _, o := range ops {
		// A live operation is fresher than its stored row.
		if cur, err := s.rawOperation(ctx, o.ID); err == nil {
			out = append(out, cur)
		} else {
			out = append(out, *o)
		}
	}
	return out, nil
}

// WaitOperation blocks until the operation is final, the timeout passes or ctx ends.
func (s *Service) WaitOperation(ctx context.Context, id string, timeout time.Duration) (api.Operation, error) {
	if _, err := s.opFor(ctx, id); err != nil {
		return api.Operation{}, err
	}
	s.ops.mu.Lock()
	lo := s.ops.live[id]
	s.ops.mu.Unlock()
	if lo != nil {
		t := time.NewTimer(timeout)
		defer t.Stop()
		select {
		case <-lo.done:
		case <-t.C:
		case <-ctx.Done():
		case <-s.baseCtx.Done():
		}
	}
	return s.rawOperation(ctx, id)
}

// CancelOperation asks a running operation to stop.
func (s *Service) CancelOperation(ctx context.Context, id string) error {
	if _, err := s.opFor(ctx, id); err != nil {
		return err
	}
	s.ops.mu.Lock()
	lo := s.ops.live[id]
	s.ops.mu.Unlock()
	if lo == nil {
		if _, err := s.d.DB.Operations.Get(ctx, id); err != nil {
			return notFound("operation", id)
		}
		return api.Errorf(api.CodeInvalid, "operation %q is already finished and cannot be cancelled", id)
	}
	lo.mu.Lock()
	if lo.op.Status.Final() || !lo.op.MayCancel {
		lo.mu.Unlock()
		return api.Errorf(api.CodeInvalid, "operation %q cannot be cancelled", id)
	}
	lo.cancelled = true
	lo.mu.Unlock()
	lo.cancel()
	return nil
}
