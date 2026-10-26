package kv

import (
	"fmt"
	"testing"
)

func cmds() [][]byte {
	var out [][]byte
	for i := 0; i < 50; i++ {
		out = append(out, Encode(Command{Op: "set", K: fmt.Sprintf("k%d", i%7), X: fmt.Sprint(i)}))
		out = append(out, Encode(Command{Op: "add", K: "n", X: "3"}))
	}
	out = append(out, Encode(Command{Op: "del", K: "k1"}), []byte("garbage"), Encode(Command{V: 9, Op: "set", K: "z"}))
	return out
}

func TestDeterministic(t *testing.T) {
	a, b := New(), New()
	for _, c := range cmds() {
		a.Apply(c)
		b.Apply(c)
	}
	ca, ha := a.State()
	cb, hb := b.State()
	if ca != cb || ha != hb || ca != 103 {
		t.Fatalf("%d %d %s %s", ca, cb, ha, hb)
	}
	if v, _ := a.Get("n"); v != "150" {
		t.Fatal(v)
	}
	if _, ok := a.Get("k1"); ok {
		t.Fatal("del")
	}
	if _, ok := a.Get("z"); ok {
		t.Fatal("bad version applied")
	}
}

func TestOrderMatters(t *testing.T) {
	a, b := New(), New()
	x, y := Encode(Command{Op: "set", K: "a", X: "1"}), Encode(Command{Op: "set", K: "a", X: "2"})
	a.Apply(x)
	a.Apply(y)
	b.Apply(y)
	b.Apply(x)
	_, ha := a.State()
	_, hb := b.State()
	if ha == hb {
		t.Fatal("chain must be order sensitive")
	}
}

func TestSnapshotRestore(t *testing.T) {
	a := New()
	for _, c := range cmds() {
		a.Apply(c)
	}
	s1, _ := a.Snapshot()
	b := New()
	if err := b.Restore(s1); err != nil {
		t.Fatal(err)
	}
	s2, _ := b.Snapshot()
	if string(s1) != string(s2) {
		t.Fatal("snapshot not stable")
	}
	next := Encode(Command{Op: "set", K: "q", X: "1"})
	a.Apply(next)
	b.Apply(next)
	ca, ha := a.State()
	cb, hb := b.State()
	if ca != cb || ha != hb {
		t.Fatal("diverged after restore")
	}
	if b.Restore([]byte("{")) == nil {
		t.Fatal("bad snapshot accepted")
	}
}

func TestOnApply(t *testing.T) {
	f := New()
	var got []string
	f.OnApply = func(id string, err error) { got = append(got, id) }
	f.Apply(Encode(Command{Op: "set", K: "a", ID: "x1"}))
	if len(got) != 1 || got[0] != "x1" {
		t.Fatal(got)
	}
}
