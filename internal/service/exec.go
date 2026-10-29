package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"

	"jailops.net/jailops/sdk/api"
)

// ExecOpts are the options of one command run in a jail.
type ExecOpts struct {
	TTY        bool
	Rows, Cols uint16
	Env        []string
	User       string
}

// ExecSession is a running command. In TTY mode Stderr is nil: the terminal
// has one output stream.
type ExecSession interface {
	Stdin() io.WriteCloser
	Stdout() io.Reader
	Stderr() io.Reader
	Resize(rows, cols uint16) error
	Signal(sig int) error
	// Wait returns the exit status once the command ended.
	Wait() (exitCode int, err error)
}

// JailExecer runs commands in jails through the root helper (jexec, with a
// pseudo-terminal when asked for one).
type JailExecer interface {
	Exec(ctx context.Context, jail string, argv []string, o ExecOpts) (ExecSession, error)
}

// ExecLimits bound exec sessions.
type ExecLimits struct {
	// PerJail is the number of concurrent sessions of one jail (default 8).
	PerJail int
	// Idle ends a session in which nothing moves for this long (default 30m).
	Idle time.Duration
	// Max ends a session after this long (default 12h).
	Max time.Duration
}

func (l ExecLimits) withDefaults() ExecLimits {
	if l.PerJail <= 0 {
		l.PerJail = 8
	}
	if l.Idle <= 0 {
		l.Idle = 30 * time.Minute
	}
	if l.Max <= 0 {
		l.Max = 12 * time.Hour
	}
	return l
}

// ExecLimitsOf returns the limits of this daemon.
func (s *Service) ExecLimitsOf() ExecLimits { return s.d.ExecLimits.withDefaults() }

// ExecStart is a started exec session with what the server needs to run it.
type ExecStart struct {
	Session ExecSession
	// Release must be called when the session is over.
	Release func()
	// Argv is what runs, for the audit entry.
	Argv    []string
	Project string
	Jail    string
}

// DefaultShell is the shell of a jail that names none.
const DefaultShell = "/bin/sh"

// StartExec starts a command in a jail of the request's project: the same
// project rules as every jail route (a project the caller may not use, or a
// jail of another project, is not_found), a running jail, at most
// ExecLimits.PerJail sessions per jail, and a shell that exists.
func (s *Service) StartExec(ctx context.Context, name string, req api.ExecRequest) (*ExecStart, error) {
	if s.d.Execer == nil {
		return nil, api.Errorf(api.CodeUnsupported, "this daemon has no way to run commands in jails").
			WithHint("jailops-priv offers exec; run jailopsd with the helper")
	}
	p, key, err := s.jailRef(ctx, name)
	if err != nil {
		return nil, err
	}
	j, err := s.getByKey(ctx, key)
	if err != nil {
		return nil, err
	}
	if j.Status.Power != api.PowerRunning {
		return nil, api.Errorf(api.CodeBusy, "jail %q is not running", name).WithHint("start it first")
	}
	argv := req.Argv
	if req.Shell || len(argv) == 0 {
		sh := req.ShellPath
		if sh == "" {
			sh = j.Spec.Shell
		}
		if sh == "" {
			sh = DefaultShell
		}
		if !strings.HasPrefix(sh, "/") || strings.ContainsAny(sh, "\x00\n ") {
			return nil, invalid("shell %q must be an absolute path inside the jail", sh)
		}
		if ok, _ := s.d.Files.Exists(ctx, path.Join(s.rootMount(key), sh)); !ok {
			return nil, api.Errorf(api.CodeNotFound, "the shell %s does not exist in jail %q", sh, name).
				WithHint("install it, or choose another with `jailops set " + name + " shell <path>`")
		}
		argv = []string{sh}
	}
	for _, a := range argv {
		if strings.ContainsRune(a, 0) {
			return nil, invalid("an argument contains a NUL byte")
		}
	}
	for _, e := range req.Env {
		if k, _, ok := strings.Cut(e, "="); !ok || k == "" || strings.ContainsAny(k, "\x00 ") || strings.ContainsRune(e, 0) {
			return nil, invalid("environment entry %q must be NAME=value", e)
		}
	}
	lim := s.ExecLimitsOf()
	s.execMu.Lock()
	if s.installing[key] {
		s.execMu.Unlock()
		return nil, api.Errorf(api.CodeBusy, "jail %q is busy: the app is being installed", name).
			WithHint("wait for the installation to end; `jailops op list` shows it")
	}
	if s.execN[key] >= lim.PerJail {
		s.execMu.Unlock()
		return nil, api.Errorf(api.CodeBusy, "jail %q already has %d exec sessions", name, lim.PerJail)
	}
	s.execN[key]++
	s.execMu.Unlock()
	var once sync.Once
	release := func() {
		once.Do(func() {
			s.execMu.Lock()
			s.execN[key]--
			s.execMu.Unlock()
		})
	}
	profs, _ := s.resolveProfiles(p.Meta.Name, j.Spec.Profiles)
	eff, _ := api.ExpandedConfig(j.Spec, profs)
	jenv, err := s.jailEnv(eff.Env)
	if err != nil {
		release()
		return nil, err
	}
	// what the caller names wins over what the jail declares
	sess, err := s.d.Execer.Exec(ctx, key, argv, ExecOpts{TTY: req.TTY, Rows: req.Rows, Cols: req.Cols, Env: append(jenv, req.Env...), User: req.User})
	if err != nil {
		release()
		return nil, err
	}
	return &ExecStart{Session: sess, Release: release, Argv: argv, Project: p.Meta.Name, Jail: name}, nil
}

// ---- implementations ----

// LocalExecer runs commands as child processes of jailopsd with os/exec. It
// does not enter a jail: it exists for tests and development, where Prefix
// (for example {"jexec", "-l"} with the jail name appended) can add the jail.
// It has no pseudo-terminal (the standard library has none): TTY is refused.
type LocalExecer struct {
	// Prefix is put before argv; when it is not empty the jail name is appended to it.
	Prefix []string
}

// Exec implements JailExecer.
func (l LocalExecer) Exec(ctx context.Context, jail string, argv []string, o ExecOpts) (ExecSession, error) {
	if len(argv) == 0 {
		return nil, invalid("empty command")
	}
	if o.TTY {
		return nil, api.Errorf(api.CodeUnsupported, "a terminal needs the root helper's pseudo-terminal support")
	}
	full := append([]string(nil), l.Prefix...)
	if len(l.Prefix) > 0 {
		full = append(full, jail)
	}
	full = append(full, argv...)
	cmd := exec.Command(full[0], full[1:]...)
	cmd.Env = append(os.Environ(), o.Env...)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	errp, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, api.Errorf(api.CodeInternal, "starting %s: %v", full[0], err)
	}
	return &localSession{cmd: cmd, in: in, out: out, err: errp}, nil
}

type localSession struct {
	cmd      *exec.Cmd
	in       io.WriteCloser
	out, err io.Reader
}

func (s *localSession) Stdin() io.WriteCloser { return s.in }
func (s *localSession) Stdout() io.Reader     { return s.out }
func (s *localSession) Stderr() io.Reader     { return s.err }
func (s *localSession) Resize(uint16, uint16) error {
	return api.Errorf(api.CodeUnsupported, "no terminal")
}
func (s *localSession) Signal(sig int) error { return s.cmd.Process.Signal(syscall.Signal(sig)) }
func (s *localSession) Wait() (int, error) {
	err := s.cmd.Wait()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	return 0, err
}

// FakeExecer is an in-memory JailExecer for tests. Programs: "echo args",
// "cat" (stdin to stdout until end of input), "stderr text", "exit N",
// "sleep" (until signalled) and "tty" (prints the window size, then each
// resize). Anything else exits 127.
type FakeExecer struct {
	mu    sync.Mutex
	calls []FakeExecCall
}

// FakeExecCall records one Exec call.
type FakeExecCall struct {
	Jail string
	Argv []string
	Opts ExecOpts
}

// Calls returns what was run.
func (f *FakeExecer) Calls() []FakeExecCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]FakeExecCall(nil), f.calls...)
}

// Exec implements JailExecer.
func (f *FakeExecer) Exec(_ context.Context, jail string, argv []string, o ExecOpts) (ExecSession, error) {
	f.mu.Lock()
	f.calls = append(f.calls, FakeExecCall{jail, append([]string(nil), argv...), o})
	f.mu.Unlock()
	if len(argv) == 0 {
		return nil, invalid("empty command")
	}
	s := &fakeSession{done: make(chan struct{}), sig: make(chan int, 4), resize: make(chan [2]uint16, 8), tty: o.TTY}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s.in, s.inR, s.outR, s.outW = inW, inR, outR, outW
	if !o.TTY {
		s.errR, s.errW = io.Pipe()
	}
	go s.run(argv, o)
	return s, nil
}

type fakeSession struct {
	in        io.WriteCloser
	inR       *io.PipeReader
	outR      *io.PipeReader
	outW      *io.PipeWriter
	errR      *io.PipeReader
	errW      *io.PipeWriter
	tty       bool
	sig       chan int
	resize    chan [2]uint16
	done      chan struct{}
	code      int
	closeOnce sync.Once
}

func (s *fakeSession) Stdin() io.WriteCloser { return s.in }
func (s *fakeSession) Stdout() io.Reader     { return s.outR }
func (s *fakeSession) Stderr() io.Reader {
	if s.errR == nil {
		return nil
	}
	return s.errR
}
func (s *fakeSession) Resize(rows, cols uint16) error {
	if !s.tty {
		return api.Errorf(api.CodeInvalid, "not a terminal session")
	}
	select {
	case s.resize <- [2]uint16{rows, cols}:
	default:
	}
	return nil
}
func (s *fakeSession) Signal(sig int) error {
	select {
	case s.sig <- sig:
	default:
	}
	return nil
}
func (s *fakeSession) Wait() (int, error) { <-s.done; return s.code, nil }

func (s *fakeSession) finish(code int) {
	s.closeOnce.Do(func() {
		s.code = code
		s.outW.Close()
		if s.errW != nil {
			s.errW.Close()
		}
		s.inR.Close()
		close(s.done)
	})
}

func (s *fakeSession) run(argv []string, o ExecOpts) {
	switch argv[0] {
	case "echo":
		fmt.Fprintln(s.outW, strings.Join(argv[1:], " "))
		s.finish(0)
	case "cat":
		_, _ = io.Copy(s.outW, s.inR)
		s.finish(0)
	case "stderr":
		if s.errW != nil {
			fmt.Fprintln(s.errW, strings.Join(argv[1:], " "))
		} else {
			fmt.Fprintln(s.outW, strings.Join(argv[1:], " "))
		}
		s.finish(2)
	case "exit":
		n := 0
		if len(argv) > 1 {
			fmt.Sscanf(argv[1], "%d", &n)
		}
		s.finish(n)
	case "env":
		fmt.Fprintln(s.outW, strings.Join(o.Env, "\n"))
		s.finish(0)
	case "tty":
		fmt.Fprintf(s.outW, "%dx%d\n", o.Rows, o.Cols)
		for {
			select {
			case r := <-s.resize:
				fmt.Fprintf(s.outW, "%dx%d\n", r[0], r[1])
			case <-s.sig:
				s.finish(130)
				return
			case <-s.done:
				return
			}
		}
	case "sleep":
		sig := <-s.sig
		s.finish(128 + sig)
	default:
		if s.errW != nil {
			fmt.Fprintf(s.errW, "%s: not found\n", argv[0])
		}
		s.finish(127)
	}
}
