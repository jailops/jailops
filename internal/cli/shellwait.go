package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"jailops.net/jailops/sdk/api"
)

// shellWait is what `jailops shell` shows while the bash of a jail is being installed in the background: what it
// waits for, how to leave, and a line that moves so that nobody takes the wait for a hang.
type shellWait struct {
	out io.Writer
	// tty: one line that is rewritten in place; otherwise one new line every plainEvery.
	tty   bool
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
	get   func(ctx context.Context) (api.Jail, error)
	// poll is how often the jail is asked, limit how long the wait lasts at most.
	poll, limit time.Duration
}

// plainEvery is the distance between the lines of a wait that is not on a terminal.
const plainEvery = 5 * time.Second

// shellCond is the ShellSetup condition of the jail, nil when it has none.
func shellCond(j api.Jail) *api.Condition {
	for i := range j.Status.Conditions {
		if j.Status.Conditions[i].Type == "ShellSetup" {
			return &j.Status.Conditions[i]
		}
	}
	return nil
}

// run waits until the setup ends. It returns nil when there is nothing to wait for or the setup ended (the caller
// opens the shell, which says by itself what is missing), the error of the context when the wait is given up with
// Ctrl-C (the setup goes on in the daemon), and an error when the limit passes.
func (w shellWait) run(ctx context.Context, name string) error {
	j, err := w.get(ctx)
	if err != nil || !shellInstalling(j) {
		return nil
	}
	fprintln(w.out, "installing the shell (bash) in the jail; Ctrl-C stops waiting, `jailops exec "+name+" -- sh` works meanwhile")
	start := w.now()
	nextPlain := start
	spin := []string{"|", "/", "-", `\`}
	for i := 0; ; i++ {
		c := shellCond(j)
		step := "working"
		since := start
		if ss := j.Status.ShellSetup; ss != nil && ss.Step != "" {
			step = ss.Step
		}
		if c != nil && !c.Since.IsZero() {
			since = c.Since
		}
		elapsed := int(w.now().Sub(since).Seconds())
		if elapsed < 0 {
			elapsed = 0
		}
		line := fmt.Sprintf("%s %s (%d s)", spin[i%len(spin)], step, elapsed)
		if w.tty {
			_, _ = fmt.Fprintf(w.out, "\r\033[K%s", line)
		} else if !w.now().Before(nextPlain) {
			fprintln(w.out, line)
			nextPlain = w.now().Add(plainEvery)
		}
		if err := w.sleep(ctx, w.poll); err != nil {
			w.endLine()
			fprintln(w.out, "stopped waiting; the setup goes on in the jail")
			return err
		}
		if w.now().Sub(start) > w.limit {
			w.endLine()
			return fmt.Errorf("the shell is still not installed after %d s (%s): `jailops info %s` shows the setup, `jailops exec %s -- sh` works meanwhile",
				int(w.limit.Seconds()), strings.TrimSpace(step), name, name)
		}
		if j, err = w.get(ctx); err != nil || !shellInstalling(j) {
			w.endLine()
			return nil
		}
	}
}

// endLine ends the line that is rewritten in place.
func (w shellWait) endLine() {
	if w.tty {
		_, _ = fmt.Fprint(w.out, "\r\033[K")
	}
}

// sleepCtx sleeps d or until the context ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
