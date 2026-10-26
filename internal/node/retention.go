package node

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"jailops.net/jailops/internal/node/ctl"
	"jailops.net/jailops/internal/retention"
	"jailops.net/jailops/sdk/api"
)

// RetentionOptions tune a retention run.
type RetentionOptions struct {
	// DryRun returns the exact plan and removes nothing.
	DryRun bool `json:"dry_run,omitempty"`
	// Group limits the run to one group.
	Group string `json:"group,omitempty"`
}

// RetentionDecision is the verdict on one snapshot.
type RetentionDecision struct {
	Snapshot string    `json:"snapshot"`
	Time     time.Time `json:"time"`
	Reasons  []string  `json:"reasons"`
}

// RetentionGroup is the plan for one group.
type RetentionGroup struct {
	Group   string              `json:"group"`
	Policy  api.RetentionPolicy `json:"policy"`
	Source  string              `json:"policy_source"` // group, default, config or none
	Keep    []RetentionDecision `json:"keep"`
	Delete  []RetentionDecision `json:"delete"`
	Deleted []string            `json:"deleted,omitempty"`
	Skipped []string            `json:"skipped,omitempty"`
	Error   string              `json:"error,omitempty"`
}

// RetentionResult is the outcome of a retention run.
type RetentionResult struct {
	DryRun  bool             `json:"dry_run"`
	Groups  []RetentionGroup `json:"groups"`
	Deleted int              `json:"deleted"`
}

// policyFor resolves the policy of a group: its own row, the default row, the
// configured default. Anything unknown or unparseable keeps everything.
func (n *Node) policyFor(rows map[string]ctl.RetentionPolicyRow, group string) (api.RetentionPolicy, *time.Location, string, error) {
	row, ok := rows[group]
	src := "group"
	if !ok {
		row, ok = rows["default"]
		src = "default"
	}
	if !ok {
		loc := time.UTC
		if n.cfg.RetentionTimezone != "" {
			if l, err := time.LoadLocation(n.cfg.RetentionTimezone); err == nil {
				loc = l
			}
		}
		return n.cfg.RetentionDefault, loc, "config", nil
	}
	var p api.RetentionPolicy
	if err := json.Unmarshal(row.Policy, &p); err != nil {
		return api.RetentionPolicy{}, time.UTC, src, errors.New("the stored policy is unreadable; keeping everything")
	}
	loc, err := time.LoadLocation(row.Timezone)
	if err != nil {
		return api.RetentionPolicy{}, time.UTC, src, errors.New("unknown time zone " + row.Timezone + "; keeping everything")
	}
	return p, loc, src, nil
}

// Retention evaluates the policies and, unless DryRun, deletes what they expire.
// Retention is a pure function over snapshot times (the node's own commit
// time, which a host cannot forge) and the policy; the newest snapshot is never
// removed, protected snapshots are kept, and a snapshot an open session uses as
// its previous snapshot is kept. Chunks are left for GC.
func (n *Node) Retention(ctx context.Context, opts RetentionOptions, actor string) (*RetentionResult, ctl.Job, error) {
	if !opts.DryRun {
		if err := n.requireOnline(); err != nil {
			return nil, ctl.Job{}, err
		}
	}
	var res *RetentionResult
	job, err := n.runJob(ctx, ctl.JobRetention, actor, opts, func(ctx context.Context) (any, error) {
		var e error
		res, e = n.retention(ctx, opts, actor)
		return res, e
	})
	return res, job, err
}

// StartRetention runs retention in the background.
func (n *Node) StartRetention(opts RetentionOptions, actor string) (ctl.Job, error) {
	if !opts.DryRun {
		if err := n.requireOnline(); err != nil {
			return ctl.Job{}, err
		}
	}
	return n.startAsync(ctl.JobRetention, actor, opts, func(ctx context.Context) (any, error) { return n.retention(ctx, opts, actor) })
}

func (n *Node) retention(ctx context.Context, opts RetentionOptions, actor string) (*RetentionResult, error) {
	rows, err := n.db.RetentionPolicies(ctx)
	if err != nil {
		return nil, err
	}
	byScope := map[string]ctl.RetentionPolicyRow{}
	for _, r := range rows {
		byScope[r.Scope] = r
	}
	pinned, err := n.db.PinnedSnapshots(ctx)
	if err != nil {
		return nil, err
	}
	snaps, err := n.db.Snapshots(ctx, opts.Group)
	if err != nil {
		return nil, err
	}
	groups := map[string][]ctl.Snapshot{}
	var order []string
	for _, s := range snaps {
		if _, ok := groups[s.GroupID]; !ok {
			order = append(order, s.GroupID)
		}
		groups[s.GroupID] = append(groups[s.GroupID], s)
	}
	out := &RetentionResult{DryRun: opts.DryRun}
	now := n.now()
	for _, g := range order {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		pol, loc, src, perr := n.policyFor(byScope, g)
		rg := RetentionGroup{Group: g, Policy: pol, Source: src}
		if perr != nil {
			rg.Error = perr.Error()
			out.Groups = append(out.Groups, rg)
			continue
		}
		var in []retention.Snapshot
		for _, s := range groups[g] {
			in = append(in, retention.Snapshot{Name: s.SnapshotID, Time: s.CommittedAt, Held: s.Protected || pinned[[2]string{g, s.SnapshotID}], Full: false})
		}
		plan := retention.Evaluate(in, pol, now, n.retentionOptions(loc))
		conv := func(ds []retention.Decision) []RetentionDecision {
			var o []RetentionDecision
			for _, d := range ds {
				o = append(o, RetentionDecision{Snapshot: d.Snapshot.Name, Time: d.Snapshot.Time, Reasons: d.Reasons})
			}
			return o
		}
		rg.Keep, rg.Delete = conv(plan.Keep), conv(plan.Delete)
		if !opts.DryRun {
			for _, d := range rg.Delete {
				err := n.deleteNow(ctx, g, d.Snapshot, actor)
				switch {
				case err == nil:
					rg.Deleted = append(rg.Deleted, d.Snapshot)
					out.Deleted++
				case errors.Is(err, ErrProtected) || api.IsCode(err, api.CodeNotFound):
					rg.Skipped = append(rg.Skipped, d.Snapshot)
				default:
					rg.Error = err.Error()
					out.Groups = append(out.Groups, rg)
					return out, err
				}
			}
		}
		out.Groups = append(out.Groups, rg)
	}
	return out, nil
}
