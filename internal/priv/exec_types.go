package priv

import "jailops.net/jailops/internal/service"

// The exec interface is the one the service layer defines, so a *Client or a
// *Local is a service.JailExecer and the daemon can use the helper as it is.
//
// ExecOpts configure a session: TTY allocates a pseudo-terminal (Stderr() is
// then nil), Rows and Cols give its size, Env is set inside the jail (through
// env(1) there, never in the helper's environment) and User runs the command
// as that user in the jail.
//
// ExecSession is a running command. Signal accepts INT, TERM, HUP, QUIT, KILL
// and WINCH only. Wait returns the exit code (128+n when killed by signal n).
type (
	ExecOpts    = service.ExecOpts
	ExecSession = service.ExecSession
)

var (
	_ service.JailExecer = (*Client)(nil)
	_ service.JailExecer = (*Local)(nil)
)
