// Package rollout executes a Rollout: a staged upgrade of the jails selected
// by label, canary first, then fixed-size batches with a pause and a health
// gate between them. A failed upgrade or a failed gate stops the rollout; the
// jails after the failure are never touched.
//
// Progress is persisted after every transition, so a rollout can be resumed
// after a crash or a failure: finished jails are skipped, an upgrade that was
// already started is awaited by its operation id instead of being repeated,
// and a failed jail is retried.
//
// Each step is an SDK operation (Jails().Upgrade and Operations().Wait), so
// the daemon records and audits it like any other. Coordination of the whole
// rollout lives in this process and its state file; there is no hub-side
// rollout resource in the API yet.
package rollout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"jailops.net/jailops/internal/fleet/applylog"
	"jailops.net/jailops/internal/fleet/config"
	"jailops.net/jailops/internal/fleet/plan"
	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

// Mode says how Run treats an existing state.
type Mode int

// Run modes.
const (
	// Start begins a rollout; an unfinished one is an error.
	Start Mode = iota
	// Resume continues an unfinished rollout.
	Resume
	// Restart discards an unfinished rollout and begins again.
	Restart
)

// ErrGate is returned when a health gate does not pass in time.
var ErrGate = errors.New("health gate failed")

// Executor runs rollouts.
type Executor struct {
	Session *plan.Session
	Store   Store
	Log     *applylog.Log
	Out     io.Writer
	// Sleep waits d or until ctx ends; tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error
	Now   func() time.Time
}

func (e *Executor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Executor) sleep(ctx context.Context, d time.Duration) error {
	if e.Sleep != nil {
		return e.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *Executor) printf(format string, args ...any) {
	if e.Out != nil {
		fmt.Fprintf(e.Out, format, args...)
	}
}

func (e *Executor) logf(st *State, event string, extra map[string]any) {
	d := map[string]any{"rollout": st.Name, "event": event, "status": st.Status}
	for k, v := range extra {
		d[k] = v
	}
	e.Log.Write(applylog.KindRollout, d)
}

func (e *Executor) save(st *State) error {
	st.Updated = e.now()
	return e.Store.Save(st)
}

func specHash(r *config.Rollout) string {
	b, _ := json.Marshal(r.Spec)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

// Run starts or resumes a rollout and returns its final state.
func (e *Executor) Run(ctx context.Context, name string, mode Mode) (*State, error) {
	r, ok := e.Session.Fleet.Rollouts[name]
	if !ok {
		return nil, fmt.Errorf("unknown rollout %q", name)
	}
	hash := specHash(r)
	st, err := e.Store.Load(name)
	if err != nil && !errors.Is(err, ErrNoState) {
		return nil, err
	}
	unfinished := st != nil && st.Status != StatusDone
	switch {
	case mode == Resume:
		if st == nil {
			return nil, fmt.Errorf("rollout %s: nothing to resume", name)
		}
		if st.Status == StatusDone {
			return st, nil
		}
		if st.SpecHash != hash {
			return st, fmt.Errorf("rollout %s: its spec changed since it started; finish or `run --restart` it", name)
		}
		st.Status, st.Error = StatusRunning, ""
	case unfinished && mode == Start:
		return st, fmt.Errorf("rollout %s is %s with %d of %d jails done: use `rollout resume %s` or `rollout run --restart %s`",
			name, st.Status, st.Counts()[ItemDone], len(st.Items), name, name)
	default:
		st, err = e.begin(ctx, r, hash)
		if err != nil {
			return nil, err
		}
	}
	if err := e.save(st); err != nil {
		return st, err
	}
	e.logf(st, "start", map[string]any{"jails": len(st.Items), "batches": len(st.Batches), "resume": mode == Resume})
	runErr := e.execute(ctx, r, st)
	switch {
	case runErr == nil:
		st.Status, st.Error = StatusDone, ""
	case errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded):
		st.Status, st.Error = StatusAborted, runErr.Error()
	default:
		st.Status, st.Error = StatusFailed, runErr.Error()
	}
	e.logf(st, "end", map[string]any{"error": st.Error})
	if err := e.save(st); err != nil {
		return st, errors.Join(runErr, err)
	}
	return st, runErr
}

// begin selects the jails and builds batches.
func (e *Executor) begin(ctx context.Context, r *config.Rollout, hash string) (*State, error) {
	f := e.Session.Fleet
	hosts := f.HostNames()
	if len(r.Spec.Hosts) > 0 {
		var err error
		if hosts, err = f.ExpandHosts(r.Spec.Hosts); err != nil {
			return nil, err
		}
	}
	var opts []sdk.ListOption
	keys := make([]string, 0, len(r.Spec.Selector))
	for k := range r.Spec.Selector {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		opts = append(opts, sdk.Label(k, r.Spec.Selector[k]))
	}
	st := &State{Name: r.Name, Fleet: f.Meta.ID, SpecHash: hash, Status: StatusRunning, Started: e.now()}
	me := f.ManagedBy()
	project := r.Spec.Project
	if project == "" {
		return nil, fmt.Errorf("rollout %s names no project (spec.project)", r.Name)
	}
	st.Project = project
	for _, h := range hosts {
		c, err := e.Session.Client(ctx, h)
		if err != nil {
			return nil, fmt.Errorf("host %s: %w", h, err)
		}
		c = c.Project(project)
		var jails []api.Jail
		for j, err := range c.Jails().List(ctx, opts...) {
			if err != nil {
				return nil, fmt.Errorf("host %s: %w", h, err)
			}
			jails = append(jails, j)
		}
		sort.Slice(jails, func(i, j int) bool { return jails[i].Metadata.Name < jails[j].Metadata.Name })
		for _, j := range jails {
			if j.Metadata.ManagedBy != me {
				e.printf("skip %s/%s: not owned by this fleet\n", h, j.Metadata.Name)
				continue
			}
			to := r.Spec.Generation
			if to == "" {
				if to, err = newest(ctx, c, config.ImageName(j.Spec.Image)); err != nil {
					return nil, fmt.Errorf("host %s jail %s: %w", h, j.Metadata.Name, err)
				}
			}
			st.Items = append(st.Items, &Item{Host: h, Jail: j.Metadata.Name, From: j.Status.Generation, To: to, State: ItemPending})
		}
	}
	if len(st.Items) == 0 {
		return nil, fmt.Errorf("rollout %s: the selector matches no jail owned by this fleet", r.Name)
	}
	canary, batch := max(r.Spec.Canary, 1), max(r.Spec.BatchSize, 1)
	for i := 0; i < len(st.Items); {
		n := batch
		if i == 0 {
			n = canary
		}
		end := min(i+n, len(st.Items))
		st.Batches = append(st.Batches, [2]int{i, end})
		i = end
	}
	return st, nil
}

func newest(ctx context.Context, c *sdk.Client, image string) (string, error) {
	best := 0
	for g, err := range c.Images().Generations(ctx, image) {
		if err != nil {
			return "", err
		}
		best = max(best, g.Number)
	}
	if best == 0 {
		return "", fmt.Errorf("image %q has no generation", image)
	}
	return "g" + strconv.Itoa(best), nil
}

func (e *Executor) execute(ctx context.Context, r *config.Rollout, st *State) error {
	pause, timeout, interval, err := r.Spec.Durations()
	if err != nil {
		return err
	}
	for bi, b := range st.Batches {
		items := st.Items[b[0]:b[1]]
		if allDone(items) {
			continue
		}
		e.printf("batch %d/%d: %s\n", bi+1, len(st.Batches), names(items))
		// Phase 1: start (or re-attach to) every upgrade of the batch.
		for _, it := range items {
			if it.State == ItemDone {
				continue
			}
			if err := e.upgrade(ctx, st, it); err != nil {
				return fmt.Errorf("batch %d: %w", bi+1, err)
			}
		}
		// Phase 2: wait for them.
		for _, it := range items {
			if it.State == ItemUpgrading {
				if err := e.await(ctx, st, it); err != nil {
					return fmt.Errorf("batch %d: %w", bi+1, err)
				}
			}
		}
		// Phase 3: the health gate.
		if err := e.gate(ctx, st, items, timeout, interval); err != nil {
			return fmt.Errorf("batch %d: %w", bi+1, err)
		}
		if bi < len(st.Batches)-1 && pause > 0 {
			e.printf("pausing %s\n", pause)
			if err := e.sleep(ctx, pause); err != nil {
				return err
			}
		}
	}
	return nil
}

func allDone(items []*Item) bool {
	for _, it := range items {
		if it.State != ItemDone {
			return false
		}
	}
	return true
}

func names(items []*Item) string {
	var n []string
	for _, it := range items {
		n = append(n, it.Host+"/"+it.Jail)
	}
	return strings.Join(n, ", ")
}

func (e *Executor) fail(st *State, it *Item, err error) error {
	it.State, it.Error = ItemFailed, err.Error()
	e.logf(st, "item_failed", map[string]any{"host": it.Host, "jail": it.Jail, "error": err.Error()})
	if serr := e.save(st); serr != nil {
		return errors.Join(err, serr)
	}
	return fmt.Errorf("%s/%s: %w", it.Host, it.Jail, err)
}

// upgrade issues the upgrade for one item unless it is already done or in flight.
func (e *Executor) upgrade(ctx context.Context, st *State, it *Item) error {
	c, err := e.client(ctx, st, it.Host)
	if err != nil {
		return e.fail(st, it, err)
	}
	if it.State == ItemUpgrading && it.Op != "" {
		return nil // await will wait for the recorded operation
	}
	if it.State == ItemVerifying {
		return nil
	}
	it.Error = ""
	status, err := c.Jails().State(ctx, it.Jail)
	if err != nil {
		return e.fail(st, it, err)
	}
	if status.Generation == it.To {
		it.State, it.Note = ItemVerifying, "already on "+it.To
		return e.save(st)
	}
	op, err := c.Jails().Upgrade(ctx, it.Jail, api.JailUpgrade{Generation: it.To})
	if err != nil {
		return e.fail(st, it, err)
	}
	it.State, it.Op, it.Note = ItemUpgrading, op.ID(), ""
	e.printf("  upgrade %s/%s %s -> %s (operation %s)\n", it.Host, it.Jail, it.From, it.To, it.Op)
	e.logf(st, "upgrade_started", map[string]any{"host": it.Host, "jail": it.Jail, "op": it.Op, "to": it.To})
	return e.save(st)
}

func (e *Executor) await(ctx context.Context, st *State, it *Item) error {
	c, err := e.client(ctx, st, it.Host)
	if err != nil {
		return e.fail(st, it, err)
	}
	if it.Op != "" {
		if _, err := c.Operations().Wait(ctx, it.Op); err != nil {
			if api.IsCode(err, api.CodeNotFound) {
				// The daemon no longer knows the operation: judge by the jail.
				it.Op = ""
				status, serr := c.Jails().State(ctx, it.Jail)
				if serr == nil && status.Generation == it.To {
					it.State = ItemVerifying
					return e.save(st)
				}
				it.State = ItemPending
				return e.fail(st, it, fmt.Errorf("lost the upgrade operation and the jail is not on %s", it.To))
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			it.Op = ""
			return e.fail(st, it, err)
		}
	}
	it.Op, it.State = "", ItemVerifying
	return e.save(st)
}

// gate waits until every verifying item is healthy or the timeout passes.
func (e *Executor) gate(ctx context.Context, st *State, items []*Item, timeout, interval time.Duration) error {
	var pending []*Item
	for _, it := range items {
		if it.State == ItemVerifying {
			pending = append(pending, it)
		}
	}
	deadline := e.now().Add(timeout)
	reasons := map[*Item]string{}
	for {
		var still []*Item
		for _, it := range pending {
			c, err := e.client(ctx, st, it.Host)
			if err != nil {
				return e.fail(st, it, err)
			}
			status, err := c.Jails().State(ctx, it.Jail)
			if err != nil {
				return e.fail(st, it, err)
			}
			if ok, why := healthy(status, it.To); ok {
				it.State, it.Error = ItemDone, ""
				e.printf("  healthy %s/%s\n", it.Host, it.Jail)
				e.logf(st, "item_done", map[string]any{"host": it.Host, "jail": it.Jail})
			} else {
				reasons[it] = why
				still = append(still, it)
			}
		}
		if err := e.save(st); err != nil {
			return err
		}
		pending = still
		if len(pending) == 0 {
			return nil
		}
		if !e.now().Before(deadline) {
			var msgs []string
			for _, it := range pending {
				it.State, it.Error = ItemFailed, "unhealthy: "+reasons[it]
				msgs = append(msgs, fmt.Sprintf("%s/%s (%s)", it.Host, it.Jail, reasons[it]))
				e.logf(st, "item_failed", map[string]any{"host": it.Host, "jail": it.Jail, "error": it.Error})
			}
			if err := e.save(st); err != nil {
				return err
			}
			return fmt.Errorf("%w after %s: %s", ErrGate, timeout, strings.Join(msgs, ", "))
		}
		if err := e.sleep(ctx, interval); err != nil {
			return err
		}
	}
}

// healthy is the health gate: the jail runs on the target generation and no
// Ready or Healthy condition is False.
func healthy(s api.JailStatus, to string) (bool, string) {
	if s.Power != api.PowerRunning {
		return false, "power is " + string(s.Power)
	}
	if to != "" && s.Generation != to {
		return false, fmt.Sprintf("generation is %s, want %s", s.Generation, to)
	}
	for _, c := range s.Conditions {
		if (c.Type == "Ready" || c.Type == "Healthy") && c.Status != api.ConditionTrue {
			return false, fmt.Sprintf("condition %s is %s", c.Type, c.Status)
		}
	}
	return true, ""
}

// Abort cancels in-flight upgrades and marks an unfinished rollout aborted.
func (e *Executor) Abort(ctx context.Context, name string) (*State, error) {
	st, err := e.Store.Load(name)
	if err != nil {
		return nil, err
	}
	if st.Status == StatusDone {
		return st, fmt.Errorf("rollout %s is already done", name)
	}
	for _, it := range st.Items {
		if it.State != ItemUpgrading || it.Op == "" {
			continue
		}
		if c, err := e.client(ctx, st, it.Host); err == nil {
			if err := c.Operations().Cancel(ctx, it.Op); err != nil {
				e.printf("could not cancel %s on %s: %v\n", it.Op, it.Host, err)
			}
		}
		it.State, it.Op = ItemPending, ""
	}
	st.Status, st.Error = StatusAborted, "aborted by the operator"
	e.logf(st, "aborted", nil)
	return st, e.save(st)
}

// client returns the host's client scoped to the rollout's project.
func (e *Executor) client(ctx context.Context, st *State, host string) (*sdk.Client, error) {
	c, err := e.Session.Client(ctx, host)
	if err != nil {
		return nil, err
	}
	if st.Project == "" {
		return nil, fmt.Errorf("rollout %s names no project", st.Name)
	}
	return c.Project(st.Project), nil
}
