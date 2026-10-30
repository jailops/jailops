package cli

import (
	"errors"
	"fmt"
	"io"
	"syscall"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

// Exit codes. They are part of the CLI contract and are never renumbered.
const (
	ExitOK          = 0
	ExitFailure     = 1 // transport failure, aborted, cancelled, anything else
	ExitUsage       = 2 // wrong invocation or a request the server found invalid
	ExitAuth        = 3 // unauthorized, forbidden, rate limited
	ExitNotFound    = 4
	ExitConflict    = 5 // already exists, stale, busy, in use
	ExitUnsupported = 6
	ExitInternal    = 7
)

// exitForCode maps an API error code to an exit code.
func exitForCode(c api.Code) int {
	switch c {
	case api.CodeInvalid, api.CodeDataLoss, api.CodeKeyNotEscrowed, api.CodePlaintextDenied:
		return ExitUsage
	case api.CodeUnauthorized, api.CodeForbidden, api.CodeRateLimited, api.CodeNotManaged:
		return ExitAuth
	case api.CodeNotFound:
		return ExitNotFound
	case api.CodeAlreadyExists, api.CodeConflict, api.CodeBusy, api.CodeIPConflict,
		api.CodePoolExhausted, api.CodeImageInUse, api.CodeMergeConflict, api.CodeChainBroken,
		api.CodeQuotaExceeded, api.CodeMaintenance, api.CodeMissingChunks, api.CodeLeaseExpired:
		return ExitConflict
	case api.CodeUnsupported, api.CodeUnverified, api.CodeZFSIncompatible, api.CodeVersionSkew:
		return ExitUnsupported
	case api.CodeInternal, api.CodeNotLeader, api.CodeNoQuorum:
		return ExitInternal
	}
	return ExitInternal // a code this client does not know is a server matter
}

// exitError carries the exit status of a command run in a jail.
type exitError struct{ code int }

func (e *exitError) Error() string { return fmt.Sprintf("the command exited with status %d", e.code) }

// ExitCode returns the process exit code for err.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var xe *exitError
	if errors.As(err, &xe) {
		return xe.code
	}
	var ue *usageError
	if errors.As(err, &ue) {
		return ExitUsage
	}
	var ae *api.Error
	if errors.As(err, &ae) {
		return exitForCode(ae.Code)
	}
	return ExitFailure
}

// printError writes "code: message" and the hint on a second line.
func (a *app) printError(err error) {
	var xe *exitError
	if errors.As(err, &xe) {
		return // the command's own output said what happened
	}
	w := a.env.Stderr
	red := func(s string) string { return s }
	if a.colorOn(w) {
		red = func(s string) string { return "\x1b[31m" + s + "\x1b[0m" }
	}
	var ae *api.Error
	var ue *usageError
	var ab *abortError
	var he *hintError
	switch {
	case errors.As(err, &ae):
		fmt.Fprintf(w, "%s: %s\n", red(string(ae.Code)), ae.Message)
		hint := ae.Hint
		if hint == "" && ae.Code == api.CodeUnauthorized && a.conn != nil && a.conn.URL == "" && a.conn.Token == "" {
			hint = "the local socket knows only root: run this with sudo, or use a token or client certificate (jailops remote add, or JAILOPS_TOKEN)"
		}
		if hint == "" {
			hint = fallbackHint(ae.Code)
		}
		if hint != "" {
			fmt.Fprintf(w, "hint: %s\n", hint)
		}
	case errors.As(err, &ue):
		fmt.Fprintf(w, "%s: %s\n", red("usage"), ue.err)
		hint := ue.hint
		if hint == "" && a.cmdPath != "" {
			hint = fmt.Sprintf("run '%s --help' for usage", a.cmdPath)
		}
		if hint != "" {
			fmt.Fprintf(w, "hint: %s\n", hint)
		}
	case errors.As(err, &he):
		fprintf(w, "%s: %s\n", red("error"), he.err)
		fprintf(w, "hint: %s\n", he.hint)
	case errors.As(err, &ab):
		fmt.Fprintf(w, "%s: %s\n", red("aborted"), ab.msg)
	case errors.Is(err, sdk.ErrCancelled):
		fmt.Fprintf(w, "%s: %s\n", red("cancelled"), err)
	default:
		fmt.Fprintf(w, "%s: %s\n", red("error"), err)
		if h := transportHint(err); h != "" {
			fmt.Fprintf(w, "hint: %s\n", h)
		}
	}
}

func transportHint(err error) string {
	switch {
	case errors.Is(err, syscall.ENOENT):
		return "the socket does not exist: is jailopsd running? Use --remote to reach another host"
	case errors.Is(err, syscall.EACCES):
		return "permission denied on the socket: check the socket and its directory permissions, or reach the daemon with a token or certificate (--remote)"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused: is jailopsd running and listening on that address?"
	}
	return ""
}

func fallbackHint(c api.Code) string {
	if e, ok := explainFor(c); ok && e.Fix != "" {
		return e.Fix + " (jailops explain " + string(c) + ")"
	}
	return ""
}

// errOut writes a line to w, ignoring errors (a closed pipe is not our failure).
func fprintln(w io.Writer, a ...any)          { _, _ = fmt.Fprintln(w, a...) }
func fprintf(w io.Writer, f string, a ...any) { _, _ = fmt.Fprintf(w, f, a...) }
