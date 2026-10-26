// Package gitx runs the git binary for jailops-sync. Commands are built from an
// argv slice and executed directly, never through a shell, always with a
// timeout and with prompts disabled.
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DefaultTimeout bounds one git command.
const DefaultTimeout = 2 * time.Minute

// Runner runs git in one directory.
type Runner struct {
	Dir     string
	Timeout time.Duration
	// Bin is the git binary; empty means "git" from PATH.
	Bin string
}

// Run executes `git -C <dir> <args...>` and returns its stdout.
func (r Runner) Run(ctx context.Context, args ...string) (string, error) {
	bin := r.Bin
	if bin == "" {
		bin = "git"
	}
	if _, err := exec.LookPath(bin); err != nil {
		return "", fmt.Errorf("git is not installed: %w", err)
	}
	to := r.Timeout
	if to <= 0 {
		to = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	argv := append([]string{"-C", filepath.Clean(r.Dir)}, args...)
	cmd := exec.CommandContext(ctx, bin, argv...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), ctx.Err())
		}
		return "", fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// IsRepo reports whether Dir is inside a git work tree.
func (r Runner) IsRepo(ctx context.Context) bool {
	out, err := r.Run(ctx, "rev-parse", "--is-inside-work-tree")
	return err == nil && out == "true"
}

// Head returns the current commit.
func (r Runner) Head(ctx context.Context) (string, error) { return r.Run(ctx, "rev-parse", "HEAD") }

// Status is a shallow read of the repository state.
type Status struct {
	IsRepo bool
	Dirty  bool
	Behind int
	Ahead  int
}

// Inspect reads the state. A directory that is not a repository, or a missing
// git, yields IsRepo=false without an error: git is a courtesy for hygiene.
func (r Runner) Inspect(ctx context.Context) Status {
	if !r.IsRepo(ctx) {
		return Status{}
	}
	s := Status{IsRepo: true}
	if out, err := r.Run(ctx, "status", "--porcelain"); err == nil && out != "" {
		s.Dirty = true
	}
	if _, err := r.Run(ctx, "rev-parse", "--abbrev-ref", "@{u}"); err == nil {
		if out, err := r.Run(ctx, "rev-list", "--left-right", "--count", "@{u}...HEAD"); err == nil {
			if f := strings.Fields(out); len(f) == 2 {
				s.Behind, _ = strconv.Atoi(f[0])
				s.Ahead, _ = strconv.Atoi(f[1])
			}
		}
	}
	return s
}

// Warnings lists what an operator should notice before applying.
func (s Status) Warnings() []string {
	var out []string
	if s.Dirty {
		out = append(out, "the fleet repository has uncommitted changes")
	}
	if s.Behind > 0 {
		out = append(out, fmt.Sprintf("the fleet repository is %d commit(s) behind its upstream", s.Behind))
	}
	return out
}

// Pull fast-forwards the checked-out branch from remote/branch. It refuses
// anything that is not a fast-forward and any dirty work tree, so a watcher
// never merges or discards local work. With an empty branch it follows the
// branch's upstream. It returns whether HEAD moved.
func (r Runner) Pull(ctx context.Context, remote, branch string) (changed bool, head string, err error) {
	if !r.IsRepo(ctx) {
		return false, "", errors.New("not a git repository")
	}
	if st := r.Inspect(ctx); st.Dirty {
		return false, "", errors.New("the work tree is dirty; refusing to pull")
	}
	before, err := r.Head(ctx)
	if err != nil {
		return false, "", err
	}
	for _, v := range []string{remote, branch} {
		if strings.HasPrefix(v, "-") {
			return false, before, fmt.Errorf("invalid remote or branch %q", v)
		}
	}
	if branch == "" {
		// Follow the branch's configured upstream.
		args := []string{"fetch", "--quiet", "--prune"}
		if remote != "" {
			args = append(args, remote)
		}
		if _, err := r.Run(ctx, args...); err != nil {
			return false, before, err
		}
		if _, err := r.Run(ctx, "merge", "--ff-only", "--quiet", "@{u}"); err != nil {
			return false, before, err
		}
	} else {
		if remote == "" {
			remote = "origin"
		}
		if _, err := r.Run(ctx, "fetch", "--quiet", "--prune", remote, branch); err != nil {
			return false, before, err
		}
		if _, err := r.Run(ctx, "merge", "--ff-only", "--quiet", "FETCH_HEAD"); err != nil {
			return false, before, err
		}
	}
	after, err := r.Head(ctx)
	if err != nil {
		return false, before, err
	}
	return after != before, after, nil
}
