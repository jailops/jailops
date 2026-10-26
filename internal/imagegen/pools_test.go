package imagegen

import (
	"testing"

	"jailops.net/jailops/sdk/api"
)

// twoPools adds a second pool root to the world and wires both managers as peers.
func twoPools(t *testing.T) (*world, *Manager, *Manager) {
	t.Helper()
	w := newWorld(t)
	for _, d := range []string{"tank/fast", "tank/fast/images"} {
		if err := w.Z.Create(bg, d, map[string]string{"jailops:managed": "on"}); err != nil {
			t.Fatal(err)
		}
	}
	a := w.M
	b := &Manager{Layout: Layout{Root: "tank/fast", MountBase: "/fast"}, Work: a.Work, ZFS: a.ZFS, Exec: a.Exec, Files: a.Files,
		Executor: a.Executor, Specs: a.Specs, Now: a.Now}
	peers := func() []*Manager { return []*Manager{a, b} }
	a.Peers, b.Peers = peers, peers
	return w, a, b
}

func TestProvideBuildsOnDemandThenReceives(t *testing.T) {
	_, a, b := twoPools(t)
	// No generation anywhere: the first jail's pool builds one.
	g1, err := a.Provide(bg, "fb@stable")
	if err != nil || g1.Number != 1 || g1.Dataset != "tank/jailops/images/fb/g1" {
		t.Fatalf("%+v %v", g1, err)
	}
	// The other pool receives the same generation instead of building another.
	g2, err := b.Provide(bg, "fb@stable")
	if err != nil || g2.Number != 1 || g2.Dataset != "tank/fast/images/fb/g1" {
		t.Fatalf("%+v %v", g2, err)
	}
	if g2.Manifest != g1.Manifest {
		t.Fatalf("a received generation must carry the manifest of its source: %s vs %s", g2.Manifest, g1.Manifest)
	}
	if hs := a.Holders(bg, "fb", 1); len(hs) != 2 {
		t.Fatalf("holders %v", hs)
	}
	// Both copies are immutable generations with a hold and their own mountpoint.
	if h, _ := b.ZFS.Holds(bg, g2.Dataset+"@ro"); len(h) != 1 {
		t.Fatal("no hold on the received @ro")
	}
	if mp, _ := b.ZFS.Get(bg, g2.Dataset, "mountpoint"); mp != "/fast/images/fb/g1" {
		t.Fatalf("mountpoint %q", mp)
	}
	if v, _ := b.ZFS.Get(bg, g2.Dataset, PropKind); v != KindGeneration {
		t.Fatalf("kind %q", v)
	}
	// Asking again is a no-op.
	again, err := b.Provide(bg, "fb#g1")
	if err != nil || again.Dataset != g2.Dataset {
		t.Fatal(again, err)
	}
	// An unknown pinned generation does not exist on any pool.
	if _, err := b.Provide(bg, "fb#g7"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatal(err)
	}
	if _, err := b.Provide(bg, "fb@nope"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatal(err)
	}
}

func TestGenerationNumbersAreUniqueAcrossPools(t *testing.T) {
	w, a, b := twoPools(t)
	if _, err := a.Build(bg, "fb", api.ImageBuild{}); err != nil {
		t.Fatal(err)
	}
	// Building on the second pool continues the numbering instead of making another g1.
	w.repoVersion = "15.0p2"
	r, err := b.Build(bg, "fb", api.ImageBuild{FromGeneration: 1})
	if err != nil || r.Generation.Number != 2 || r.Generation.Parent != 1 {
		t.Fatalf("%+v %v", r.Generation, err)
	}
	// The parent was received on the second pool, so the clone is local to it.
	if o, _ := w.Z.Origin(bg, r.Generation.Dataset); o != "tank/fast/images/fb/g1@ro" {
		t.Fatalf("origin %q", o)
	}
	// The newest generation on any pool wins for a bare reference.
	g, err := a.Provide(bg, "fb")
	if err != nil || g.Number != 2 || g.Dataset != "tank/jailops/images/fb/g2" {
		t.Fatalf("%+v %v", g, err)
	}
	if n, _ := a.MaxNumber(bg, "fb"); n != 2 {
		t.Fatal(n)
	}
}

func TestReceiveFailureLeavesNothingBehind(t *testing.T) {
	sweepSetup := func(t *testing.T) *world {
		w, a, _ := twoPools(t)
		if _, err := a.Build(bg, "fb", api.ImageBuild{}); err != nil {
			t.Fatal(err)
		}
		return w
	}
	// twoPools wires the managers inside the world; rebuild b from a's peers for the op.
	sweep(t, sweepSetup, func(w *world) error {
		b := w.M.Peers()[1]
		_, err := b.Provide(bg, "fb@latest")
		return err
	})
}
