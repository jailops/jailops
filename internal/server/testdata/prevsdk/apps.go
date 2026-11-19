package sdk

import (
	"context"
	"net/http"

	"jailops.net/jailops/sdk/api"
)

// AppsClient reads the application catalog (/api/v1/apps): definitions of
// jails that run packages, installed on the host as local files. It needs
// the "apps" extension.
type AppsClient struct{ c *Client }

// Apps returns the catalog client.
func (c *Client) Apps() *AppsClient { return &AppsClient{c} }

// List returns every definition of the catalog, valid or not, sorted by name.
func (a *AppsClient) List(ctx context.Context) ([]api.AppSummary, error) {
	if err := a.c.need("apps"); err != nil {
		return nil, err
	}
	v, _, err := get[[]api.AppSummary](ctx, a.c, "/api/v1/apps", nil)
	return v, err
}

// Get returns one definition with the hash of its file. An invalid
// definition is an "invalid" error that says why.
func (a *AppsClient) Get(ctx context.Context, name string) (api.AppDetail, error) {
	if err := a.c.need("apps"); err != nil {
		return api.AppDetail{}, err
	}
	v, _, err := get[api.AppDetail](ctx, a.c, "/api/v1/apps/"+esc(name), nil)
	return v, err
}

// Install creates a jail that runs an app of the catalog, in the client's
// project, as one operation whose result says how to reach it (extension
// "app_install"). With DryRun the result lists the steps and nothing is
// created. A failed installation after the jail's first start leaves the
// jail stopped for inspection; the error's hint says so.
func (a *AppsClient) Install(ctx context.Context, name string, req api.AppInstall) (*Operation[api.AppInstallResult], error) {
	if err := a.c.need("app_install"); err != nil {
		return nil, err
	}
	op, _, err := sendOp[api.AppInstallResult](ctx, a.c, request{method: http.MethodPost, path: "/api/v1/apps/" + esc(name) + "/install", body: req}, nil)
	return op, err
}

// Installed lists the jails of the client's project that an app
// installation created: those carrying the label api.LabelApp. The server
// filters labels by key and value, so the list is read whole and narrowed
// here.
func (a *AppsClient) Installed(ctx context.Context) ([]api.Jail, error) {
	var out []api.Jail
	for j, err := range a.c.Jails().List(ctx) {
		if err != nil {
			return nil, err
		}
		if j.Metadata.Labels[api.LabelApp] != "" {
			out = append(out, j)
		}
	}
	return out, nil
}
