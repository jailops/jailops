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

// appsLocalDir is the directory of the operator's own definitions.
func (s *Service) appsLocalDir() string {
	if s.d.AppsLocalDir != "" {
		return s.d.AppsLocalDir
	}
	return apps.DefaultLocalDir
}

// findApp reads one definition: the local one first, then the packaged one.
func (s *Service) findApp(name string) (apps.Entry, bool, error) {
	e, ok, err := apps.FindAll(s.appsDir(), s.appsLocalDir(), name)
	if err != nil {
		return e, ok, api.Errorf(api.CodeInternal, "read the application catalog: %v", err).
			WithHint("check apps.catalog_dir and apps.local_dir in jailopsd.yaml: the daemon must be able to read both")
	}
	return e, ok, nil
}

// invalidHint says where to fix an invalid definition.
func (s *Service) invalidHint(e apps.Entry) string {
	if e.Source == api.AppSourceLocal {
		return "fix the file in " + s.appsLocalDir() + " (apps.local_dir), or remove it"
	}
	return "fix the file in the catalog directory, or reinstall the jailops-apps package"
}

// ListApps answers GET /api/v1/apps: every definition of the catalog
// directory and of the local directory, valid or not, sorted by name; a
// local one hides a packaged one of the same name. The directories are read
// on each call; a missing one is empty. Any authenticated identity may read
// the catalog: it is host-level documentation, not an object of a project.
func (s *Service) ListApps(context.Context) ([]api.AppSummary, error) {
	es, err := apps.LoadAll(s.appsDir(), s.appsLocalDir())
	if err != nil {
		return nil, api.Errorf(api.CodeInternal, "read the application catalog: %v", err).
			WithHint("check apps.catalog_dir and apps.local_dir in jailopsd.yaml: the daemon must be able to read both")
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
	e, ok, err := s.findApp(name)
	if err != nil {
		return api.AppDetail{}, err
	}
	if !ok {
		return api.AppDetail{}, api.Errorf(api.CodeNotFound, "app %q is not in the catalog", name).
			WithHint("jailops app list shows the catalog")
	}
	if e.Err != nil {
		return api.AppDetail{}, api.Errorf(api.CodeInvalid, "app %q: the definition %s is invalid: %v", name, e.File, e.Err).
			WithHint(s.invalidHint(e)).WithDetail("file", e.File)
	}
	return e.Detail(), nil
}
