package zfsrepl

import "testing"

func TestParseSnaps(t *testing.T) {
	s, err := ParseSnaps("tank/a@x\t111\t10\ntank/a@y\t222\t12\n")
	if err != nil || len(s) != 2 {
		t.Fatal(err, s)
	}
	l, _ := Latest(s)
	if l.Name != "y" || l.Guid != "222" {
		t.Fatal(l)
	}
	if _, ok := ByName(s, "x"); !ok {
		t.Fatal("byname")
	}
	if _, err := ParseSnaps("garbage"); err == nil {
		t.Fatal("want error")
	}
	if _, err := ParseSnaps("tank/a\t1\t2"); err == nil {
		t.Fatal("want error")
	}
	if s, err := ParseSnaps(""); err != nil || len(s) != 0 {
		t.Fatal("empty")
	}
}

func TestNameGuidHolds(t *testing.T) {
	m, err := ParseNameGuid("p/a#b1\t77\np/a@s\t88\n")
	if err != nil || m["p/a#b1"] != "77" {
		t.Fatal(m, err)
	}
	h := ParseHolds("p/a@s\tkeep\tThu Jan  1 00:00 2026\n")
	if !HasHold(h, "keep") || HasHold(h, "x") {
		t.Fatal(h)
	}
}

func TestToken(t *testing.T) {
	if _, ok := ResumeToken("-"); ok {
		t.Fatal("dash is none")
	}
	tok, ok := ResumeToken("1-c6c2e0b8d-110-789c63\n")
	if !ok || !ValidToken(tok) {
		t.Fatal(tok)
	}
	for _, bad := range []string{"", "x", "1-zz-1", "tank/x", "1-ab cd"} {
		if ValidToken(bad) {
			t.Errorf("%q valid", bad)
		}
	}
}

func TestContinuity(t *testing.T) {
	if Continuity("1", "1") != nil {
		t.Fatal()
	}
	for _, c := range [][2]string{{"1", "2"}, {"", "1"}, {"1", ""}, {"1", "-"}} {
		if Continuity(c[0], c[1]) == nil {
			t.Errorf("%v", c)
		}
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]RecvKind{
		"cannot receive new filesystem stream: destination 'p/x' exists\nmust specify -F to overwrite it":   RecvExists,
		"cannot receive incremental stream: destination p/x has been modified\nsince most recent snapshot":  RecvModified,
		"cannot receive incremental stream: most recent snapshot of p/x does not\nmatch incremental source": RecvBaseMismatch,
		"cannot receive new filesystem stream: local origin for clone p/x does not exist":                   RecvOriginMissing,
		"something else": RecvOther,
	}
	for in, want := range cases {
		if got := ClassifyRecv(1, in); got != want {
			t.Errorf("%q: %s want %s", in, got, want)
		}
	}
	if ClassifyRecv(0, "") != RecvOK {
		t.Fatal()
	}
}

func TestExpectationsUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range Expectations() {
		if seen[e.ID] || e.Claim == "" {
			t.Fatal(e)
		}
		seen[e.ID] = true
	}
}
