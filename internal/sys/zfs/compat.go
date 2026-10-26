package zfs

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// Verdict is the outcome of a feature compatibility check.
type Verdict string

const (
	// Verified: every active feature is allowed by the compatibility file.
	Verified Verdict = "VERIFIED"
	// No: at least one active feature is not in the compatibility file.
	No Verdict = "NO"
	// Unknown: there is no usable compatibility file to compare with.
	Unknown Verdict = "UNKNOWN"
)

// Blocks reports whether the verdict must stop an operation. Only NO blocks.
func (v Verdict) Blocks() bool { return v == No }

// CompatResult is the detailed outcome of CheckCompatDetail.
type CompatResult struct {
	Verdict Verdict
	// Blocking lists the active features missing from the file (for NO).
	Blocking []string
}

// ParseCompat returns the feature names listed in a compatibility file
// body: names separated by commas and/or newlines, '#' starts a comment.
func ParseCompat(content string) map[string]bool {
	set := map[string]bool{}
	for _, line := range strings.Split(content, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		for _, f := range strings.Split(line, ",") {
			if f = strings.TrimSpace(f); f != "" {
				set[f] = true
			}
		}
	}
	return set
}

// CheckCompat compares the active feature flags of a pool with the host's
// compatibility file for the target release line. Only active features are
// considered: a feature that is merely enabled never blocks, because it
// changes no on-disk format until used. An empty compatFileContent (file
// absent or unreadable) gives UNKNOWN, never a block.
func CheckCompat(activeFeatures []string, compatFileContent string) Verdict {
	return CheckCompatDetail(activeFeatures, compatFileContent).Verdict
}

// CheckCompatDetail is CheckCompat with the list of blocking features.
func CheckCompatDetail(activeFeatures []string, compatFileContent string) CompatResult {
	if strings.TrimSpace(compatFileContent) == "" {
		return CompatResult{Verdict: Unknown}
	}
	allowed := ParseCompat(compatFileContent)
	var bad []string
	for _, f := range activeFeatures {
		f = strings.TrimPrefix(strings.TrimSpace(f), "feature@")
		if f != "" && !allowed[f] {
			bad = append(bad, f)
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return CompatResult{Verdict: No, Blocking: bad}
	}
	return CompatResult{Verdict: Verified}
}

// DefaultCompatDir is where OpenZFS installs compatibility files on the host.
// The FreeBSD path; Linux uses /usr/share/zfs/compatibility.d.
const DefaultCompatDir = "/usr/share/zfs/compatibility.d"

// LoadCompat reads the compatibility file called name from dir at runtime
// (files are never vendored). name must be a plain file name; lists of
// several files are not supported and return CodeUnsupported.
func LoadCompat(dir, name string) (string, error) {
	if strings.Contains(name, ",") {
		return "", api.Errorf(api.CodeUnsupported, "compatibility lists are not supported: %q", name)
	}
	if name == "" || name == "." || name == ".." || name != filepath.Base(name) || strings.HasPrefix(name, "-") || strings.ContainsAny(name, "\x00\\") {
		return "", api.Errorf(api.CodeInvalid, "invalid compatibility name %q", name)
	}
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		if os.IsNotExist(err) {
			return "", api.Errorf(api.CodeNotFound, "compatibility file %q not found in %s", name, dir)
		}
		return "", api.Errorf(api.CodeInternal, "read compatibility file: %v", err)
	}
	return string(b), nil
}
