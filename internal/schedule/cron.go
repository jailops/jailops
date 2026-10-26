// Package schedule parses five-field cron expressions and computes the next
// fire time, with defined behaviour across daylight saving transitions.
//
// DST semantics (wall-clock cron, documented because cron implementations
// differ):
//   - A wall time that does not exist (spring forward gap) fires once, at the
//     first instant after the gap, never zero times and never more than once
//     per expression even if several matching minutes fall inside the gap.
//   - A wall time that occurs twice (fall back overlap) fires once, on its
//     first occurrence. A job with a wildcard hour therefore does not fire a
//     second time during the repeated hour.
package schedule

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Spec is a parsed cron expression.
type Spec struct {
	expr    string
	minute  uint64 // bits 0-59
	hour    uint32 // bits 0-23
	dom     uint32 // bits 1-31
	month   uint16 // bits 1-12
	dow     uint8  // bits 0-6, Sunday=0
	domStar bool
	dowStar bool
}

// String returns the original expression.
func (s *Spec) String() string { return s.expr }

var monthNames = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
var dowNames = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}

type fieldDef struct {
	name     string
	min, max int
	names    map[string]int
	dow      bool
}

var fields = [5]fieldDef{
	{"minute", 0, 59, nil, false},
	{"hour", 0, 23, nil, false},
	{"day-of-month", 1, 31, nil, false},
	{"month", 1, 12, monthNames, false},
	{"day-of-week", 0, 7, dowNames, true},
}

// Parse parses "minute hour dom month dow". Supported syntax per field: "*",
// numbers, names (jan, mon), lists "a,b", ranges "a-b", and steps "*/n",
// "a-b/n", "a/n". Day of week 7 means Sunday. As in classic cron, when both
// day-of-month and day-of-week are restricted a day matches if either does.
// Macros such as @daily are not supported.
func Parse(expr string) (*Spec, error) {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return nil, fmt.Errorf("schedule: %q: want 5 fields, got %d", expr, len(parts))
	}
	var bits [5]uint64
	var star [5]bool
	for i, p := range parts {
		b, st, err := parseField(p, fields[i])
		if err != nil {
			return nil, fmt.Errorf("schedule: %q: %s: %w", expr, fields[i].name, err)
		}
		bits[i], star[i] = b, st
	}
	s := &Spec{expr: strings.Join(parts, " ")}
	s.minute = bits[0]
	s.hour = uint32(bits[1])
	s.dom = uint32(bits[2])
	s.month = uint16(bits[3])
	d := bits[4]
	if d&(1<<7) != 0 {
		d |= 1
	}
	s.dow = uint8(d & 0x7f)
	s.domStar, s.dowStar = star[2], star[4]
	// Reject expressions that can never fire (for example 30 February).
	if _, ok := s.Next(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), time.UTC); !ok {
		return nil, fmt.Errorf("schedule: %q never fires", expr)
	}
	return s, nil
}

func parseField(p string, f fieldDef) (bits uint64, star bool, err error) {
	if p == "" {
		return 0, false, errors.New("empty")
	}
	for _, item := range strings.Split(p, ",") {
		rng, stepS, hasStep := strings.Cut(item, "/")
		step := 1
		if hasStep {
			step, err = strconv.Atoi(stepS)
			if err != nil || step < 1 {
				return 0, false, fmt.Errorf("bad step %q", stepS)
			}
		}
		lo, hi := f.min, f.max
		switch {
		case rng == "*" || rng == "?":
			if !hasStep && len(strings.Split(p, ",")) == 1 {
				star = true
			}
			if f.dow {
				hi = 6
			}
		case strings.Contains(rng, "-"):
			a, b, _ := strings.Cut(rng, "-")
			if lo, err = parseVal(a, f); err != nil {
				return 0, false, err
			}
			if hi, err = parseVal(b, f); err != nil {
				return 0, false, err
			}
			if lo > hi {
				return 0, false, fmt.Errorf("range %q is backwards", rng)
			}
		default:
			if lo, err = parseVal(rng, f); err != nil {
				return 0, false, err
			}
			if hasStep {
				hi = f.max // "a/n" means a-max/n
				if f.dow {
					hi = 6
				}
			} else {
				hi = lo
			}
		}
		for v := lo; v <= hi; v += step {
			bits |= 1 << uint(v)
		}
	}
	return bits, star, nil
}

func parseVal(s string, f fieldDef) (int, error) {
	if f.names != nil {
		if v, ok := f.names[strings.ToLower(s)]; ok {
			return v, nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil || strings.HasPrefix(s, "+") || strings.HasPrefix(s, "-") {
		return 0, fmt.Errorf("bad value %q", s)
	}
	if v < f.min || v > f.max {
		return 0, fmt.Errorf("value %d out of range %d-%d", v, f.min, f.max)
	}
	return v, nil
}

func (s *Spec) dayMatches(t time.Time) bool {
	domOK := s.dom&(1<<uint(t.Day())) != 0
	dowOK := s.dow&(1<<uint(t.Weekday())) != 0
	switch {
	case s.domStar && s.dowStar:
		return true
	case s.domStar:
		return dowOK
	case s.dowStar:
		return domOK
	}
	return domOK || dowOK
}

// nextWall returns the first matching wall-clock minute strictly after w. The
// time is "naive": it is carried in UTC and its fields are the wall fields.
func (s *Spec) nextWall(w time.Time, limit time.Time) (time.Time, bool) {
	t := w.Truncate(time.Minute).Add(time.Minute)
	for t.Before(limit) {
		if s.month&(1<<uint(t.Month())) == 0 {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC)
			continue
		}
		if !s.dayMatches(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC)
			continue
		}
		if s.hour&(1<<uint(t.Hour())) == 0 {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, time.UTC)
			continue
		}
		if s.minute&(1<<uint(t.Minute())) == 0 {
			t = t.Add(time.Minute)
			continue
		}
		return t, true
	}
	return time.Time{}, false
}

func naive(t time.Time) time.Time {
	y, mo, d := t.Date()
	return time.Date(y, mo, d, t.Hour(), t.Minute(), 0, 0, time.UTC)
}

// resolve maps a wall minute to an instant in loc following the DST rules in
// the package comment. ok is false when no instant is both valid and after
// `after` (the wall time was already fired, or is a repeat).
func resolve(w time.Time, loc *time.Location, after time.Time) (time.Time, bool) {
	// Candidate offsets around w: those in effect a day before and a day after.
	guess := time.Date(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), 0, 0, loc)
	var offs []int
	for _, probe := range []time.Time{guess.Add(-26 * time.Hour), guess, guess.Add(26 * time.Hour)} {
		_, o := probe.In(loc).Zone()
		dup := false
		for _, e := range offs {
			dup = dup || e == o
		}
		if !dup {
			offs = append(offs, o)
		}
	}
	var valid []time.Time
	for _, o := range offs {
		inst := w.Add(-time.Duration(o) * time.Second).In(loc)
		if naive(inst).Equal(w) {
			valid = append(valid, inst)
		}
	}
	if len(valid) == 0 { // gap: fire at the first instant after the gap
		lo := guess.Add(-26 * time.Hour)
		hi := guess.Add(26 * time.Hour)
		_, loOff := lo.Zone()
		for hi.Sub(lo) > time.Second {
			mid := lo.Add(hi.Sub(lo) / 2)
			if _, o := mid.Zone(); o == loOff {
				lo = mid
			} else {
				hi = mid
			}
		}
		t := hi.Truncate(time.Second)
		if _, o := t.Zone(); o == loOff { // Truncate drift guard
			t = t.Add(time.Second)
		}
		return t, t.After(after)
	}
	first := valid[0]
	for _, v := range valid[1:] {
		if v.Before(first) {
			first = v
		}
	}
	return first, first.After(after)
}

// Next returns the first fire time strictly after `after` in loc, and false if
// there is none within eight years. A nil loc means UTC.
func (s *Spec) Next(after time.Time, loc *time.Location) (time.Time, bool) {
	if loc == nil {
		loc = time.UTC
	}
	after = after.In(loc)
	w := naive(after)
	limit := w.AddDate(8, 0, 0)
	for {
		nw, ok := s.nextWall(w, limit)
		if !ok {
			return time.Time{}, false
		}
		if t, ok := resolve(nw, loc, after); ok {
			return t, true
		}
		w = nw
	}
}
