package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

// One command on many jails: `jailops exec JAIL... -- CMD`, `exec -l key=value -- CMD`, `exec --all -- CMD`.
//
// The command runs in every running jail, at most --parallel at a time, each in its own exec session (so each is
// audited as an exec, with the same project and role rules). The output of each jail is shown line by line with the jail's
// name in front, stdout on stdout and stderr on stderr, and passes through termText: a jail does not get to write control
// characters to the terminal of the person who ran the command on a fleet of them. Standard input is not forwarded.
//
// The exit status of jailops is 0 when every command exited 0 and every jail that was named could run it. Otherwise it
// is the highest exit status of the commands that failed, or 1 when a jail could not run the command at all (it does not
// exist, it is not running, the session failed): a script may treat any non-zero status as "at least one failed". Jails
// that a selector (-l, --all) matched and that are stopped are skipped with a note; a stopped jail that is named is a
// failure.

// DefaultExecParallel is how many jails run the command at a time unless --parallel says otherwise.
const DefaultExecParallel = 4

// MaxExecParallel is the most --parallel allows.
const MaxExecParallel = 32

// linePrefixer writes whole lines, each with a prefix, to w. Lines of several jails are written under one lock so that
// they never mix inside a line.
type linePrefixer struct {
	mu     *sync.Mutex
	w      io.Writer
	prefix string
	buf    []byte
}

func (p *linePrefixer) Write(b []byte) (int, error) {
	p.buf = append(p.buf, b...)
	for {
		i := indexByte(p.buf, '\n')
		if i < 0 {
			break
		}
		p.line(string(p.buf[:i]))
		p.buf = p.buf[i+1:]
	}
	if len(p.buf) > 1<<20 { // a line without end: no jail makes the CLI hold more than 1 MiB
		p.line(string(p.buf))
		p.buf = p.buf[:0]
	}
	return len(b), nil
}

func (p *linePrefixer) line(s string) {
	s = strings.TrimSuffix(s, "\r")
	p.mu.Lock()
	defer p.mu.Unlock()
	fprintf(p.w, "%s%s\n", p.prefix, colored(termText(s, false)))
}

// flush writes what is left of the last line.
func (p *linePrefixer) flush() {
	if len(p.buf) > 0 {
		p.line(string(p.buf))
		p.buf = nil
	}
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

// execResult is what one jail did.
type execResult struct {
	jail    string
	code    int    // exit status of the command
	skipped string // why the command did not run on a jail a selector matched
	fail    string // why a jail could not run the command at all
}

func (r execResult) failed() bool { return r.fail != "" || r.code != 0 }

// runExecMany runs argv in each of the jails.
func (a *app) runExecMany(ctx context.Context, names []string, bulk bool, argv []string, o execOpts, parallel int) error {
	if parallel < 1 || parallel > MaxExecParallel {
		return usagef("--parallel must be between 1 and %d", MaxExecParallel)
	}
	if len(names) == 0 {
		a.say("no jail matched\n")
		return nil
	}
	if ok, err := a.dry("run a command in "+fmt.Sprint(len(names))+" jail(s): "+strings.Join(names, ", "), map[string]any{"jails": names, "argv": argv, "parallel": parallel}); ok || err != nil {
		return err
	}
	c, err := a.connect(ctx)
	if err != nil {
		return err
	}
	so := sdk.ExecOptions{Env: o.env, User: o.user}
	results := make([]execResult, len(names))
	var outMu, errMu sync.Mutex
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i, name := range names {
		results[i] = execResult{jail: name}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[i].fail = "interrupted"
				return
			}
			j, err := c.Jails().Get(ctx, name)
			switch {
			case err != nil:
				results[i].fail = errorText(err)
				return
			case j.Status.Power != api.PowerRunning:
				if bulk {
					results[i].skipped = "not running"
				} else {
					results[i].fail = "not running"
				}
				return
			}
			sess, err := c.Jails().Exec(ctx, name, argv, so)
			if err != nil {
				results[i].fail = errorText(err)
				return
			}
			defer sess.Close()
			stop := context.AfterFunc(ctx, func() { _ = sess.Close() })
			defer stop()
			prefix := "[" + termText(name, false) + "] "
			out := &linePrefixer{mu: &outMu, w: a.env.Stdout, prefix: prefix}
			errw := &linePrefixer{mu: &errMu, w: a.env.Stderr, prefix: prefix}
			code, err := sess.Run(nil, out, errw)
			out.flush()
			errw.flush()
			if ctx.Err() != nil {
				results[i].fail = "interrupted"
				return
			}
			if err != nil {
				results[i].fail = errorText(err)
				return
			}
			results[i].code = code
		}()
	}
	wg.Wait()
	return a.execSummary(results, bulk)
}

func errorText(err error) string {
	var ae *api.Error
	if errors.As(err, &ae) {
		return ae.Message
	}
	return err.Error()
}

// execSummary prints what happened on standard error and returns the exit status as an error.
func (a *app) execSummary(results []execResult, bulk bool) error {
	var ok, failed, skipped []string
	exit := 0
	for _, r := range results {
		switch {
		case r.skipped != "":
			skipped = append(skipped, r.jail+": "+r.skipped)
		case r.fail != "":
			failed = append(failed, r.jail+": "+r.fail)
			exit = max(exit, 1)
		case r.code != 0:
			failed = append(failed, fmt.Sprintf("%s: exit %d", r.jail, r.code))
			exit = max(exit, r.code)
		default:
			ok = append(ok, r.jail)
		}
	}
	sort.Strings(ok)
	if len(results) > 1 || bulk || len(failed) > 0 {
		parts := []string{fmt.Sprintf("%d ok", len(ok))}
		if len(failed) > 0 {
			parts = append(parts, fmt.Sprintf("%d failed (%s)", len(failed), strings.Join(failed, "; ")))
		}
		if len(skipped) > 0 {
			parts = append(parts, fmt.Sprintf("%d skipped (%s)", len(skipped), strings.Join(skipped, "; ")))
		}
		if !a.quiet || len(failed) > 0 {
			fprintf(a.env.Stderr, "exec: %s\n", colored(termText(strings.Join(parts, ", "), false)))
		}
	}
	if exit != 0 {
		return &exitError{code: exit}
	}
	return nil
}
