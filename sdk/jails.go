package sdk

import (
	"context"
	"iter"
	"net/http"
	"net/url"

	"jailops.net/jailops/sdk/api"
)

// NewJail builds a Jail document from a name and a spec.
func NewJail(name string, spec api.JailSpec) api.Jail {
	return api.Jail{Metadata: api.ObjectMeta{Name: name}, Spec: spec}
}

// JailClient accesses /1.0/jails.
type JailClient struct {
	c *Client
	r *Resource[api.Jail]
}

// Jails returns the jails client.
func (c *Client) Jails() *JailClient {
	return &JailClient{c, newResource[api.Jail](c, "/1.0/jails")}
}

// List iterates jails.
func (j *JailClient) List(ctx context.Context, opts ...ListOption) iter.Seq2[api.Jail, error] {
	return j.r.List(ctx, opts...)
}

// Get fetches a jail.
func (j *JailClient) Get(ctx context.Context, name string) (api.Jail, error) {
	return j.r.Get(ctx, name)
}

// GetETag fetches a jail and its ETag.
func (j *JailClient) GetETag(ctx context.Context, name string) (api.Jail, string, error) {
	return j.r.GetETag(ctx, name)
}

// Update replaces a jail (see Resource.Update).
func (j *JailClient) Update(ctx context.Context, name string, v api.Jail, etag string) (api.Jail, error) {
	return j.r.Update(ctx, name, v, etag)
}

// Patch merges a JSON merge patch into a jail.
func (j *JailClient) Patch(ctx context.Context, name string, patch any, etag string) (api.Jail, error) {
	return j.r.Patch(ctx, name, patch, etag)
}

// Create creates a jail; the operation's result is the created jail.
func (j *JailClient) Create(ctx context.Context, jail api.Jail) (*Operation[api.Jail], error) {
	op, _, err := sendOp(ctx, j.c, request{method: http.MethodPost, path: j.r.path, body: jail},
		func(ctx context.Context) (api.Jail, error) { return j.Get(ctx, jail.Metadata.Name) })
	return op, err
}

// Delete deletes a jail.
func (j *JailClient) Delete(ctx context.Context, name string) (*Operation[None], error) {
	op, _, err := sendOp[None](ctx, j.c, request{method: http.MethodDelete, path: j.r.item(name)}, nil)
	return op, err
}

// State returns the observed status of a jail.
func (j *JailClient) State(ctx context.Context, name string) (api.JailStatus, error) {
	st, _, err := get[api.JailStatus](ctx, j.c, j.r.item(name)+"/state", nil)
	return st, err
}

// ActionOption tunes a state action.
type ActionOption func(*api.JailAction)

// ActionTimeout sets the action timeout in seconds.
func ActionTimeout(seconds int) ActionOption { return func(a *api.JailAction) { a.Timeout = seconds } }

// Force forces the action.
func Force() ActionOption { return func(a *api.JailAction) { a.Force = true } }

func (j *JailClient) action(ctx context.Context, name, action string, opts []ActionOption) (*Operation[api.Jail], error) {
	a := api.JailAction{Action: action}
	for _, o := range opts {
		o(&a)
	}
	op, _, err := sendOp(ctx, j.c, request{method: http.MethodPut, path: j.r.item(name) + "/state", body: a},
		func(ctx context.Context) (api.Jail, error) { return j.Get(ctx, name) })
	return op, err
}

// Start starts a jail.
func (j *JailClient) Start(ctx context.Context, name string, opts ...ActionOption) (*Operation[api.Jail], error) {
	return j.action(ctx, name, "start", opts)
}

// Stop stops a jail.
func (j *JailClient) Stop(ctx context.Context, name string, opts ...ActionOption) (*Operation[api.Jail], error) {
	return j.action(ctx, name, "stop", opts)
}

// Restart restarts a jail.
func (j *JailClient) Restart(ctx context.Context, name string, opts ...ActionOption) (*Operation[api.Jail], error) {
	return j.action(ctx, name, "restart", opts)
}

// Rollback rolls a jail back. Scopes data and all need ConfirmDataLoss.
func (j *JailClient) Rollback(ctx context.Context, name string, rb api.JailRollback) (*Operation[api.Jail], error) {
	if err := j.c.need("jail_rollback_scopes"); err != nil {
		return nil, err
	}
	op, _, err := sendOp(ctx, j.c, request{method: http.MethodPost, path: j.r.item(name) + "/rollback", body: rb},
		func(ctx context.Context) (api.Jail, error) { return j.Get(ctx, name) })
	return op, err
}

// Upgrade moves a jail to a newer image generation.
func (j *JailClient) Upgrade(ctx context.Context, name string, up api.JailUpgrade) (*Operation[api.Jail], error) {
	op, _, err := sendOp(ctx, j.c, request{method: http.MethodPost, path: j.r.item(name) + "/upgrade", body: up},
		func(ctx context.Context) (api.Jail, error) { return j.Get(ctx, name) })
	return op, err
}

// SnapshotClient accesses the snapshots of one jail.
type SnapshotClient struct {
	c    *Client
	jail string
	r    *Resource[api.JailSnapshot]
}

// Snapshots returns the snapshot client of a jail.
func (j *JailClient) Snapshots(jail string) *SnapshotClient {
	return &SnapshotClient{j.c, jail, newResource[api.JailSnapshot](j.c, j.r.item(jail)+"/snapshots")}
}

// List iterates snapshots.
func (s *SnapshotClient) List(ctx context.Context, opts ...ListOption) iter.Seq2[api.JailSnapshot, error] {
	if err := s.c.need("jail_snapshots"); err != nil {
		return func(y func(api.JailSnapshot, error) bool) { y(api.JailSnapshot{}, err) }
	}
	return s.r.List(ctx, opts...)
}

// Get fetches a snapshot.
func (s *SnapshotClient) Get(ctx context.Context, name string) (api.JailSnapshot, error) {
	if err := s.c.need("jail_snapshots"); err != nil {
		return api.JailSnapshot{}, err
	}
	return s.r.Get(ctx, name)
}

// Create takes a snapshot of all datasets of the jail. An empty name lets the
// server choose one.
func (s *SnapshotClient) Create(ctx context.Context, name, comment string) (*Operation[api.JailSnapshot], error) {
	if err := s.c.need("jail_snapshots"); err != nil {
		return nil, err
	}
	body := api.JailSnapshot{
		Metadata: api.ObjectMeta{Name: name},
		Spec:     api.JailSnapshotSpec{Jail: s.jail, Comment: comment},
	}
	var fetch func(context.Context) (api.JailSnapshot, error)
	if name != "" {
		fetch = func(ctx context.Context) (api.JailSnapshot, error) { return s.r.Get(ctx, name) }
	}
	op, _, err := sendOp(ctx, s.c, request{method: http.MethodPost, path: s.r.path, body: body}, fetch)
	return op, err
}

// Delete deletes a snapshot.
func (s *SnapshotClient) Delete(ctx context.Context, name string) (*Operation[None], error) {
	if err := s.c.need("jail_snapshots"); err != nil {
		return nil, err
	}
	op, _, err := sendOp[None](ctx, s.c, request{method: http.MethodDelete, path: s.r.item(name)}, nil)
	return op, err
}

// ConfigResult is the result of Config: the jail and the keys that need a
// restart to take effect.
type ConfigResult struct {
	api.Jail
	RestartRequired []string `json:"restart_required"`
	Restarted       bool     `json:"restarted,omitempty"`
}

// Config sets and unsets configuration keys of a jail. The server never
// restarts a jail on its own: RestartRequired names the keys that wait for a
// restart, and restart=true asks for it explicitly.
func (j *JailClient) Config(ctx context.Context, name string, set map[string]string, unset []string, restart bool) (*Operation[ConfigResult], error) {
	q := url.Values{}
	if restart {
		q.Set("restart", "true")
	}
	op, _, err := sendOp[ConfigResult](ctx, j.c, request{method: http.MethodPost, path: j.r.item(name) + "/config", query: q,
		body: api.ConfigChange{Set: set, Unset: unset}}, nil)
	return op, err
}

// Expanded fetches a jail with its profiles merged in and the source of every key.
func (j *JailClient) Expanded(ctx context.Context, name string) (api.JailExpanded, error) {
	out, _, err := get[api.JailExpanded](ctx, j.c, j.r.item(name), url.Values{"expanded": {"true"}})
	return out, err
}
