package api

import "testing"

func TestParseIfMatch(t *testing.T) {
	cases := []struct {
		in      string
		any     bool
		gens    []int64
		invalid bool
	}{
		{in: "", any: true},
		{in: "  ", any: true},
		{in: "*", any: true},
		{in: `"7"`, gens: []int64{7}},
		{in: ` "7" `, gens: []int64{7}},
		{in: `"7", "9"`, gens: []int64{7, 9}},
		{in: `"7","7"`, gens: []int64{7}},
		{in: `W/"7"`},                        // a weak tag never matches
		{in: `W/"7", "9"`, gens: []int64{9}}, // the strong one does
		{in: `"abc"`},                        // not a tag of ours
		{in: `"07"`},                         // not the form the server writes
		{in: `"-1"`},                         // not a generation
		{in: `7`, invalid: true},             // not quoted
		{in: `"7", *`, invalid: true},        // * stands alone
		{in: `"7`, invalid: true},            // unbalanced
		{in: `"7",`, invalid: true},          // an empty element
	}
	for _, c := range cases {
		m, err := ParseIfMatch(c.in)
		if (err != nil) != c.invalid {
			t.Fatalf("%q: err %v, want invalid=%v", c.in, err, c.invalid)
		}
		if c.invalid {
			continue
		}
		if m.Any != c.any || len(m.Gens) != len(c.gens) {
			t.Fatalf("%q: %+v, want any=%v gens=%v", c.in, m, c.any, c.gens)
		}
		for i := range c.gens {
			if m.Gens[i] != c.gens[i] {
				t.Fatalf("%q: %+v, want %v", c.in, m, c.gens)
			}
		}
	}
	m, _ := ParseIfMatch(`"7", "9"`)
	if !m.Matches(9) || m.Matches(8) {
		t.Fatal("Matches")
	}
	if w, _ := ParseIfMatch(`W/"7"`); w.Matches(7) {
		t.Fatal("a weak tag matched")
	}
	if a, _ := ParseIfMatch(""); !a.Matches(123) {
		t.Fatal("no header matches every generation")
	}
}
