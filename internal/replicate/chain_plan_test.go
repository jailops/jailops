package replicate

import (
	"reflect"
	"testing"
	"time"

	"jailops.net/jailops/sdk/api"
)

func TestChainContinuity(t *testing.T) {
	c := Chain{ID: "c"}
	if err := c.CheckContinuity(0); err != nil {
		t.Fatal(err)
	}
	wantCode(t, c.CheckContinuity(5), api.CodeChainBroken)
	if err := c.Append(Received{Snapshot: "a", GUID: 10}); err != nil {
		t.Fatal(err)
	}
	if err := c.Append(Received{Snapshot: "b", GUID: 20, FromGUID: 10}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		from uint64
		ok   bool
	}{{"continues", 20, true}, {"stale base", 10, false}, {"unknown base", 99, false}, {"full over chain", 0, false}}
	for _, tc := range tests {
		err := c.CheckContinuity(tc.from)
		if tc.ok && err != nil || !tc.ok && !api.IsCode(err, api.CodeChainBroken) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	wantCode(t, c.Append(Received{Snapshot: "x", GUID: 20, FromGUID: 20}), api.CodeChainBroken) // duplicate guid
	if !c.Received[0].Full || c.Received[1].Full {
		t.Fatal("Full flag")
	}
}

func TestDetectFork(t *testing.T) {
	c := Chain{ID: "c", Received: []Received{{Snapshot: "a", GUID: 1}, {Snapshot: "b", GUID: 2, FromGUID: 1}, {Snapshot: "c", GUID: 3, FromGUID: 2}}}
	si := func(n string, g uint64) SnapInfo { return SnapInfo{Name: n, GUID: g} }
	tests := []struct {
		name  string
		snaps []SnapInfo
		bms   []SnapInfo
		kind  ForkKind
		comm  uint64
	}{
		{"in sync", []SnapInfo{si("c", 3)}, nil, ForkNone, 0},
		{"base only as bookmark", nil, []SnapInfo{si("x", 3)}, ForkNone, 0},
		{"host restored from older state", []SnapInfo{si("a", 1), si("b", 2), si("c", 77)}, nil, ForkRestored, 2},
		{"rewound without new snaps", []SnapInfo{si("a", 1)}, nil, ForkRestored, 1},
		{"nothing common", []SnapInfo{si("z", 50)}, nil, ForkNoCommon, 0},
		{"recreated with same names", []SnapInfo{si("a", 11), si("b", 12), si("c", 13)}, nil, ForkRestored, 0},
	}
	for _, tc := range tests {
		r := DetectFork(c, tc.snaps, tc.bms)
		if r.Kind != tc.kind || r.CommonGUID != tc.comm {
			t.Errorf("%s: %+v", tc.name, r)
		}
	}
	if r := DetectFork(Chain{}, nil, nil); r.Kind != ForkNone {
		t.Fatal("empty chain")
	}
}

func snaps(names ...string) []SnapInfo {
	var out []SnapInfo
	for i, n := range names {
		out = append(out, SnapInfo{Name: n, GUID: uint64(100 + i), Created: epoch.Add(time.Duration(i) * time.Hour)})
	}
	return out
}

func TestBuildPlan(t *testing.T) {
	s := snaps("a", "b", "c")
	chain := Chain{ID: "c", Received: []Received{{Snapshot: "a", GUID: 100, Full: true}}}

	p, err := BuildPlan(PlanInput{Chain: Chain{ID: "c"}, Snapshots: s, SourceControl: true})
	if err != nil || p.Kind != KindFull || p.To.Name != "c" || !p.NewChain || p.From != nil {
		t.Fatalf("full: %+v %v", p, err)
	}

	p, err = BuildPlan(PlanInput{Chain: chain, Snapshots: s, To: "b", SourceControl: true})
	if err != nil || p.Kind != KindIncremental || p.From.Name != "a" || p.To.Name != "b" {
		t.Fatalf("incr: %+v %v", p, err)
	}
	// Hold and release ordering: holds before send; bookmark before any release.
	wantBefore := []Step{{Op: OpHold, Snapshot: "b", Tag: "jailops-send-c"}, {Op: OpHold, Snapshot: "a", Tag: "jailops-send-c"}}
	if !reflect.DeepEqual(p.Before, wantBefore) {
		t.Fatalf("before %v", p.Before)
	}
	if p.After[0].Op != OpBookmark || p.After[0].Bookmark != BookmarkName("c", 101) {
		t.Fatalf("after %v", p.After)
	}
	for _, st := range p.After[1:] {
		if st.Op != OpRelease {
			t.Fatalf("after %v", p.After)
		}
	}

	// Base only as a bookmark: no hold on it, prunes the old one.
	bm := []SnapInfo{{Name: BookmarkName("c", 100), GUID: 100, Created: epoch}, {Name: "user-bm", GUID: 5}}
	p, err = BuildPlan(PlanInput{Chain: chain, Snapshots: s[1:], Bookmarks: bm, SourceControl: true})
	if err != nil || p.From == nil || !p.From.Bookmark || len(p.Before) != 1 {
		t.Fatalf("bookmark base: %+v %v", p, err)
	}
	var pruned []string
	for _, st := range p.After {
		if st.Op == OpDestroyBookmark {
			pruned = append(pruned, st.Bookmark)
		}
	}
	if len(pruned) != 1 || pruned[0] != BookmarkName("c", 100) {
		t.Fatalf("pruned %v (must never touch foreign bookmarks)", pruned)
	}

	// Pull has no source control: no steps.
	p, _ = BuildPlan(PlanInput{Chain: chain, Snapshots: s})
	if len(p.Before)+len(p.After)+len(p.OnFailure) != 0 {
		t.Fatal("steps without control")
	}

	// Noop.
	p, _ = BuildPlan(PlanInput{Chain: chain, Snapshots: s[:1]})
	if p.Kind != KindNoop {
		t.Fatalf("noop %v", p.Kind)
	}

	// Broken: base gone.
	_, err = BuildPlan(PlanInput{Chain: chain, Snapshots: snaps("x", "y")[1:]})
	wantCode(t, err, api.CodeChainBroken)
	p, err = BuildPlan(PlanInput{Chain: chain, Snapshots: snaps("x", "y")[1:], OnBroken: NewChainOnBroken})
	if err != nil || p.Kind != KindFull || p.ChainID == "c" || !p.NewChain || !ValidID(p.ChainID) {
		t.Fatalf("new chain %+v %v", p, err)
	}

	// Errors.
	_, err = BuildPlan(PlanInput{Chain: chain, Snapshots: s, To: "nope"})
	wantCode(t, err, api.CodeNotFound)
	_, err = BuildPlan(PlanInput{Chain: chain, Snapshots: s, Raw: true})
	wantCode(t, err, api.CodeInvalid) // raw flag differs from chain
	_, err = BuildPlan(PlanInput{Chain: Chain{ID: "bad id"}, Snapshots: s})
	wantCode(t, err, api.CodeInvalid)
	// Older target than head is refused.
	two := Chain{ID: "c", Received: []Received{{Snapshot: "c", GUID: 102, Full: true}}}
	_, err = BuildPlan(PlanInput{Chain: two, Snapshots: s, To: "a"})
	if err == nil {
		t.Fatal("older snapshot accepted")
	}
}

func TestBuildPlanResume(t *testing.T) {
	s := snaps("a", "b")
	chain := Chain{ID: "c", Received: []Received{{Snapshot: "a", GUID: 100, Full: true}}}
	p, _ := BuildPlan(PlanInput{Chain: chain, Snapshots: s, Resume: &ResumeInfo{Token: "t", FromGUID: 100, ToGUID: 101}})
	if p.Resume == nil || p.AbortResume {
		t.Fatalf("%+v", p)
	}
	p, _ = BuildPlan(PlanInput{Chain: chain, Snapshots: s, Resume: &ResumeInfo{Token: "t", FromGUID: 100, ToGUID: 555}})
	if p.Resume != nil || !p.AbortResume {
		t.Fatalf("%+v", p)
	}
}

func TestKeyHistoryAndEscrow(t *testing.T) {
	h := KeyHistory{{KeyID: "k2", From: epoch.Add(48 * time.Hour)}, {KeyID: "k1", From: epoch}}
	for _, tc := range []struct {
		at   time.Duration
		want string
		ok   bool
	}{{-time.Hour, "", false}, {0, "k1", true}, {47 * time.Hour, "k1", true}, {48 * time.Hour, "k2", true}, {900 * time.Hour, "k2", true}} {
		got, ok := h.KeyAt(epoch.Add(tc.at))
		if got != tc.want || ok != tc.ok {
			t.Errorf("%v: %q %v", tc.at, got, ok)
		}
	}
	k := escrowed()
	if !k.EscrowComplete() {
		t.Fatal("complete escrow rejected")
	}
	k.History = append(k.History, KeyEntry{KeyID: "k2", From: epoch.Add(time.Hour)})
	if k.EscrowComplete() {
		t.Fatal("escrow that misses a historic key counted as complete")
	}
	for _, mod := range []func(*KeyStatus){
		func(k *KeyStatus) { k.State = KeyPresent },
		func(k *KeyStatus) { k.Escrow = nil },
		func(k *KeyStatus) { k.Escrow.Recipients = nil },
		func(k *KeyStatus) { k.Escrow.BlobRef = "" },
		func(k *KeyStatus) { k.Escrow.KeyID = "other" },
	} {
		k := escrowed()
		mod(&k)
		if k.EscrowComplete() {
			t.Errorf("accepted %+v", k)
		}
		wantCode(t, CheckRecoverable(true, true, k), api.CodeInvalid)
	}
	if err := CheckRecoverable(true, true, escrowed()); err != nil {
		t.Fatal(err)
	}
	if err := CheckRecoverable(false, true, KeyStatus{}); err != nil {
		t.Fatal("non-raw is a plaintext question, not a key question")
	}
}

func TestCompareFeatures(t *testing.T) {
	if CompareFeatures([]string{"a"}, nil).Verdict != VerdictUnknown {
		t.Fatal("unknown")
	}
	if CompareFeatures([]string{"a"}, []string{"a", "b"}).Verdict != VerdictVerified {
		t.Fatal("verified")
	}
	r := CompareFeatures([]string{"a", "z"}, []string{"a"})
	if r.Verdict != VerdictNo || len(r.Reasons) != 1 {
		t.Fatalf("%+v", r)
	}
}

func TestReverter(t *testing.T) {
	var r Reverter
	var order []int
	for i := 1; i <= 3; i++ {
		r.Add(func() error { order = append(order, i); return nil })
	}
	if err := r.Revert(); err != nil || !reflect.DeepEqual(order, []int{3, 2, 1}) {
		t.Fatal(order, err)
	}
	var r2 Reverter
	r2.Add(func() error { t.Fatal("ran after Success"); return nil })
	r2.Success()
	r2.Revert()
}
