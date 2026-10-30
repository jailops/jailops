//go:build unix

package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// Exec runs commands with os/exec. It never uses a shell. Each command gets
// its own process group and the whole group is killed on timeout or
// cancellation, so helpers spawned by the command do not outlive it.
type Exec struct{}

// NewExec returns the production Runner.
func NewExec() *Exec { return &Exec{} }

var _ Runner = (*Exec)(nil)

// limitedBuffer keeps the first max bytes and counts that more was dropped.
// It always reports full writes so the child never blocks or gets SIGPIPE.
type limitedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	l.mu.Lock()
	defer l.mu.Unlock()
	room := l.max - l.buf.Len()
	if room < 0 {
		room = 0
	}
	if len(p) > room {
		l.truncated = true
		p = p[:room]
	}
	l.buf.Write(p)
	return n, nil
}

// Run implements Runner.
func (Exec) Run(ctx context.Context, c Cmd) (Result, error) {
	if c.Name == "" {
		return Result{}, errors.New("run: empty command name")
	}
	timeout := c.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = c.Env
	if cmd.Env == nil {
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LANG=C", "LC_ALL=C"}
	}
	cmd.Stdin = c.Stdin
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Negative pid addresses the process group created by Setpgid.
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		return nil
	}
	cmd.WaitDelay = 2 * time.Second

	maxOut := c.MaxOutput
	if maxOut <= 0 {
		maxOut = DefaultMaxStdout
	}
	stdout := &limitedBuffer{max: maxOut}
	stderr := &limitedBuffer{max: DefaultMaxStderr}
	if c.Stdout != nil {
		cmd.Stdout = c.Stdout
	} else {
		cmd.Stdout = stdout
	}
	cmd.Stderr = stderr

	start := time.Now()
	err := cmd.Run()
	res := Result{
		Duration:  time.Since(start),
		Truncated: stdout.truncated || stderr.truncated,
	}
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
		// The command succeeded, but a process it left behind (a daemon it
		// started) still holds its output open. The pipes were closed
		// WaitDelay after the exit; what was written later is lost.
		err, res.Truncated = nil, true
	}
	if c.Stdout == nil {
		res.Stdout = stdout.buf.Bytes()
	}
	res.Stderr = stderr.buf.Bytes()
	if err == nil {
		return res, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return res, fmt.Errorf("%s: %w after %s", Redact(c), ErrTimeout, timeout)
		}
		return res, fmt.Errorf("%s: %w", Redact(c), ctxErr)
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.ExitCode = ee.ExitCode()
		return res, &ExitError{Name: c.Name, Args: c.Args, Code: res.ExitCode, Stderr: string(res.Stderr)}
	}
	res.ExitCode = -1
	return res, fmt.Errorf("%s: %w", Redact(c), err)
}
