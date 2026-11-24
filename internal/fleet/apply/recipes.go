package apply

import (
	"context"
	"fmt"
	"strings"

	"jailops.net/jailops/internal/fleet/applylog"
	"jailops.net/jailops/internal/fleet/config"
	"jailops.net/jailops/internal/fleet/plan"
	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

// Recipe outcome statuses.
const (
	RecipeApplied   = "applied"
	RecipeFailed    = "failed"
	RecipeSkipped   = "skipped" // an earlier failure stopped it
	RecipeWaiting   = "waiting"
	RecipeConverged = "converged"
	RecipeUnknown   = "unknown"
	RecipeForgotten = "forgotten"
)

// RecipeOutcome is the result of one recipe of one jail.
type RecipeOutcome struct {
	Item   plan.RecipeItem
	Status string
	// Reason says why it waits or was skipped.
	Reason string
	Steps  []api.ApplyStep
	Err    error
}

// FailedRecipes returns the recipes that failed.
func (r Result) FailedRecipes() []RecipeOutcome {
	var out []RecipeOutcome
	for _, o := range r.Recipes {
		if o.Status == RecipeFailed {
			out = append(out, o)
		}
	}
	return out
}

// applyRecipes runs the decisions of the plan for the recipes, after every object of every host. A recipe that fails
// stops the later recipes of its jail and the jails that need it; the other jails go on. Nothing is undone.
func applyRecipes(ctx context.Context, s *plan.Session, p *plan.Plan, opt Options, res *Result) {
	failedJail := map[string]string{} // jail -> recipe that failed
	unsettled := map[string]string{}  // jail -> why a jail that needs it cannot go on
	for _, r := range p.Recipes {
		o := RecipeOutcome{Item: r}
		jk := r.JailKey()
		note := func(why string) {
			if unsettled[jk] == "" {
				unsettled[jk] = r.Name + ": " + why
			}
		}
		switch r.Action {
		case plan.RecipeMatch:
			o.Status = RecipeConverged
			if r.Status == plan.StatusUnknown {
				o.Status, o.Reason = RecipeUnknown, r.Why
			}
			if !r.Settled {
				note(r.Why)
			}
		case plan.RecipeWait:
			o.Status, o.Reason = RecipeWaiting, r.Why
			note(r.Why)
		case plan.RecipeForget:
			o.Status = RecipeForgotten
			if err := forgetRecipe(ctx, s, r); err != nil {
				o.Status, o.Err = RecipeFailed, err
			}
		case plan.RecipeApply:
			switch {
			case ctx.Err() != nil:
				o.Status, o.Reason = RecipeSkipped, ctx.Err().Error()
			case failedOn(*res, r.Host):
				o.Status, o.Reason = RecipeSkipped, "an earlier change on this host failed"
			case failedJail[jk] != "":
				o.Status, o.Reason = RecipeSkipped, "recipe "+failedJail[jk]+" of this jail failed"
			default:
				for _, n := range r.Needs {
					nk := r.Host + "/" + r.Project + "/" + n
					if f := failedJail[nk]; f != "" {
						o.Status, o.Reason = RecipeSkipped, fmt.Sprintf("needs %s, whose recipe %s failed", n, f)
						break
					}
					if why := unsettled[nk]; why != "" {
						o.Status, o.Reason = RecipeWaiting, fmt.Sprintf("needs %s (%s)", n, why)
						break
					}
				}
			}
			if o.Status == "" {
				o.Steps, o.Err = applyRecipe(ctx, s, r)
				o.Status = RecipeApplied
				if o.Err != nil {
					o.Status = RecipeFailed
				}
			}
			switch o.Status {
			case RecipeFailed:
				failedJail[jk] = r.Name
			case RecipeSkipped:
				failedJail[jk] = r.Name
			case RecipeWaiting:
				note(o.Reason)
			}
		}
		res.Recipes = append(res.Recipes, o)
		if o.Status == RecipeConverged || o.Status == RecipeUnknown && r.Action == plan.RecipeMatch {
			continue // not a change: listed in the result, not in the log or the text
		}
		data := map[string]any{"host": r.Host, "kind": "Recipe", "project": r.Project, "name": r.Jail + "/" + r.Name, "hash": r.Hash, "status": o.Status, "steps": o.Steps}
		line := fmt.Sprintf("  %-8s %s %s", o.Status, r.Host, r.Ref())
		switch {
		case o.Err != nil:
			data["error"] = o.Err.Error()
			line += ": " + o.Err.Error()
		case o.Reason != "":
			data["reason"] = o.Reason
			line += ": " + o.Reason
		}
		opt.Log.Write(applylog.KindChange, data)
		fmt.Fprintf(opt.Out, "%s (recipe)\n", line)
		if len(o.Steps) > 0 {
			for _, l := range (api.ApplyResult{Steps: o.Steps}).Lines() {
				fmt.Fprintf(opt.Out, "      %s\n", l)
			}
		}
	}
	if err := s.Checks.Save(); err != nil {
		fmt.Fprintln(opt.Out, "warning: the memory of recipe checks was not saved:", err)
	}
}

// applyRecipe applies one recipe. The record on the jail is removed before the first step and written only after the
// last one succeeded, so a run that fails halfway leaves no record and the next run applies the recipe again, even when
// the files are by then as they should be and only a reload is missing.
func applyRecipe(ctx context.Context, s *plan.Session, r plan.RecipeItem) ([]api.ApplyStep, error) {
	res, err := r.Source.Resolve(plan.StoreResolver{Store: s.Store})
	if err != nil {
		return nil, err
	}
	if res.ValueErr != nil {
		return nil, fmt.Errorf("a secret cannot be read: %w", res.ValueErr)
	}
	c, err := s.Client(ctx, r.Host)
	if err != nil {
		return nil, err
	}
	c = c.Project(r.Project)
	label := config.RecipeLabel(r.Name)
	s.Checks.Forget(r.CheckKey())
	if r.Recorded != "" {
		if err := setLabel(ctx, c, r.Jail, label, nil); err != nil {
			return nil, fmt.Errorf("nothing was applied: the record of the recipe could not be removed first: %w", err)
		}
	}
	op, err := c.Jails().Apply(ctx, r.Jail, res.Request(false))
	var out api.ApplyResult
	if err == nil {
		out, err = op.Wait(ctx)
	}
	if err != nil {
		return out.Steps, err
	}
	if err := setLabel(ctx, c, r.Jail, label, &res.Hash); err != nil {
		return out.Steps, fmt.Errorf("the recipe was applied, but its record on the jail could not be written (it runs again next time): %w", err)
	}
	s.Checks.Set(r.CheckKey(), res.Hash)
	return out.Steps, nil
}

// forgetRecipe removes the record of a recipe the repository no longer lists.
func forgetRecipe(ctx context.Context, s *plan.Session, r plan.RecipeItem) error {
	c, err := s.Client(ctx, r.Host)
	if err != nil {
		return err
	}
	s.Checks.Forget(r.CheckKey())
	return setLabel(ctx, c.Project(r.Project), r.Jail, config.RecipeLabel(r.Name), nil)
}

// setLabel writes one label of a jail with a merge patch, which leaves every other label as it is; a nil value removes it.
func setLabel(ctx context.Context, c *sdk.Client, jail, label string, value *string) error {
	ad, err := plan.AdapterFor(api.KindJail)
	if err != nil {
		return err
	}
	live, etag, err := ad.Get(ctx, c, jail)
	if err != nil {
		return err
	}
	var v any
	if value != nil {
		v = *value
	}
	patch := map[string]any{"metadata": map[string]any{"labels": map[string]any{label: v}}}
	return ad.Patch(ctx, c, live.Meta.Name, patch, etag)
}

// keepRecipeLabels adds the records of recipes the live jail has to the labels sync is about to write, so that updating a
// jail does not make its recipes look new.
func keepRecipeLabels(want, live map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range want {
		out[k] = v
	}
	for k, v := range live {
		if strings.HasPrefix(k, config.RecipeLabelPrefix) {
			out[k] = v
		}
	}
	return out
}
