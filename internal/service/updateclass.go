package service

import (
	"errors"

	"jailops.net/jailops/internal/plan"
	"jailops.net/jailops/sdk/api"
)

// upgradeOutcome classifies the failure of an OS upgrade for the update history. The outcomes mean different things to
// the person reading the history:
//
//   - preflight_failed: nothing was changed. The preflight refused, the plan could not be built, or the /etc merge needs a
//     human and the plan undid itself. The jail is on its old root.
//   - rolled_back: a step failed after the plan began and every undo step succeeded. The jail is back on its old root.
//   - recovery_needed: a step failed and an undo step failed too. The jail may be between the two roots; the journalled plan
//     is replayed when the daemon starts.
//
// A success is not an error and is not classified here. rolledBack says that the root was swapped back.
func upgradeOutcome(err error, from string) (result api.UpdateResult, rolledBack bool, note string) {
	var pe *plan.Error
	switch {
	case errors.As(err, &pe) && len(pe.UndoErrs) > 0:
		return api.UpdateRecoveryNeeded, false,
			"the undo did not finish, so the jail may be between " + from + " and the new root: the interrupted plan is replayed when the daemon starts (service jailopsd restart); check `jailops info` afterwards"
	case api.IsCode(err, api.CodePreconditionFailed):
		return api.UpdatePreflightFailed, false, "the /etc merge needs a human; the jail stays on " + from
	case errors.As(err, &pe):
		return api.UpdateRolledBack, true, "the jail was swapped back to " + from + "; application data was not touched"
	}
	return api.UpdatePreflightFailed, false, "nothing was changed: the upgrade did not start (" + from + " is still the root)"
}
