package service

import (
	"context"

	"jailops.net/jailops/internal/apps"
	"jailops.net/jailops/sdk/api"
)

// appsDir is the catalog directory: the configured one, else the default.
func (s *Service) appsDir() string {
	if s.d.AppsDir != "" {
		return s.d.AppsDir
	}
	return apps.DefaultDir
}

// ListApps answers GET /api/v1/apps: every definition of the catalog
// directory, valid or not, sorted by name. The directory is read on each
// call; a missing one is an empty catalog. Any authenticated identity may
// read the catalog: it is host-level documentation, not an object of a
// project.
func (s *Service) ListApps(context.Context) ([]api.AppSummary, error) {
	es, err := apps.Load(s.appsDir())
	if err != nil {
		return nil, api.Errorf(api.CodeInternal, "read the application catalog: %v", err).
			WithHint("check apps.catalog_dir in jailopsd.yaml")
	}
	out := make([]api.AppSummary, 0, len(es))
	for _, e := range es {
		out = append(out, e.Summary())
	}
	return out, nil
}

// GetApp answers GET /api/v1/apps/<name>: the definition with the hash of
// its file. An invalid definition is answered with its reason, so it can
// never be read as something to install.
func (s *Service) GetApp(_ context.Context, name string) (api.AppDetail, error) {
	e, ok, err := apps.Find(s.appsDir(), name)
	if err != nil {
		return api.AppDetail{}, api.Errorf(api.CodeInternal, "read the application catalog: %v", err).
			WithHint("check apps.catalog_dir in jailopsd.yaml")
	}
	if !ok {
		return api.AppDetail{}, api.Errorf(api.CodeNotFound, "app %q is not in the catalog", name).
			WithHint("jailops app list shows the catalog")
	}
	if e.Err != nil {
		return api.AppDetail{}, api.Errorf(api.CodeInvalid, "app %q: the definition %s is invalid: %v", name, e.File, e.Err).
			WithHint("fix the file in the catalog directory, or reinstall the jailops-apps package").
			WithDetail("file", e.File)
	}
	return e.Detail(), nil
}
