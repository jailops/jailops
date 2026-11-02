package zfs

import (
	"context"
	"regexp"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// Feature states as zpool get reports them for feature@<name>.
const (
	FeatureDisabled = "disabled"
	FeatureEnabled  = "enabled"
	FeatureActive   = "active"
)

var featureRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// ValidateFeature checks a feature name without the "feature@" prefix.
func ValidateFeature(name string) error {
	if !featureRE.MatchString(name) {
		return api.Errorf(api.CodeInvalid, "invalid pool feature %q", name)
	}
	return nil
}

// FeatureStater reads the state of one pool feature: disabled, enabled or active ("" when the pool does not know it).
// Command, Fake and the helper's clients offer it; it is not part of Client.
type FeatureStater interface {
	FeatureState(ctx context.Context, pool, feature string) (string, error)
}

// FeatureState implements FeatureStater: zpool get -H -p -o value feature@<feature> <pool>.
func (c *Command) FeatureState(ctx context.Context, pool, feature string) (string, error) {
	if err := ValidatePool(pool); err != nil {
		return "", err
	}
	if err := ValidateFeature(feature); err != nil {
		return "", err
	}
	out, err := c.out(ctx, c.zpool(), "get", "-H", "-p", "-o", "value", "feature@"+feature, pool)
	if err != nil {
		return "", err
	}
	return ParseFeatureState(out), nil
}

// ParseFeatureState reads the value zpool get printed; "-" (a feature the pool does not know) is "".
func ParseFeatureState(out string) string {
	v := strings.TrimSpace(out)
	switch v {
	case FeatureDisabled, FeatureEnabled, FeatureActive:
		return v
	}
	return ""
}

// SetFeatureState sets what FeatureState reports for a feature that is not active (test helper). Without it a
// feature reads as enabled.
func (f *Fake) SetFeatureState(pool, feature, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.states == nil {
		f.states = map[string]string{}
	}
	f.states[pool+"@"+feature] = state
}

// FeatureState implements FeatureStater: active when SetFeatures listed it, else what SetFeatureState set, else
// enabled.
func (f *Fake) FeatureState(_ context.Context, pool, feature string) (string, error) {
	if err := ValidatePool(pool); err != nil {
		return "", err
	}
	if err := ValidateFeature(feature); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ds[pool] == nil {
		return "", api.Errorf(api.CodeNotFound, "no such pool '%s'", pool)
	}
	for _, a := range f.features[pool] {
		if a == feature {
			return FeatureActive, nil
		}
	}
	if s, ok := f.states[pool+"@"+feature]; ok {
		return s, nil
	}
	return FeatureEnabled, nil
}
