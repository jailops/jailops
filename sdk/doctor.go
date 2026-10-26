package sdk

import (
	"context"
	"net/url"
	"strconv"

	"jailops.net/jailops/sdk/api"
)

// Doctor runs the server's diagnostics (GET /1.0/doctor).
func (c *Client) Doctor(ctx context.Context) (api.DoctorReport, error) {
	if err := c.need("doctor"); err != nil {
		return api.DoctorReport{}, err
	}
	r, _, err := get[api.DoctorReport](ctx, c, "/1.0/doctor", nil)
	return r, err
}

// Audit returns the newest audit entries (GET /1.0/audit?limit=).
func (c *Client) Audit(ctx context.Context, limit int) ([]api.AuditEntry, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	r, _, err := get[[]api.AuditEntry](ctx, c, "/1.0/audit", q)
	return r, err
}
