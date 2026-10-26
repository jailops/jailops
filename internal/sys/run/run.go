// Package run is the only way JailOps starts external commands. Commands are
// argv vectors, never shell strings, always bounded by a timeout, and tests
// replace the Runner with a Fake.
package run

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// DefaultTimeout applies when Cmd.Timeout is zero.
const DefaultTimeout = 60 * time.Second

// NoTimeout, as Cmd.Timeout, leaves only the context to bound the command.
// Use it for long streams such as zfs send.
const NoTimeout time.Duration = -1

// Default capture limits per stream when Cmd.MaxOutput is zero.
const (
	DefaultMaxStdout = 8 << 20
	DefaultMaxStderr = 1 << 20
)

// Cmd describes one command. Name is looked up in PATH by the Exec runner.
type Cmd struct {
	Name string
	Args []string
	// Stdin is streamed to the process without buffering. Nil means /dev/null.
	Stdin io.Reader
	// Stdout, when non-nil, receives the process output as a stream and
	// Result.Stdout stays empty. Nothing is buffered beyond the pipe.
	Stdout io.Writer
	// Env is the full environment. Nil means a minimal inherited one
	// (PATH, LANG=C, LC_ALL=C).
	Env []string
	Dir string
	// Timeout of zero means DefaultTimeout, NoTimeout means none.
	Timeout time.Duration
	// MaxOutput bounds captured stdout (and stderr is bounded by
	// DefaultMaxStderr). Zero means the defaults. Excess output is dropped
	// and Result.Truncated is set; the process is not killed.
	MaxOutput int
}

// Result is what a finished command left behind.
type Result struct {
	Stdout    []byte
	Stderr    []byte
	ExitCode  int
	Truncated bool
	Duration  time.Duration
}

// Runner runs commands. A non-zero exit status is returned as *ExitError
// together with the Result.
type Runner interface {
	Run(ctx context.Context, c Cmd) (Result, error)
}

// ExitError reports a command that ran and exited non-zero (or was killed
// by a signal, then Code is -1).
type ExitError struct {
	Name   string
	Args   []string
	Code   int
	Stderr string
}

func (e *ExitError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if len(msg) > 512 {
		msg = msg[:512] + "..."
	}
	return fmt.Sprintf("%s exited with status %d: %s", Redact(Cmd{Name: e.Name, Args: e.Args}), e.Code, msg)
}

// ErrTimeout is wrapped by errors for commands that hit their timeout.
var ErrTimeout = errors.New("command timed out")

// IsExit reports whether err is an *ExitError and returns it.
func IsExit(err error) (*ExitError, bool) {
	var e *ExitError
	ok := errors.As(err, &e)
	return e, ok
}

// Stdout is a convenience: run c and return stdout as a string.
func Stdout(ctx context.Context, r Runner, c Cmd) (string, error) {
	res, err := r.Run(ctx, c)
	return string(res.Stdout), err
}
