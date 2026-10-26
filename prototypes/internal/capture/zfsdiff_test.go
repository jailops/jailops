package capture

import "testing"

func TestParseZfsDiff(t *testing.T) {
	out := "1000.1\tM\tF\t/a\n1000.2\t+\tF\t/b\n1000.3\t-\tF\t/c\n1000.4\tR\tF\t/d\t/e\n"
	ops, err := ParseZfsDiff(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 4 {
		t.Fatalf("got %d ops", len(ops))
	}
	if ops[3].Change != 'R' || ops[3].Path != "/d" || ops[3].NewPath != "/e" {
		t.Fatalf("rename parsed wrong: %+v", ops[3])
	}
	cs := ZfsDiffChangeSet(ops)
	want := ChangeSet{Added: []string{"/b", "/e"}, Removed: []string{"/c", "/d"}, Modified: []string{"/a"}}
	if !cs.Equal(want) {
		t.Fatalf("got %+v want %+v", cs, want)
	}
}

func TestParseZfsDiffErrors(t *testing.T) {
	cases := []string{
		"too\tfew\tfields",
		"1000\tX\tF\t/a",       // unknown change code
		"1000\tR\tF\t/a",       // rename missing new path
		"notanumber\tM\tF\t/a", // bad timestamp
	}
	for _, c := range cases {
		if _, err := ParseZfsDiff(c); err == nil {
			t.Errorf("expected error for %q", c)
		}
	}
}

func TestParseZfsDiffEmpty(t *testing.T) {
	ops, err := ParseZfsDiff("")
	if err != nil || len(ops) != 0 {
		t.Fatalf("ops=%v err=%v", ops, err)
	}
}
