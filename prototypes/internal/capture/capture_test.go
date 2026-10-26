package capture

import "testing"

func e(mtime, ctime int64, size int64) Entry {
	return Entry{Kind: 'f', Mode: 0o644, Size: size, MtimeNs: mtime, CtimeNs: ctime, Inode: 1}
}

func TestEntryDiffAndStable(t *testing.T) {
	a := e(1, 1, 10)
	b := a
	if d := EntryDiff(a, b); len(d) != 0 {
		t.Fatalf("identical entries differ: %v", d)
	}
	if !Stable(a, b) {
		t.Fatal("identical entries must be Stable")
	}
	b.Size = 20
	b.MtimeNs = 2
	d := EntryDiff(a, b)
	if len(d) != 2 {
		t.Fatalf("want 2 diffs, got %v", d)
	}
	b.CtimeNs = 1 // ctime unchanged, inode unchanged
	if !Stable(a, b) {
		t.Fatal("Stable must only look at inode and ctime")
	}
	b.Inode = 2
	if Stable(a, b) {
		t.Fatal("different inode must not be Stable")
	}
}

func TestCompare(t *testing.T) {
	prev := Tree{"a": e(1, 1, 1), "b": e(1, 1, 1), "c": e(1, 1, 1)}
	cur := Tree{"a": e(1, 1, 1), "b": e(2, 2, 2), "d": e(1, 1, 1)}
	cs := Compare(prev, cur)
	if !sameSet(cs.Added, []string{"d"}) {
		t.Errorf("added=%v", cs.Added)
	}
	if !sameSet(cs.Removed, []string{"c"}) {
		t.Errorf("removed=%v", cs.Removed)
	}
	if !sameSet(cs.Modified, []string{"b"}) {
		t.Errorf("modified=%v", cs.Modified)
	}
	if !cs.Equal(ChangeSet{Added: []string{"d"}, Removed: []string{"c"}, Modified: []string{"b"}}) {
		t.Fatal("Equal must ignore order")
	}
	other := ChangeSet{Added: []string{"d"}, Removed: []string{"c"}}
	if cs.Equal(other) {
		t.Fatal("missing modified entry must not be Equal")
	}
	if diag := cs.Disagreements(other); len(diag) == 0 {
		t.Fatal("expected a disagreement report")
	}
}

func TestDigestDeterministicAndSensitive(t *testing.T) {
	t1 := Tree{"a": e(1, 1, 1), "b": e(1, 1, 1)}
	t2 := Tree{"b": e(1, 999, 1), "a": e(1, 1, 1)} // different ctime, different map insertion order
	if Digest(t1) != Digest(t2) {
		t.Fatal("digest must not depend on ctime or map order")
	}
	t3 := Tree{"a": e(1, 1, 1), "b": e(2, 1, 1)} // different mtime
	if Digest(t1) == Digest(t3) {
		t.Fatal("digest must be sensitive to mtime")
	}
}

func TestXattrHash(t *testing.T) {
	if XattrHash(nil) != "" {
		t.Fatal("no xattrs must hash to empty string")
	}
	h1 := XattrHash([]string{"user.a=1", "user.b=2"})
	h2 := XattrHash([]string{"user.b=2", "user.a=1"})
	if h1 != h2 {
		t.Fatal("xattr hash must not depend on order")
	}
	h3 := XattrHash([]string{"user.a=1"})
	if h1 == h3 {
		t.Fatal("different xattr sets must hash differently")
	}
}
