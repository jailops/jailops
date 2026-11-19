package jailos

import (
	"errors"

	"jailops.net/jailops/sdk/api"
)

// ForcedStop is the answer to a stop that did not finish within its budget and was forced: the jail is gone, its stop
// commands were cut off. The daemon takes it as a stop with a warning, so it is an internal error with a marker that
// survives the helper protocol, never an error code of the API.
func ForcedStop(msg string) error {
	return api.Errorf(api.CodeInternal, "%s", msg).WithDetail("stop", "forced")
}

// IsForcedStop says whether err is the answer of ForcedStop.
func IsForcedStop(err error) bool {
	var ae *api.Error
	return errors.As(err, &ae) && ae.Detail["stop"] == "forced"
}
