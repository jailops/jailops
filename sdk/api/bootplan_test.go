package api_test

import (
	"reflect"
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

func bj(project, name string, auto bool, prio int, deps ...string) api.BootJail {
	b := api.BootJail{Key: project + "_" + name, Project: project, Name: name, Boot: api.BootPolicy{Autostart: auto, Priority: prio}}
	for _, d := range deps {
		b.Boot.Depends = append(b.Boot.Depends, project+"_"+d)
	}
	return b
}

func names(p api.BootPlan) []string {
	var out []string
	for _, e := range p.Order {
		out = append(out, e.Project+"/"+e.Jail)
	}
	return out
}

func TestPlanBootOrder(t *testing.T) {
	p := api.PlanBoot([]api.BootJail{
		bj("a", "web", true, 1, "db"), bj("a", "db", false, 9), bj("a", "cache", true, 0), bj("a", "idle", false, 0), bj("b", "edge", true, -1),
	})
	if got, want := names(p), []string{"b/edge", "a/cache", "a/db", "a/web"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order %v want %v", got, want)
	}
	if p.Order[2].Reason != "dependency of web" || p.Order[3].Reason != "autostart" || p.Order[3].Position != 4 || !reflect.DeepEqual(p.Order[3].Depends, []string{"db"}) {
		t.Fatalf("%+v", p.Order)
	}
	if len(p.NotStarted) != 0 {
		t.Fatalf("%+v", p.NotStarted)
	}
}

func TestPlanBootDependencyOfADependency(t *testing.T) {
	p := api.PlanBoot([]api.BootJail{bj("a", "web", true, 0, "app"), bj("a", "app", false, 0, "db"), bj("a", "db", false, 0)})
	if got, want := names(p), []string{"a/db", "a/app", "a/web"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order %v want %v", got, want)
	}
	if p.Order[0].Reason != "dependency of web" || p.Order[1].Reason != "dependency of web" {
		t.Fatalf("%+v", p.Order)
	}
}

func TestPlanBootCycleAndMissingDependency(t *testing.T) {
	p := api.PlanBoot([]api.BootJail{bj("a", "x", true, 0, "y"), bj("a", "y", true, 0, "x"), bj("a", "lost", true, 0, "ghost"), bj("a", "ok", true, 0)})
	if got := names(p); !reflect.DeepEqual(got, []string{"a/ok"}) {
		t.Fatalf("order %v", got)
	}
	why := map[string]string{}
	for _, s := range p.NotStarted {
		why[s.Jail] = s.Reason
	}
	if !strings.Contains(why["x"], "cycle") || !strings.Contains(why["y"], "cycle") || !strings.Contains(why["lost"], "ghost") {
		t.Fatalf("%+v", p.NotStarted)
	}
	// the reasons name the jails, not their keys
	if strings.Contains(why["x"], "a_") || strings.Contains(why["lost"], "a_") {
		t.Fatalf("keys in %+v", why)
	}
}

func TestPlanBootLockedJailAndItsDependents(t *testing.T) {
	vault := bj("a", "vault", true, 0)
	vault.Encrypted = true
	p := api.PlanBoot([]api.BootJail{vault, bj("a", "report", true, 0, "vault"), bj("a", "web", true, 1)})
	if got := names(p); !reflect.DeepEqual(got, []string{"a/web"}) {
		t.Fatalf("order %v", got)
	}
	why := map[string]string{}
	for _, s := range p.NotStarted {
		why[s.Jail] = s.Reason
	}
	if !strings.Contains(why["vault"], "jailops jail unlock vault") || !strings.Contains(why["report"], "depends on vault") {
		t.Fatalf("%+v", p.NotStarted)
	}
}

func TestPlanBootIsThePlanOfABootWhatEverRuns(t *testing.T) {
	web := bj("a", "web", true, 0)
	web.Running = true
	p := api.PlanBoot([]api.BootJail{web})
	if len(p.Order) != 1 || !p.Order[0].Running {
		t.Fatalf("%+v", p)
	}
}

func TestBootPlanVisible(t *testing.T) {
	p := api.PlanBoot([]api.BootJail{bj("a", "one", true, 0), bj("b", "two", true, 1), bj("a", "three", true, 2)})
	v := p.Visible(func(project string) bool { return project == "a" })
	if got := names(v); !reflect.DeepEqual(got, []string{"a/one", "a/three"}) || v.Order[1].Position != 2 {
		t.Fatalf("%v %+v", got, v.Order)
	}
	if v.Order == nil || v.NotStarted == nil {
		t.Fatal("empty lists are not nil")
	}
}
