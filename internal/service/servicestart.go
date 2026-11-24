package service

import (
	"fmt"
	"regexp"
)

// serviceStartScript is the fixed shell text of serviceStartArgv. The service
// name is its first positional parameter, never part of the text. The output
// of the rc script goes to a temporary file in the jail: a daemon the script
// starts inherits that file instead of the pipes of the in-jail exec, which
// would otherwise stay open and keep the caller waiting. The file is printed
// when the start fails and removed in any case.
const serviceStartScript = `out=$(/usr/bin/mktemp -t jailops) || exit 1; /usr/sbin/service "$1" start </dev/null >"$out" 2>&1; rc=$?; [ $rc -eq 0 ] || /bin/cat "$out"; /bin/rm -f "$out"; exit $rc`

// serviceStartName is $0 of serviceStartScript, the name the shell reports in its errors.
const serviceStartName = "jailops-start"

var serviceNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// serviceStartArgv is the in-jail command that starts an rc.d service with
// its output detached from the caller (serviceStartScript).
func serviceStartArgv(service string) ([]string, error) {
	if !serviceNameRE.MatchString(service) {
		return nil, fmt.Errorf("%q is not an rc.d service name", service)
	}
	return []string{"/bin/sh", "-c", serviceStartScript, serviceStartName, service}, nil
}
