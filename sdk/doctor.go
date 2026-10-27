package sdk

import (
	"context"
	"net/url"
	"strconv"

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

// Audit returns the newest audit entries (GET /api/v1/audit?limit=).
func (c *Client) Audit(ctx context.Context, limit int) ([]api.AuditEntry, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	r, _, err := get[[]api.AuditEntry](ctx, c, "/api/v1/audit", q)
	return r, err
}
