package retention

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"jailops.net/jailops/sdk/api"
)

var t0 = time.Date(2025, 6, 30, 12, 0, 0, 0, time.UTC)

func gen(n int, step time.Duration) []Snapshot {
	var s []Snapshot
	for i := 0; i < n; i++ {
		s = append(s, Snapshot{Name: fmt.Sprintf("s%04d", i), Time: t0.Add(-time.Duration(i) * step)})
	}
	return s
}

func names(d []Decision) map[string]bool {
	m := map[string]bool{}
	for _, x := range d {
		m[x.Snapshot.Name] = true
	}
	return m
}

func TestDailyAndLast(t *testing.T) {
	s := gen(10*24, time.Hour) // 10 days hourly
	r := Evaluate(s, api.RetentionPolicy{Last: 2, Daily: 3}, t0, Options{})
	k := names(r.Keep)
	// last 2: s0000 s0001; daily 3: newest of each of the 3 newest days.
	for _, n := range []string{"s0000", "s0001", "s0013", "s0037"} {
		if !k[n] {
			t.Errorf("%s missing from keep: %v", n, k)
		}
	}
	if len(r.Keep) != 4 {
		t.Errorf("keep %d: %v", len(r.Keep), k)
	}
	if len(r.Keep)+len(r.Delete) != len(s) {
		t.Fatal("partition broken")
	}
}

func TestProtections(t *testing.T) {
	s := gen(20, 24*time.Hour)
	s[15].Held = true
	s[16].LastCommon = true
	s[17].HasClones = true
	s[19].Full = true
	r := Evaluate(s, api.RetentionPolicy{Last: 1}, t0, Options{SafetyWindow: 3 * 24 * time.Hour})
	k := names(r.Keep)
	for _, n := range []string{"s0000", "s0001", "s0002", "s0015", "s0016", "s0017", "s0019"} {
		if !k[n] {
			t.Errorf("%s should be protected", n)
		}
	}
	if len(r.Keep) != 7 {
		t.Errorf("keep=%d", len(r.Keep))
	}
	// Two fulls: neither is "the only full".
	s[18].Full = true
	r = Evaluate(s, api.RetentionPolicy{Last: 1}, t0, Options{})
	if names(r.Keep)["s0019"] {
		t.Error("with two fulls the old one is deletable")
	}
}

func TestNoPolicyKeepsAll(t *testing.T) {
	r := Evaluate(gen(5, time.Hour), api.RetentionPolicy{}, t0, Options{})
	if len(r.Delete) != 0 {
		t.Fatal("zero policy deleted")
	}
}

func TestWeeklyMonthlyYearly(t *testing.T) {
	var s []Snapshot
	for d := 0; d < 800; d++ {
		s = append(s, Snapshot{Name: fmt.Sprintf("d%03d", d), Time: t0.AddDate(0, 0, -d)})
	}
	r := Evaluate(s, api.RetentionPolicy{Weekly: 4, Monthly: 6, Yearly: 2}, t0, Options{})
	if len(r.Keep) < 6 || len(r.Keep) > 12 {
		t.Fatalf("unexpected keep count %d", len(r.Keep))
	}
}

func randPolicy(r *rand.Rand) api.RetentionPolicy {
	return api.RetentionPolicy{Last: r.Intn(4), Hourly: r.Intn(4), Daily: r.Intn(5), Weekly: r.Intn(4), Monthly: r.Intn(4), Yearly: r.Intn(3)}
}

func randSnaps(r *rand.Rand) []Snapshot {
	n := 1 + r.Intn(60)
	var s []Snapshot
	for i := 0; i < n; i++ {
		s = append(s, Snapshot{Name: fmt.Sprintf("n%d", i), Time: t0.Add(-time.Duration(r.Intn(60*24*90)) * time.Minute),
			Held: r.Intn(15) == 0, Full: r.Intn(20) == 0, LastCommon: r.Intn(25) == 0})
	}
	return s
}

func TestProperties(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	for it := 0; it < 3000; it++ {
		s := randSnaps(r)
		p := randPolicy(r)
		opt := Options{SafetyWindow: time.Duration(r.Intn(3)) * time.Hour}
		res := Evaluate(s, p, t0, opt)
		if len(res.Keep)+len(res.Delete) != len(s) {
			t.Fatal("partition")
		}
		// never deletes newest
		newest := s[0]
		for _, x := range s {
			if x.Time.After(newest.Time) || (x.Time.Equal(newest.Time) && x.Name > newest.Name) {
				newest = x
			}
		}
		if names(res.Delete)[newest.Name] {
			t.Fatalf("newest deleted: %+v %+v", p, s)
		}
		// protections never deleted
		for _, d := range res.Delete {
			if d.Snapshot.Held || d.Snapshot.LastCommon {
				t.Fatalf("protected deleted %+v", d)
			}
		}
		// idempotent
		var kept []Snapshot
		for _, d := range res.Keep {
			kept = append(kept, d.Snapshot)
		}
		res2 := Evaluate(kept, p, t0, opt)
		if len(res2.Delete) != 0 {
			t.Fatalf("not idempotent: %+v deletes %v", p, res2.Delete)
		}
		// monotone: raising any count never deletes a kept snapshot
		q := p
		q.Daily += r.Intn(3)
		q.Last += r.Intn(3)
		q.Monthly += r.Intn(3)
		res3 := Evaluate(s, q, t0, opt)
		k3 := names(res3.Keep)
		for _, d := range res.Keep {
			if !k3[d.Snapshot.Name] {
				t.Fatalf("not monotone: %+v -> %+v lost %s", p, q, d.Snapshot.Name)
			}
		}
		// order independence
		r.Shuffle(len(s), func(i, j int) { s[i], s[j] = s[j], s[i] })
		res4 := Evaluate(s, p, t0, opt)
		if len(res4.Keep) != len(res.Keep) {
			t.Fatal("order dependent")
		}
	}
}
