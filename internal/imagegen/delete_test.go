package imagegen

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"jailops.net/jailops/internal/lifecycle/fault"
	"jailops.net/jailops/sdk/api"
)

func buildN(t *testing.T, w *world, image string, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		w.repoVersion = "v" + string(rune('0'+i))
		req := api.ImageBuild{}
		if i > 1 {
			req.FromGeneration = i - 1
		}
		if _, err := w.M.Build(bg, image, req); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDeleteRefused(t *testing.T) {
	w := newWorld(t)
	buildN(t, w, "fb", 2)
	w.M.SetAlias(bg, "fb", "stable", 2)
	jails := []JailRef{{Name: "web", Image: "fb@stable", Current: "fb#g1"}, {Name: "db", Image: "fb#g2"}}
	if _, err := w.M.Delete(bg, "fb", 1, jails, false); !api.IsCode(err, api.CodeImageInUse) {
		t.Fatalf("current generation of a jail must block: %v", err)
	}
	if _, err := w.M.Delete(bg, "fb", 1, []JailRef{{Name: "x", Image: "fb@latest", Previous: "fb#g1"}}, false); !api.IsCode(err, api.CodeImageInUse) {
		t.Fatalf("rollback target must block: %v", err)
	}
	if _, err := w.M.Delete(bg, "fb", 9, nil, false); !api.IsCode(err, api.CodeNotFound) {
		t.Fatal(err)
	}
}

func noAliasWorld(t *testing.T) *world {
	w := newWorld(t)
	w.AddImage("fb") // no aliases
	return w
}

func TestDeleteDestroysWhenNoClones(t *testing.T) {
	w := noAliasWorld(t)
	buildN(t, w, "fb", 1)
	res, err := w.M.Delete(bg, "fb", 1, nil, false)
	if err != nil || res.Parked {
		t.Fatal(res, err)
	}
	if ok, _ := w.Z.Exists(bg, "tank/jailops/images/fb/g1"); ok {
		t.Fatal("still there")
	}
}

func TestDeleteParksThenCollects(t *testing.T) {
	w := noAliasWorld(t)
	buildN(t, w, "fb", 2)
	// a jail root cloned from g2 (not in the specs passed to Delete: ZFS safety net)
	w.Z.Create(bg, "tank/jailops/jails/j", nil)
	if err := w.Z.Clone(bg, "tank/jailops/images/fb/g2@ro", "tank/jailops/jails/j/root", nil); err != nil {
		t.Fatal(err)
	}
	r, err := w.M.Delete(bg, "fb", 1, nil, false)
	if err != nil || !r.Parked {
		t.Fatal(r, err)
	}
	r, err = w.M.Delete(bg, "fb", 2, nil, false)
	if err != nil || !r.Parked || len(r.Collected) != 0 {
		t.Fatal(r, err)
	}
	o, _ := w.M.Observe(bg, "fb")
	if !reflect.DeepEqual(o.Deleted, []int{1, 2}) || len(o.Status.Generations) != 0 {
		t.Fatalf("%+v", o)
	}
	if o.MaxNumber != 2 {
		t.Fatal("numbers must not be reused", o.MaxNumber)
	}
	// parked generation is still a valid clone origin
	if org, _ := w.Z.Origin(bg, "tank/jailops/jails/j/root"); org != "tank/jailops/deleted/fb-g2@ro" {
		t.Fatal(org)
	}
	// the last clone goes: g2, then g1 (freed by g2) are collected
	w.Z.Destroy(bg, "tank/jailops/jails/j/root")
	got, err := w.M.Collect(bg)
	if err != nil || len(got) != 2 {
		t.Fatal(got, err)
	}
	if ns := w.Z.Names(); len(ns) != 6 { // tank, jailops, images, jails, jails/j, images/fb... deleted
		t.Logf("datasets: %v", ns)
	}
	if ok, _ := w.Z.Exists(bg, "tank/jailops/deleted/fb-g1"); ok {
		t.Fatal("g1 not collected")
	}
	// a new build continues numbering
	if r, err := w.M.Build(bg, "fb", api.ImageBuild{}); err != nil || r.Generation.Number != 3 {
		t.Fatal(r.Generation, err)
	}
}

func TestCollectLeavesUnmanaged(t *testing.T) {
	w := noAliasWorld(t)
	w.Z.Create(bg, "tank/jailops/deleted", nil)
	w.Z.Create(bg, "tank/jailops/deleted/foreign", nil)
	if got, err := w.M.Collect(bg); err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
	if ok, _ := w.Z.Exists(bg, "tank/jailops/deleted/foreign"); !ok {
		t.Fatal("foreign dataset destroyed")
	}
}

func deleteSetup(withClone bool) func(t *testing.T) *world {
	return func(t *testing.T) *world {
		w := noAliasWorld(t)
		buildN(t, w, "fb", 2)
		if withClone {
			w.Z.Create(bg, "tank/jailops/jails/j", nil)
			w.Z.Clone(bg, "tank/jailops/images/fb/g1@ro", "tank/jailops/jails/j/root", nil)
		}
		return w
	}
}

func TestDeleteFailureSweep(t *testing.T) {
	for _, withClone := range []bool{false, true} {
		sweep(t, deleteSetup(withClone), func(w *world) error {
			// g1 has g2 as a clone, so both variants park; the no-clone variant deletes g2
			n := 1
			if !withClone {
				n = 2
			}
			r, err := w.M.Delete(bg, "fb", n, nil, false)
			if err == nil && r.CollectErr != nil {
				return nil
			}
			return err
		})
	}
}

func TestDeleteDryRun(t *testing.T) {
	w := noAliasWorld(t)
	buildN(t, w, "fb", 2)
	before := w.state()
	w.F.Reset(0)
	r, err := w.M.Delete(bg, "fb", 1, nil, true)
	if err != nil || !r.Parked || len(r.Plan) == 0 || w.F.Count() != 0 || w.state() != before {
		t.Fatal(r, err)
	}
}

func TestDeleteInjectedErrorIsReported(t *testing.T) {
	w := noAliasWorld(t)
	buildN(t, w, "fb", 1)
	w.F.Reset(1)
	if _, err := w.M.Delete(bg, "fb", 1, nil, false); !errors.Is(err, fault.ErrInjected) {
		t.Fatal(err)
	}
}

func TestRefs(t *testing.T) {
	st := api.ImageStatus{Aliases: map[string]int{"stable": 2, "latest": 3}, Generations: []api.ImageGeneration{{Number: 1}, {Number: 2}, {Number: 3}}}
	jails := []JailRef{
		{Name: "a", Image: "fb@stable"},
		{Name: "b", Image: "fb#g1"},
		{Name: "c", Image: "fb@latest", Current: "fb#g2", Previous: "other#g1"},
		{Name: "d", Image: "other@stable", Current: "other#g2"},
		{Name: "e", Image: "garbage!"},
	}
	got := Refs("fb", st, jails)
	want := map[int][]string{1: {"b"}, 2: {"a", "c"}, 3: {"c"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
	ApplyRefs("fb", &st, jails)
	if !reflect.DeepEqual(st.Generations[1].Refs, []string{"a", "c"}) {
		t.Fatal(st.Generations)
	}
}

func TestCandidates(t *testing.T) {
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	mk := func(n, daysAgo int) api.ImageGeneration {
		return api.ImageGeneration{Number: n, CreatedAt: now.AddDate(0, 0, -daysAgo)}
	}
	st := api.ImageStatus{Generations: []api.ImageGeneration{mk(1, 100), mk(2, 80), mk(3, 60), mk(4, 40), mk(5, 5), mk(6, 1)},
		Aliases: map[string]int{"stable": 2}}
	tests := []struct {
		name  string
		ret   api.ImageRetention
		jails []JailRef
		want  []int
	}{
		{"no policy keeps all", api.ImageRetention{}, nil, nil},
		{"keep last 2", api.ImageRetention{KeepLast: 2}, nil, []int{1, 3, 4}},
		{"keep days 30", api.ImageRetention{KeepDays: 30}, nil, []int{1, 3, 4}},
		{"both", api.ImageRetention{KeepLast: 4, KeepDays: 30}, nil, []int{1}},
		{"referenced kept", api.ImageRetention{KeepLast: 1}, []JailRef{{Name: "j", Image: "fb#g4"}}, []int{1, 3, 5}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Candidates(tc.ret, "fb", st, tc.jails, now)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("%v want %v", got, tc.want)
			}
		})
	}
}

func TestPrune(t *testing.T) {
	w := noAliasWorld(t)
	buildN(t, w, "fb", 4)
	got, err := w.M.Prune(bg, "fb", api.ImageRetention{KeepLast: 1}, []JailRef{{Name: "j", Image: "fb#g2"}})
	if err != nil {
		t.Fatal(err)
	}
	// g1 and g3 are candidates; both have clones (g2, g4) so they are parked
	if !reflect.DeepEqual(got, []int{1, 3}) {
		t.Fatal(got)
	}
	o, _ := w.M.Observe(bg, "fb")
	if len(o.Status.Generations) != 2 || len(o.Deleted) != 2 {
		t.Fatalf("%+v", o)
	}
}

func aliasesOf(t *testing.T, w *world, image string) map[string]int {
	t.Helper()
	o, err := w.M.Observe(bg, image)
	if err != nil {
		t.Fatal(err)
	}
	return o.Status.Aliases
}

func TestDeleteMovesAliasesToTheNextNewestGeneration(t *testing.T) {
	w := newWorld(t) // aliases stable and latest
	buildN(t, w, "fb", 3)
	if err := w.M.SetAlias(bg, "fb", "stable", 2); err != nil {
		t.Fatal(err)
	}
	// dry run shows the alias step and changes nothing
	res, err := w.M.Delete(bg, "fb", 3, nil, true)
	if err != nil || len(res.Plan) == 0 || !reflect.DeepEqual(aliasesOf(t, w, "fb"), map[string]int{"latest": 3, "stable": 2}) {
		t.Fatalf("%v %v %v", res.Plan, err, aliasesOf(t, w, "fb"))
	}
	if _, err := w.M.Delete(bg, "fb", 3, nil, false); err != nil {
		t.Fatal(err)
	}
	if got := aliasesOf(t, w, "fb"); !reflect.DeepEqual(got, map[string]int{"latest": 2, "stable": 2}) {
		t.Fatalf("latest must move to the next newest generation: %v", got)
	}
	if _, err := w.M.Delete(bg, "fb", 2, nil, false); err != nil {
		t.Fatal(err)
	}
	if got := aliasesOf(t, w, "fb"); !reflect.DeepEqual(got, map[string]int{"latest": 1, "stable": 1}) {
		t.Fatalf("%v", got)
	}
	if _, err := w.M.Delete(bg, "fb", 1, nil, false); err != nil {
		t.Fatal(err)
	}
	if got := aliasesOf(t, w, "fb"); len(got) != 0 {
		t.Fatalf("aliases must go with the last generation: %v", got)
	}
}

func TestDeleteGenerationUsedByJailKeepsAliases(t *testing.T) {
	w := newWorld(t)
	buildN(t, w, "fb", 2)
	_, err := w.M.Delete(bg, "fb", 2, []JailRef{{Name: "web", Image: "fb@latest"}}, false)
	if !api.IsCode(err, api.CodeImageInUse) || aliasesOf(t, w, "fb")["latest"] != 2 {
		t.Fatalf("%v %v", err, aliasesOf(t, w, "fb"))
	}
}

func TestDeleteAliasAndImage(t *testing.T) {
	w := newWorld(t)
	buildN(t, w, "fb", 1)
	if ok, err := w.M.DeleteAlias(bg, "fb", "stable"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, _ := w.M.DeleteAlias(bg, "fb", "stable"); ok {
		t.Fatal("already gone")
	}
	if got := aliasesOf(t, w, "fb"); !reflect.DeepEqual(got, map[string]int{"latest": 1}) {
		t.Fatal(got)
	}
	if _, err := w.M.Delete(bg, "fb", 1, nil, false); err != nil {
		t.Fatal(err)
	}
	if err := w.M.DeleteImage(bg, "fb"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := w.Z.Exists(bg, "tank/jailops/images/fb"); ok {
		t.Fatal("image dataset still there")
	}
}
