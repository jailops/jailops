package sdk

import (
	"context"
	"net/http"

	"jailops.net/jailops/sdk/api"
)

// Access returns who may connect to an installed app (extension
// "app_access"): its grants, the firewall rules and pg_hba.conf lines they
// generated, and what of that is missing on the host. jail is the jail the app
// was installed as.
func (a *AppsClient) Access(ctx context.Context, jail string) (api.AppAccess, error) {
	if err := a.c.need("app_access"); err != nil {
		return api.AppAccess{}, err
	}
	v, _, err := get[api.AppAccess](ctx, a.c, "/api/v1/jails/"+esc(jail)+"/access", nil)
	if v.Grants == nil && err == nil {
		v.Grants = []api.AppAccessGrant{}
	}
	return v, err
}

// Allow lets a source reach an installed app: a firewall rule in the app's
// managed policy and, for an app with an access block (postgresql), a role, a
// database and a pg_hba.conf line. The connection string of a new role is in
// this answer only; nothing keeps it. Allowing again changes nothing unless
// the request differs or asks for Rotate.
func (a *AppsClient) Allow(ctx context.Context, jail string, req api.AppAllow) (api.AppAllowResult, error) {
	if err := a.c.need("app_access"); err != nil {
		return api.AppAllowResult{}, err
	}
	v, _, err := send[api.AppAllowResult](ctx, a.c, request{method: http.MethodPost, path: "/api/v1/jails/" + esc(jail) + "/access", body: req})
	return v, err
}

// Deny takes a grant back: the firewall rule and the pg_hba.conf line go, and
// a role JailOps made loses its login (DropRole drops it). Denying a source
// that has no grant changes nothing.
func (a *AppsClient) Deny(ctx context.Context, jail string, req api.AppDeny) (api.AppDenyResult, error) {
	if err := a.c.need("app_access"); err != nil {
		return api.AppDenyResult{}, err
	}
	v, _, err := send[api.AppDenyResult](ctx, a.c, request{method: http.MethodPost, path: "/api/v1/jails/" + esc(jail) + "/access/deny", body: req})
	return v, err
}
