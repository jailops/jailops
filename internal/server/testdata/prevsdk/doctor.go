package sdk

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"jailops.net/jailops/sdk/api"
)

// Doctor runs the server's diagnostics (GET /api/v1/doctor).
func (c *Client) Doctor(ctx context.Context) (api.DoctorReport, error) {
	if err := c.need("doctor"); err != nil {
		return api.DoctorReport{}, err
	}
	r, _, err := get[api.DoctorReport](ctx, c, "/api/v1/doctor", nil)
	return r, err
}

// DoctorFix performs the safe cleanups the diagnostics report (POST /api/v1/doctor/fix): stale mounts below the root of
// stopped jails and what an earlier network layout left. With dryRun it only lists them.
func (c *Client) DoctorFix(ctx context.Context, dryRun bool) (api.DoctorFixResult, error) {
	if err := c.need("doctor"); err != nil {
		return api.DoctorFixResult{}, err
	}
	r, _, err := send[api.DoctorFixResult](ctx, c, request{method: http.MethodPost, path: "/api/v1/doctor/fix", body: api.DoctorFixRequest{DryRun: dryRun}})
	return r, err
}

// Audit returns the newest audit entries, newest first (GET /api/v1/audit?limit=).
func (c *Client) Audit(ctx context.Context, limit int) ([]api.AuditEntry, error) {
	r, _, err := c.AuditPage(ctx, AuditFilter{Limit: limit})
	return r, err
}

// AuditFilter narrows the audit entries the server returns. Zero fields match everything.
type AuditFilter struct {
	// Limit is the number of entries, at most (the server's default is 200, its maximum 1000).
	Limit int
	// Actor keeps the entries of one requestor: a user, a token name or a certificate fingerprint.
	Actor string
	// Since keeps the entries from this time on.
	Since time.Time
	// Cursor continues after a page: the next cursor that page returned.
	Cursor string
}

// AuditPage returns one page of the audit entries that match f, newest first, and the cursor of the next page ("" when
// there is none): GET /api/v1/audit?limit=&actor=&since=&cursor=, the cursor from the Link header (rel="next").
func (c *Client) AuditPage(ctx context.Context, f AuditFilter) ([]api.AuditEntry, string, error) {
	q := url.Values{}
	if f.Limit > 0 {
		q.Set("limit", strconv.Itoa(f.Limit))
	}
	if f.Actor != "" {
		q.Set("actor", f.Actor)
	}
	if !f.Since.IsZero() {
		q.Set("since", f.Since.UTC().Format(time.RFC3339))
	}
	if f.Cursor != "" {
		q.Set("cursor", f.Cursor)
	}
	r, hdr, err := get[[]api.AuditEntry](ctx, c, "/api/v1/audit", q)
	return r, nextCursor(hdr), err
}

// nextCursor reads the cursor of a Link header with rel="next"; "" when there is none.
func nextCursor(h http.Header) string {
	for _, l := range h.Values("Link") {
		target, rel, ok := strings.Cut(l, ">;")
		if !ok || !strings.Contains(rel, `rel="next"`) {
			continue
		}
		if u, err := url.Parse(strings.TrimPrefix(strings.TrimSpace(target), "<")); err == nil {
			return u.Query().Get("cursor")
		}
	}
	return ""
}

// NDPStatus returns the state of the neighbor discovery proxy of every uplink (GET /api/v1/ndp).
func (c *Client) NDPStatus(ctx context.Context) (api.NDPStatus, error) {
	r, _, err := get[api.NDPStatus](ctx, c, "/api/v1/ndp", nil)
	return r, err
}

// NDPEvents returns the recent events of the proxies with a sequence number above after (GET /api/v1/ndp/events).
func (c *Client) NDPEvents(ctx context.Context, after uint64) (api.NDPEvents, error) {
	r, _, err := get[api.NDPEvents](ctx, c, "/api/v1/ndp/events", url.Values{"after": {strconv.FormatUint(after, 10)}})
	return r, err
}
