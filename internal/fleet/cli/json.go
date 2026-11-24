package cli

import (
	"encoding/json"
	"io"

	"jailops.net/jailops/internal/fleet/apply"
	"jailops.net/jailops/internal/fleet/plan"
	"jailops.net/jailops/sdk/api"
)

// The JSON form of a plan and of the result of an apply. It names what is done to which object and recipe; it never
// holds a value, a secret or the content of a file.

type jsonObject struct {
	Host    string   `json:"host"`
	Kind    string   `json:"kind"`
	Project string   `json:"project,omitempty"`
	Name    string   `json:"name"`
	Action  string   `json:"action"`
	Details []string `json:"details,omitempty"`
	Status  string   `json:"status,omitempty"`
	Error   string   `json:"error,omitempty"`
}

type jsonRecipe struct {
	Host     string          `json:"host"`
	Project  string          `json:"project"`
	Jail     string          `json:"jail"`
	Recipe   string          `json:"recipe"`
	Action   string          `json:"action"`
	Status   string          `json:"status,omitempty"`
	Why      string          `json:"why,omitempty"`
	Hash     string          `json:"hash,omitempty"`
	Recorded string          `json:"recorded,omitempty"`
	Steps    []api.ApplyStep `json:"steps,omitempty"`
	Error    string          `json:"error,omitempty"`
}

type jsonReport struct {
	Summary string       `json:"summary"`
	Hosts   []string     `json:"hosts"`
	Items   []jsonObject `json:"items"`
	Recipes []jsonRecipe `json:"recipes"`
	Errors  []string     `json:"errors,omitempty"`
	// Result is set by sync --apply: what was done, object by object and recipe by recipe.
	Result *jsonResult `json:"result,omitempty"`
}

type jsonResult struct {
	Objects []jsonObject `json:"objects"`
	Recipes []jsonRecipe `json:"recipes"`
	Failed  int          `json:"failed"`
}

func planReport(p *plan.Plan) jsonReport {
	r := jsonReport{Summary: p.Summary(), Hosts: p.Hosts, Items: []jsonObject{}, Recipes: []jsonRecipe{}}
	for _, it := range p.Items {
		r.Items = append(r.Items, jsonObject{Host: it.Host, Kind: string(it.Kind), Project: it.Project, Name: it.Name, Action: string(it.Action), Details: it.Details})
	}
	for _, rc := range p.Recipes {
		r.Recipes = append(r.Recipes, jsonRecipe{Host: rc.Host, Project: rc.Project, Jail: rc.Jail, Recipe: rc.Name, Action: string(rc.Action), Status: string(rc.Status),
			Why: rc.Why, Hash: rc.Hash, Recorded: rc.Recorded, Steps: rc.Steps})
	}
	for _, e := range p.Errors {
		r.Errors = append(r.Errors, "host "+e.Host+": "+e.Err.Error())
	}
	return r
}

func resultReport(res apply.Result) *jsonResult {
	out := &jsonResult{Objects: []jsonObject{}, Recipes: []jsonRecipe{}}
	for _, o := range res.Outcomes {
		jo := jsonObject{Host: o.Item.Host, Kind: string(o.Item.Kind), Project: o.Item.Project, Name: o.Item.Name, Action: string(o.Item.Action), Status: o.Status}
		if o.Err != nil {
			jo.Error = o.Err.Error()
		}
		out.Objects = append(out.Objects, jo)
	}
	for _, o := range res.Recipes {
		jr := jsonRecipe{Host: o.Item.Host, Project: o.Item.Project, Jail: o.Item.Jail, Recipe: o.Item.Name, Action: string(o.Item.Action), Status: o.Status,
			Why: o.Reason, Hash: o.Item.Hash, Steps: o.Steps}
		if o.Err != nil {
			jr.Error = o.Err.Error()
		}
		out.Recipes = append(out.Recipes, jr)
	}
	out.Failed = len(res.Failed()) + len(res.FailedRecipes())
	return out
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
