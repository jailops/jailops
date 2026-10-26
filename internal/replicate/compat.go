package replicate

import (
	"context"
	"sort"

	"jailops.net/jailops/sdk/api"
)

// Verdict is the answer of a compatibility check.
type Verdict string

// Verdicts. Only VerdictNo blocks a transfer.
const (
	VerdictVerified Verdict = "VERIFIED"
	VerdictNo       Verdict = "NO"
	VerdictUnknown  Verdict = "UNKNOWN"
)

// CompatRequest describes the transfer about to happen.
type CompatRequest struct {
	Source string
	Chain  string
	Raw    bool
}

// CompatResult is a verdict with reasons.
type CompatResult struct {
	Verdict Verdict
	Reasons []string
}

// CompatChecker checks ZFS feature compatibility before a send (design
// section 3). An implementation compares the feature flags the stream needs
// with the target pool's enabled features.
type CompatChecker interface {
	Check(ctx context.Context, r CompatRequest) (CompatResult, error)
}

// CompareFeatures is the pure core of a checker: needed are the features the
// stream requires; supported is what the target accepts (nil means the target
// is not known). A needed feature outside a known supported set is NO.
func CompareFeatures(needed, supported []string) CompatResult {
	if supported == nil {
		return CompatResult{Verdict: VerdictUnknown, Reasons: []string{"target features unknown"}}
	}
	have := map[string]bool{}
	for _, s := range supported {
		have[s] = true
	}
	var missing []string
	for _, n := range needed {
		if !have[n] {
			missing = append(missing, n)
		}
	}
	if len(missing) == 0 {
		return CompatResult{Verdict: VerdictVerified}
	}
	sort.Strings(missing)
	r := CompatResult{Verdict: VerdictNo}
	for _, m := range missing {
		r.Reasons = append(r.Reasons, "target lacks feature "+m)
	}
	return r
}

// runCompat evaluates a checker. A nil checker or a checker error is UNKNOWN,
// which does not block; NO returns api.CodeZFSIncompatible.
func runCompat(ctx context.Context, c CompatChecker, r CompatRequest) (CompatResult, error) {
	if c == nil {
		return CompatResult{Verdict: VerdictUnknown, Reasons: []string{"no checker configured"}}, nil
	}
	res, err := c.Check(ctx, r)
	if err != nil {
		return CompatResult{Verdict: VerdictUnknown, Reasons: []string{"check failed: " + err.Error()}}, nil
	}
	switch res.Verdict {
	case VerdictNo:
		e := api.Errorf(api.CodeZFSIncompatible, "target cannot receive this stream: %v", res.Reasons)
		return res, e
	case VerdictVerified:
		return res, nil
	}
	res.Verdict = VerdictUnknown
	return res, nil
}
