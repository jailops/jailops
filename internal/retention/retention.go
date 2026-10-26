// Package retention evaluates grandfather-father-son retention as a pure
// function. It decides; it never deletes. The backup node applies the result
// and is the only place backups are deleted.
package retention

import (
	"sort"
	"time"

	"jailops.net/jailops/sdk/api"
)

// Snapshot is one backup snapshot as the retention engine sees it.
type Snapshot struct {
	Name string
	Time time.Time
	// Held is true when a hold that JailOps does not manage protects it
	// (an operator hold, a running drill).
	Held bool
	// LastCommon marks the last common snapshot of a chain: the base the next
	// incremental needs.
	LastCommon bool
	// Full marks a snapshot received as a full send (the root of a chain).
	Full bool
	// HasClones is true when a clone depends on the snapshot.
	HasClones bool
}

// Options tune an evaluation.
type Options struct {
	// SafetyWindow protects every snapshot newer than now-SafetyWindow.
	SafetyWindow time.Duration
	// Loc defines hour, day, week, month and year boundaries; nil means UTC.
	Loc *time.Location
}

// Reasons recorded per snapshot.
const (
	ReasonLast       = "last"
	ReasonHourly     = "hourly"
	ReasonDaily      = "daily"
	ReasonWeekly     = "weekly"
	ReasonMonthly    = "monthly"
	ReasonYearly     = "yearly"
	ReasonNewest     = "protected:newest"
	ReasonHeld       = "protected:held"
	ReasonLastCommon = "protected:last-common"
	ReasonSafety     = "protected:safety-window"
	ReasonOnlyFull   = "protected:only-full"
	ReasonClones     = "protected:clones"
	ReasonNoPolicy   = "no-policy"
	ReasonExpired    = "expired"
)

// Decision is the verdict on one snapshot.
type Decision struct {
	Snapshot Snapshot
	Reasons  []string
}

// Result splits snapshots into keep and delete, each newest first.
type Result struct {
	Keep   []Decision
	Delete []Decision
}

// IsZero reports whether the policy keeps nothing by rule.
func isZero(p api.RetentionPolicy) bool { return p == api.RetentionPolicy{} }

// StructuralProtections lists reasons s must not be deleted regardless of
// age, by anyone, given the whole set. It does not include the safety window.
func StructuralProtections(s Snapshot, all []Snapshot) []string {
	var r []string
	if s.Held {
		r = append(r, ReasonHeld)
	}
	if s.LastCommon {
		r = append(r, ReasonLastCommon)
	}
	if s.HasClones {
		r = append(r, ReasonClones)
	}
	if s.Full && len(all) > 1 {
		fulls := 0
		for _, o := range all {
			if o.Full {
				fulls++
			}
		}
		if fulls == 1 {
			r = append(r, ReasonOnlyFull)
		}
	}
	return r
}

type bucketFn func(time.Time) [3]int

func hourKey(t time.Time) [3]int  { y, d := t.Year(), t.YearDay(); return [3]int{y, d, t.Hour()} }
func dayKey(t time.Time) [3]int   { return [3]int{t.Year(), t.YearDay(), 0} }
func weekKey(t time.Time) [3]int  { y, w := t.ISOWeek(); return [3]int{y, w, 0} }
func monthKey(t time.Time) [3]int { return [3]int{t.Year(), int(t.Month()), 0} }
func yearKey(t time.Time) [3]int  { return [3]int{t.Year(), 0, 0} }

// Evaluate applies the policy. Snapshots may be in any order. An all-zero
// policy keeps everything. The newest snapshot is never deleted. For each
// period the newest snapshot of each of the most recent N periods that have
// any snapshot is kept, so raising a count never deletes more (monotone) and
// evaluating the keep set again changes nothing (idempotent).
func Evaluate(snaps []Snapshot, p api.RetentionPolicy, now time.Time, opt Options) Result {
	loc := opt.Loc
	if loc == nil {
		loc = time.UTC
	}
	sorted := append([]Snapshot(nil), snaps...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if !sorted[i].Time.Equal(sorted[j].Time) {
			return sorted[i].Time.After(sorted[j].Time)
		}
		return sorted[i].Name > sorted[j].Name
	})
	reasons := make([][]string, len(sorted))
	add := func(i int, r string) {
		for _, e := range reasons[i] {
			if e == r {
				return
			}
		}
		reasons[i] = append(reasons[i], r)
	}
	if isZero(p) {
		for i := range sorted {
			add(i, ReasonNoPolicy)
		}
	}
	for i := 0; i < p.Last && i < len(sorted); i++ {
		add(i, ReasonLast)
	}
	period := func(n int, fn bucketFn, reason string) {
		if n <= 0 {
			return
		}
		seen := 0
		var last [3]int
		for i, s := range sorted { // newest first: first of a bucket is its newest
			k := fn(s.Time.In(loc))
			if seen > 0 && k == last {
				continue
			}
			if seen >= n {
				break
			}
			seen++
			last = k
			add(i, reason)
		}
	}
	period(p.Hourly, hourKey, ReasonHourly)
	period(p.Daily, dayKey, ReasonDaily)
	period(p.Weekly, weekKey, ReasonWeekly)
	period(p.Monthly, monthKey, ReasonMonthly)
	period(p.Yearly, yearKey, ReasonYearly)

	for i, s := range sorted {
		if i == 0 {
			add(i, ReasonNewest)
		}
		for _, r := range StructuralProtections(s, sorted) {
			add(i, r)
		}
		if opt.SafetyWindow > 0 && s.Time.After(now.Add(-opt.SafetyWindow)) {
			add(i, ReasonSafety)
		}
	}
	var res Result
	for i, s := range sorted {
		if len(reasons[i]) == 0 {
			res.Delete = append(res.Delete, Decision{s, []string{ReasonExpired}})
		} else {
			res.Keep = append(res.Keep, Decision{s, reasons[i]})
		}
	}
	return res
}
