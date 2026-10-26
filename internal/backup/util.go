package backup

import (
	"regexp"

	"jailops.net/jailops/sdk/api"
)

var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$`)

// validID reports whether s is safe as a jail name or drill id, both of which
// become part of dataset, directory and network names.
func validID(s string) bool { return idRe.MatchString(s) }

func invalid(format string, a ...any) error { return api.Errorf(api.CodeInvalid, format, a...) }
