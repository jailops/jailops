package capture

import "testing"

func TestGenOpsDeterministic(t *testing.T) {
	a := GenOps(42, 200)
	b := GenOps(42, 200)
	if len(a) != len(b) {
		t.Fatalf("lengths differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("op %d differs: %+v vs %+v", i, a[i], b[i])
		}
	}
	c := GenOps(43, 200)
	same := true
	for i := range a {
		if i >= len(c) || a[i] != c[i] {
			same = false
			break
		}
	}
	if same {
		t.Fatal("different seeds produced identical sequences")
	}
}

func TestGenOpsOnlyReferencesLiveFiles(t *testing.T) {
	ops := GenOps(7, 500)
	alive := map[string]bool{}
	for _, op := range ops {
		switch op.Kind {
		case OpCreate, OpCreateDir:
			alive[op.Path] = true
		case OpRename, OpHardlink:
			if !alive[op.Path] {
				t.Fatalf("%s references %q which is not alive", op.Kind, op.Path)
			}
			if op.Kind == OpRename {
				delete(alive, op.Path)
			}
			alive[op.NewPath] = true
		case OpDelete, OpChmod, OpChown, OpXattr, OpAcl, OpFlag, OpModify, OpTruncate:
			if !alive[op.Path] {
				t.Fatalf("%s references %q which is not alive", op.Kind, op.Path)
			}
			if op.Kind == OpDelete {
				delete(alive, op.Path)
			}
		}
	}
	final := Alive(ops)
	if len(final) != len(alive) {
		t.Fatalf("Alive() = %v, want %v", final, alive)
	}
	for p := range alive {
		if !final[p] {
			t.Fatalf("Alive() missing %q", p)
		}
	}
}
