package schedule

import (
	"testing"
	"time"
)

func mustLoc(t *testing.T, n string) *time.Location {
	t.Helper()
	l, err := time.LoadLocation(n)
	if err != nil {
		t.Skipf("tzdata missing: %v", err) // environment, not logic
	}
	return l
}

func TestParseErrors(t *testing.T) {
	for _, e := range []string{"", "* * * *", "* * * * * *", "60 * * * *", "* 24 * * *", "* * 0 * *",
		"* * * 13 *", "* * * * 8", "5-1 * * * *", "*/0 * * * *", "a * * * *", "* * 30 2 *", "-1 * * * *", "1,,2 * * * *"} {
		if _, err := Parse(e); err == nil {
			t.Errorf("Parse(%q) accepted", e)
		}
	}
}

func TestNextUTC(t *testing.T) {
	base := time.Date(2025, 3, 14, 10, 7, 30, 0, time.UTC)
	tests := []struct{ expr, want string }{
		{"* * * * *", "2025-03-14T10:08:00Z"},
		{"*/15 * * * *", "2025-03-14T10:15:00Z"},
		{"7 10 * * *", "2025-03-15T10:07:00Z"}, // strictly after
		{"0 3 * * *", "2025-03-15T03:00:00Z"},
		{"0 0 1 * *", "2025-04-01T00:00:00Z"},
		{"0 0 * * mon", "2025-03-17T00:00:00Z"},
		{"0 0 * * 7", "2025-03-16T00:00:00Z"},
		{"0 0 * * 0", "2025-03-16T00:00:00Z"},
		{"30 4 1,15 * *", "2025-03-15T04:30:00Z"},
		{"0 0 29 feb *", "2028-02-29T00:00:00Z"},
		{"0 12 * jun-aug 1-5", "2025-06-02T12:00:00Z"},
		{"10-20/5 11 * * *", "2025-03-14T11:10:00Z"},
		{"5/20 10 * * *", "2025-03-14T10:25:00Z"},
		{"0 0 13 * fri", "2025-03-15T00:00:00Z"},
	}
	// the last case is verified below separately; compute properly
	tests[len(tests)-1].want = "2025-03-21T00:00:00Z"
	for _, tc := range tests {
		s, err := Parse(tc.expr)
		if err != nil {
			t.Fatalf("%q: %v", tc.expr, err)
		}
		got, ok := s.Next(base, time.UTC)
		if !ok || got.Format(time.RFC3339) != tc.want {
			t.Errorf("%q: got %v ok=%v want %s", tc.expr, got, ok, tc.want)
		}
	}
}

func TestDomDowOr(t *testing.T) {
	s, _ := Parse("0 0 13 * fri")
	// From Mar 1 2025: Fri Mar 7 (dow) comes before the 13th.
	got, _ := s.Next(time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC), time.UTC)
	if got.Day() != 7 {
		t.Fatalf("got %v", got)
	}
}

func TestDSTSpringForward(t *testing.T) {
	loc := mustLoc(t, "America/New_York")
	// 2025-03-09 02:00 EST jumps to 03:00 EDT.
	s, _ := Parse("30 2 * * *")
	after := time.Date(2025, 3, 8, 12, 0, 0, 0, loc)
	got, _ := s.Next(after, loc)
	if want := time.Date(2025, 3, 9, 7, 0, 0, 0, time.UTC); !got.Equal(want) { // 03:00 EDT
		t.Fatalf("gap fire: got %v want %v", got.UTC(), want)
	}
	// Exactly one fire per day: next is Mar 10 02:30.
	got2, _ := s.Next(got, loc)
	if want := time.Date(2025, 3, 10, 2, 30, 0, 0, loc); !got2.Equal(want) {
		t.Fatalf("after gap: got %v want %v", got2, want)
	}
	// Wildcard minutes inside the gap fire once at gap end, then continue.
	w, _ := Parse("*/10 2-3 * * *")
	a := time.Date(2025, 3, 9, 1, 59, 0, 0, loc)
	g1, _ := w.Next(a, loc)
	if want := time.Date(2025, 3, 9, 7, 0, 0, 0, time.UTC); !g1.Equal(want) {
		t.Fatalf("g1 %v", g1.UTC())
	}
	g2, _ := w.Next(g1, loc)
	if want := time.Date(2025, 3, 9, 7, 10, 0, 0, time.UTC); !g2.Equal(want) {
		t.Fatalf("g2 %v", g2.UTC())
	}
}

func TestDSTFallBack(t *testing.T) {
	loc := mustLoc(t, "America/New_York")
	// 2025-11-02 02:00 EDT falls back to 01:00 EST: 01:30 happens twice.
	s, _ := Parse("30 1 * * *")
	after := time.Date(2025, 11, 1, 12, 0, 0, 0, loc)
	first, _ := s.Next(after, loc)
	if want := time.Date(2025, 11, 2, 5, 30, 0, 0, time.UTC); !first.Equal(want) { // EDT
		t.Fatalf("first %v", first.UTC())
	}
	next, _ := s.Next(first, loc)
	if want := time.Date(2025, 11, 3, 1, 30, 0, 0, loc); !next.Equal(want) {
		t.Fatalf("must not fire in the repeated hour: %v", next)
	}
	// Starting inside the second pass also skips the repeat.
	second := time.Date(2025, 11, 2, 6, 30, 0, 0, time.UTC) // 01:30 EST
	n2, _ := s.Next(second.Add(-time.Minute), loc)
	if !n2.Equal(next) {
		t.Fatalf("n2 %v want %v", n2, next)
	}
}

func TestNextMonotoneAcrossYear(t *testing.T) {
	loc := mustLoc(t, "Europe/Berlin")
	for _, e := range []string{"*/7 * * * *", "30 2 * * *", "0 * * * *", "15 1,2,3 * * 0"} {
		s, _ := Parse(e)
		cur := time.Date(2025, 1, 1, 0, 0, 0, 0, loc)
		end := cur.AddDate(1, 0, 0)
		n := 0
		for cur.Before(end) && n < 200000 {
			nx, ok := s.Next(cur, loc)
			if !ok || !nx.After(cur) {
				t.Fatalf("%q: not strictly increasing: %v -> %v", e, cur, nx)
			}
			cur = nx
			n++
		}
	}
}

func TestJitter(t *testing.T) {
	w := 10 * time.Minute
	a, b := Jitter("jail-a", w), Jitter("jail-b", w)
	if a != Jitter("jail-a", w) {
		t.Fatal("not deterministic")
	}
	if a == b {
		t.Fatal("collision on two ids (extremely unlikely)")
	}
	seen := map[time.Duration]bool{}
	for i := 0; i < 500; i++ {
		j := Jitter(string(rune('a'+i%26))+time.Duration(i).String(), w)
		if j < 0 || j >= w {
			t.Fatalf("out of range %v", j)
		}
		seen[j/time.Minute] = true
	}
	if len(seen) < 8 {
		t.Fatalf("poor spread: %d buckets", len(seen))
	}
	if Jitter("x", 0) != 0 || Jitter("x", -1) != 0 {
		t.Fatal("zero window")
	}
}

func FuzzParse(f *testing.F) {
	f.Add("*/5 * * * *")
	f.Add("0 0 29 feb *")
	f.Fuzz(func(t *testing.T, s string) {
		sp, err := Parse(s)
		if err != nil {
			return
		}
		a := time.Date(2025, 5, 5, 5, 5, 5, 0, time.UTC)
		if n, ok := sp.Next(a, time.UTC); ok && !n.After(a) {
			t.Fatalf("%q: %v", s, n)
		}
	})
}
