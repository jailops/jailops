package node

import (
	"errors"
	"fmt"

	"jailops.net/jailops/internal/repo/datastore"
	"jailops.net/jailops/sdk/api"
)

// Error is a node error with the API code it maps to.
type Error = api.Error

func errf(code api.Code, format string, args ...any) *api.Error {
	return api.Errorf(code, format, args...)
}

// ErrProtected is returned when a protected snapshot would be removed.
var ErrProtected = errors.New("node: the snapshot is protected")

// asAPIError maps any error from the repository or the control plane to an
// API error. Maintenance modes become CodeMaintenance (HTTP 503), not a fault.
func asAPIError(err error) *api.Error {
	if err == nil {
		return nil
	}
	var ae *api.Error
	switch {
	case errors.As(err, &ae):
		return ae
	case errors.Is(err, datastore.ErrReadOnly):
		return errf(api.CodeMaintenance, "the repository is in read-only maintenance mode").WithHint("retry later")
	case errors.Is(err, datastore.ErrOffline):
		return errf(api.CodeMaintenance, "the repository is offline for maintenance").WithHint("retry later")
	case errors.Is(err, datastore.ErrNotFound):
		return errf(api.CodeNotFound, "not found")
	case errors.Is(err, ErrProtected):
		return errf(api.CodeBusy, "the snapshot is protected")
	}
	return errf(api.CodeInternal, "%v", err)
}

func wrap(what string, err error) error {
	if err == nil {
		return nil
	}
	if ae := (*api.Error)(nil); errors.As(err, &ae) {
		return err
	}
	if errors.Is(err, datastore.ErrReadOnly) || errors.Is(err, datastore.ErrOffline) {
		return err
	}
	return fmt.Errorf("%s: %w", what, err)
}
