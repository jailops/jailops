package plan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"jailops.net/jailops/internal/fleet/config"
	"jailops.net/jailops/internal/fleet/secrets"
	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

// The recipes of a jail (docs/fleet-sync.md).
//
// A recipe is applied when its hash (the rendered recipe, the files beside it and a fingerprint of each secret) is not
// the one recorded on the jail by the last successful apply, or when the record is missing, or when a dry run, which reads
// the jail and runs nothing, finds a step that would change something. Otherwise nothing is done. The dry run judges only
// what it can read: a run without guard and a run with unless are actions, not state, and do not count.

// RecipeAction is what the plan does with one recipe of one jail.
type RecipeAction string

// Recipe actions.
const (
	RecipeApply RecipeAction = "apply"
	RecipeMatch RecipeAction = "match"
	// RecipeWait: nothing can be applied now (the jail does not run, or a jail it needs is not settled). Not an error.
	RecipeWait RecipeAction = "wait"
	// RecipeForget removes the record of a recipe the repository no longer lists. Nothing is undone in the jail.
	RecipeForget RecipeAction = "forget"
	RecipeError  RecipeAction = "error"
)

// RecipeStatus is what the last look inside the jail found.
type RecipeStatus string

// Recipe statuses. Unknown is never read as converged.
const (
	StatusConverged RecipeStatus = "converged"
	StatusDrift     RecipeStatus = "drift"
	StatusUnknown   RecipeStatus = "unknown"
)

// RecipeItem is one decision about one recipe of one jail.
type RecipeItem struct {
	Host, Project, Jail string
	Needs               []string
	// Name is the recipe's name in the jail (the end of its path).
	Name   string
	Source config.RecipeSource
	Action RecipeAction
	Status RecipeStatus
	// Why says what makes it apply, wait or stay unknown.
	Why string
	// Hash is the current hash, Recorded the one on the jail ("" when none).
	Hash, Recorded string
	// Steps are the result of the dry run made now (nil when none was made).
	Steps []api.ApplyStep
	// Looked says a dry run was made now.
	Looked bool
	// Settled says a jail that needs this one may go on when nothing more happens to it: it ran, its hash is the
	// recorded one, and it was found converged or is not due for another look.
	Settled bool
}

// Ref is "Jail/project/name recipe".
func (r RecipeItem) Ref() string {
	return fmt.Sprintf("Jail/%s/%s %s", r.Project, r.Jail, r.Name)
}

// JailKey names the jail of the item on its host.
func (r RecipeItem) JailKey() string { return r.Host + "/" + r.Project + "/" + r.Jail }

// CheckKey names the item in the memory of checks.
func (r RecipeItem) CheckKey() string { return r.JailKey() + "/" + r.Name }

// Pending says whether the recipe will be applied.
func (r RecipeItem) Pending() bool { return r.Action == RecipeApply }

// StoreResolver gives the secrets of the store to a render: the value is decrypted in memory only, the stamp is a digest
// of the stored ciphertext and needs no key.
type StoreResolver struct{ Store *secrets.Store }

// Value decrypts a secret (one trailing line break of a file is not part of it).
func (r StoreResolver) Value(ref string) (string, error) {
	if config.IsSopsRef(ref) {
		return r.Store.SopsValue(ref)
	}
	b, err := r.Store.Decrypt(ref)
	if err != nil {
		return "", err
	}
	v := strings.TrimSuffix(string(b), "\n")
	r.Store.Redactor().Add([]byte(v))
	return v, nil
}

// Stamp is a fingerprint of the stored secret.
func (r StoreResolver) Stamp(ref string) (string, error) {
	if config.IsSopsRef(ref) {
		return r.Store.SopsStable(ref)
	}
	sealed, err := r.Store.Sealed(ref)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(sealed))
	return hex.EncodeToString(sum[:8]), nil
}

// planRecipes decides the recipes of the jails of one host, jails after the jails they need, recipes in list order.
func planRecipes(ctx context.Context, c *sdk.Client, s *Session, opt Options, host string, items []Item, live map[config.Key]Live) ([]RecipeItem, error) {
	var all, jails []*config.Object
	for _, o := range s.Fleet.ObjectsFor(host) {
		if o.Kind != api.KindJail {
			continue
		}
		// recipes are for FreeBSD jails: a Linux jail has no recipe items, so it is never drift or unknown
		if isLinuxJail(o, live[o.Key()]) {
			continue
		}
		all = append(all, o)
		if len(o.Recipes) > 0 {
			jails = append(jails, o)
		}
	}
	ordered, err := config.NeedsOrder(jails)
	if err != nil {
		return nil, err
	}
	interval, err := s.Fleet.DriftInterval()
	if err != nil {
		return nil, err
	}
	itemOf := func(o *config.Object) *Item {
		for i := range items {
			if items[i].Kind == api.KindJail && items[i].Project == o.Project && items[i].Name == o.Name {
				return &items[i]
			}
		}
		return nil
	}
	var out []RecipeItem
	// unsettled holds, per jail whose recipes are all decided, why a jail that needs it must wait ("" when it may go on).
	unsettled := map[string]string{}
	pending := map[string]bool{}
	for _, o := range ordered {
		jitem := itemOf(o)
		l, exists := live[o.Key()]
		power := api.PowerState("")
		if exists && jitem != nil && jitem.Action != ActionRefuse && jitem.Action != ActionError {
			if j, err := c.Project(o.Project).Jails().Get(ctx, o.Name); err == nil {
				power = j.Status.Power
			}
		}
		jkey := host + "/" + o.Project + "/" + o.Name
		var jobs []RecipeItem
		for _, src := range o.Recipes {
			it := RecipeItem{Host: host, Project: o.Project, Jail: o.Name, Needs: o.Needs, Name: src.Name, Source: src}
			it.Recorded = l.Meta.Labels[config.RecipeLabel(src.Name)]
			res, rerr := src.Resolve(StoreResolver{s.Store})
			it.Hash = res.Hash
			switch {
			case rerr != nil:
				it.Action, it.Why = RecipeError, rerr.Error()
			case jitem == nil || jitem.Action == ActionRefuse || jitem.Action == ActionError:
				it.Action, it.Status, it.Why = RecipeWait, StatusUnknown, "the jail is not managed by this fleet"
			case !exists && o.Start:
				it.Action, it.Why = RecipeApply, "new jail"
			case !exists:
				it.Action, it.Status, it.Why = RecipeWait, StatusUnknown, "not running: the new jail is not started (start: true starts it once, when it is created)"
			case power != api.PowerRunning:
				it.Action, it.Status, it.Why = RecipeWait, StatusUnknown, "not running"
			default:
				lookInside(ctx, c, s, opt, interval, &it, res)
			}
			jobs = append(jobs, it)
			if it.Action == RecipeApply {
				pending[jkey] = true
			}
			if !it.Settled && it.Action != RecipeApply {
				if unsettled[jkey] == "" {
					unsettled[jkey] = fmt.Sprintf("%s: %s", it.Name, it.Why)
				}
			}
		}
		// A recipe that would apply waits when a jail it needs cannot be settled: it was not running, or its last look failed.
		for i := range jobs {
			if jobs[i].Action != RecipeApply {
				continue
			}
			for _, n := range o.Needs {
				nk := host + "/" + o.Project + "/" + n
				if why := unsettled[nk]; why != "" && !pending[nk] {
					jobs[i].Action, jobs[i].Why = RecipeWait, fmt.Sprintf("needs %s (%s)", n, why)
					break
				}
			}
		}
		out = append(out, jobs...)
	}
	// Records of recipes the repository no longer lists.
	for _, o := range all {
		l, exists := live[o.Key()]
		if !exists || l.Meta.ManagedBy != s.Fleet.ManagedBy() {
			continue
		}
		listed := map[string]bool{}
		for _, r := range o.Recipes {
			listed[r.Name] = true
		}
		var gone []string
		for k := range l.Meta.Labels {
			if n, ok := strings.CutPrefix(k, config.RecipeLabelPrefix); ok && !listed[n] {
				gone = append(gone, n)
			}
		}
		sort.Strings(gone)
		for _, n := range gone {
			out = append(out, RecipeItem{Host: host, Project: o.Project, Jail: o.Name, Name: n, Action: RecipeForget, Recorded: l.Meta.Labels[config.RecipeLabel(n)],
				Why: "no longer listed in the repository; nothing is undone in the jail"})
		}
	}
	return out, nil
}

// isLinuxJail says whether the jail is a Linux jail: the one on the host when there is one, else the one the file declares.
func isLinuxJail(o *config.Object, l Live) bool {
	spec := l.Spec
	if spec == nil {
		spec = o.Spec
	}
	js, ok := spec.(api.JailSpec)
	return ok && js.OS == api.OSLinux
}

// lookInside decides a recipe of a running jail: it compares the hash, and looks inside with a dry run when the hash
// changed, when the run is manual, or when drift_interval passed since the last look that found it converged.
func lookInside(ctx context.Context, c *sdk.Client, s *Session, opt Options, interval time.Duration, it *RecipeItem, res config.Resolved) {
	changed := it.Recorded != it.Hash
	last, trusted := s.Checks.Converged(it.CheckKey())
	trusted = trusted && last.Hash == it.Hash && time.Since(last.At) < interval
	if !changed && !opt.Manual && trusted {
		it.Action, it.Status, it.Settled = RecipeMatch, StatusConverged, true
		it.Why = fmt.Sprintf("not looked at again until %s", last.At.Add(interval).Format(time.DateTime))
		return
	}
	status, steps, err := dryRun(ctx, c, it, res)
	it.Looked, it.Steps, it.Status = true, steps, status
	if status != StatusConverged {
		s.Checks.Forget(it.CheckKey())
	}
	drift := 0
	for _, st := range steps {
		if st.Status == api.StepWouldChange {
			drift++
		}
	}
	switch {
	case it.Recorded == "":
		it.Action, it.Why = RecipeApply, "no record on the jail: never applied, or the last apply did not finish"
	case changed:
		it.Action, it.Why = RecipeApply, "the recipe changed"
	case status == StatusDrift:
		it.Action, it.Why = RecipeApply, fmt.Sprintf("%d step(s) differ in the jail", drift)
	case status == StatusConverged:
		it.Action, it.Settled = RecipeMatch, true
		s.Checks.Set(it.CheckKey(), it.Hash)
	default:
		it.Action, it.Why = RecipeMatch, "unknown: not looked at inside the jail: "+err.Error()
	}
}

// dryRun asks the jail what the recipe would change. It judges the steps it can read; a step that would execute or
// check is an action, not a state.
func dryRun(ctx context.Context, c *sdk.Client, it *RecipeItem, res config.Resolved) (RecipeStatus, []api.ApplyStep, error) {
	if res.ValueErr != nil {
		return StatusUnknown, nil, fmt.Errorf("a secret cannot be read here: %w", res.ValueErr)
	}
	op, err := c.Project(it.Project).Jails().Apply(ctx, it.Jail, res.Request(true))
	var r api.ApplyResult
	if err == nil {
		r, err = op.Wait(ctx)
	}
	if err != nil {
		return StatusUnknown, nil, err
	}
	return Classify(r.Steps), r.Steps, nil
}

// Classify judges the steps of a dry run. Only a step that would change something is drift: package, file, dir, sysrc,
// service and a run with creates say so when the jail differs. A run without guard (would execute) and a run with unless
// (would check) are actions, not state; a step that was skipped (its guard held) is in order.
func Classify(steps []api.ApplyStep) RecipeStatus {
	for _, st := range steps {
		if st.Status == api.StepWouldChange {
			return StatusDrift
		}
	}
	return StatusConverged
}

// RecipeSummary is a count for the summary line.
func (p *Plan) RecipeSummary() string {
	var apply, wait, unknown int
	for _, r := range p.Recipes {
		switch r.Action {
		case RecipeApply:
			apply++
		case RecipeWait:
			wait++
		}
		if r.Status == StatusUnknown && r.Action == RecipeMatch {
			unknown++
		}
	}
	var parts []string
	if apply > 0 {
		parts = append(parts, fmt.Sprintf("%d recipe(s) to apply", apply))
	}
	if wait > 0 {
		parts = append(parts, fmt.Sprintf("%d recipe(s) waiting", wait))
	}
	if unknown > 0 {
		parts = append(parts, fmt.Sprintf("%d recipe(s) unknown", unknown))
	}
	return strings.Join(parts, ", ")
}
