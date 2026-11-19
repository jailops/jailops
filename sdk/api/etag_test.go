package api

import "testing"

func TestETagRoundTrip(t *testing.T) {
	tag := ETag("0b6d7f0e-1c2a-4f6b-9a3e-5d4c3b2a1908", 7)
	if tag != `"0b6d7f0e-1c2a-4f6b-9a3e-5d4c3b2a1908-7"` {
		t.Fatal(tag)
	}
	u, g, ok := ParseETag(tag)
	if !ok || u != "0b6d7f0e-1c2a-4f6b-9a3e-5d4c3b2a1908" || g != 7 {
		t.Fatalf("%q %d %v", u, g, ok)
	}
	for _, bad := range []string{``, `7`, `"7"`, `"-7"`, `"abc"`, `"u-07"`, `"u-x"`, `u-7`} {
		if _, _, ok := ParseETag(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestParseIfMatch(t *testing.T) {
	a, b := ETag("u1", 7), ETag("u1", 9)
	cases := []struct {
		in      string
		any     bool
		tags    []string
		invalid bool
	}{
		{in: "", any: true},
		{in: "  ", any: true},
		{in: "*", any: true},
		{in: a, tags: []string{a}},
		{in: " " + a + " ", tags: []string{a}},
		{in: a + ", " + b, tags: []string{a, b}},
		{in: a + "," + a, tags: []string{a}},
		{in: "W/" + a},                               // a weak tag never matches
		{in: "W/" + a + ", " + b, tags: []string{b}}, // the strong one does
		{in: `7`, invalid: true},                     // not quoted
		{in: a + `, *`, invalid: true},               // * stands alone
		{in: `"u-7`, invalid: true},                  // unbalanced
		{in: a + `,`, invalid: true},                 // an empty element
	}
	for _, c := range cases {
		m, err := ParseIfMatch(c.in)
		if (err != nil) != c.invalid {
			t.Fatalf("%q: err %v, want invalid=%v", c.in, err, c.invalid)
		}
		if c.invalid {
			continue
		}
		if m.Any != c.any || len(m.Tags) != len(c.tags) {
			t.Fatalf("%q: %+v, want any=%v tags=%v", c.in, m, c.any, c.tags)
		}
		for i := range c.tags {
			if m.Tags[i] != c.tags[i] {
				t.Fatalf("%q: %+v, want %v", c.in, m, c.tags)
			}
		}
	}
	m, _ := ParseIfMatch(a + ", " + b)
	if !m.Matches(b) || m.Matches(ETag("u1", 8)) || m.Matches(ETag("u2", 7)) {
		t.Fatal("Matches")
	}
	if w, _ := ParseIfMatch("W/" + a); w.Matches(a) {
		t.Fatal("a weak tag matched")
	}
	if n, _ := ParseIfMatch(""); !n.Matches(ETag("x", 123)) {
		t.Fatal("no header matches every object")
	}
}
