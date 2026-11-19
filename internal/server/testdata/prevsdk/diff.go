package sdk

import (
	"context"
	"net/url"
	"strconv"

	"jailops.net/jailops/sdk/api"
)

// DiffOptions narrow a jail diff.
type DiffOptions struct {
	// Limit is how many entries of each dataset come back (0: api.DefaultDiffLimit).
	Limit int
	// Dataset names one dataset (root, etc, local, var-log, ...); empty is all of them.
	Dataset string
	// Packages adds the packages installed on purpose.
	Packages bool
}

// Diff lists what a thin jail holds on top of its image (GET /api/v1/jails/{name}/diff, extension jail_diff).
func (j *JailClient) Diff(ctx context.Context, name string, o DiffOptions) (api.JailDiff, error) {
	if err := j.c.need("jail_diff"); err != nil {
		return api.JailDiff{}, err
	}
	q := url.Values{}
	if o.Limit > 0 {
		q.Set("limit", strconv.Itoa(o.Limit))
	}
	if o.Dataset != "" {
		q.Set("dataset", o.Dataset)
	}
	if o.Packages {
		q.Set("packages", "true")
	}
	v, _, err := get[api.JailDiff](ctx, j.c, j.r.item(name)+"/diff", q)
	return v, err
}
