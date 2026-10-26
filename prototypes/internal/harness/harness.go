// Package harness is the shared safety and reporting frame of the JailOps
// prototypes. Every prototype runs through Main, which enforces:
//
//   - refusal to run without --yes-i-have-a-scratch-pool (except --dry-run)
//   - all ZFS work confined to $JAILOPS_PROTO_POOL/jailops-proto-<n>
//   - interface and jail names prefixed "jop"
//   - --dry-run prints every command without executing it
//   - --cleanup removes only what the prototype created
//   - a final machine-readable JSON verdict line
//
// None of this was run on FreeBSD by its author. Anything that depends on FreeBSD
// behaviour is marked UNVERIFIED: in the prototype that uses it.
package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// NamePrefix is the mandatory prefix of every interface, jail and bridge a prototype creates.
const NamePrefix = "jop"

// YesFlag is the explicit consent flag.
const YesFlag = "yes-i-have-a-scratch-pool"

// Check is one line of the verdict.
type Check struct {
	Name   string `json:"name"`
	Ok     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// Verdict is the final JSON line.
type Verdict struct {
	Prototype          int      `json:"prototype"`
	Pass               bool     `json:"pass"`
	Checks             []Check  `json:"checks"`
	UnverifiedResolved []string `json:"unverified_resolved"`
	DryRun             bool     `json:"dry_run,omitempty"`
	Aborted            string   `json:"aborted,omitempty"`
	Host               string   `json:"host,omitempty"`
}

// Result of one command.
type Result struct {
	Stdout string
	Stderr string
	Code   int
	Err    error
	Dry    bool
}

// Out is stdout trimmed.
func (r Result) Out() string { return strings.TrimSpace(r.Stdout) }

// OK reports success (a dry run counts as success so plans print fully).
func (r Result) OK() bool { return r.Dry || (r.Err == nil && r.Code == 0) }

// Combined returns stdout and stderr for logs.
func (r Result) Combined() string {
	return strings.TrimSpace(strings.TrimSpace(r.Stdout) + "\n" + strings.TrimSpace(r.Stderr))
}

// Env is the per-run context.
type Env struct {
	N        int
	Name     string
	Pool     string // $JAILOPS_PROTO_POOL
	Root     string // <pool>/jailops-proto-<n>
	DryRun   bool
	Cleanup  bool
	Yes      bool
	Verbose  bool
	Out      io.Writer
	Timeout  time.Duration
	Checks   []Check
	Resolved []string

	cleanups []func()
	aborted  string
}

type abortSignal struct{ msg string }

// RootName returns the dataset root for prototype n under pool.
func RootName(pool string, n int) string {
	return fmt.Sprintf("%s/jailops-proto-%d", pool, n)
}

// ValidIfName reports whether an interface, bridge or jail name carries the prefix.
func ValidIfName(s string) bool {
	return strings.HasPrefix(s, NamePrefix) && len(s) > len(NamePrefix) && len(s) <= 15 &&
		strings.IndexFunc(s, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_')
		}) < 0
}

// GuardZFS returns an error when any dataset argument of a zfs command lies outside root.
// Arguments that start with "-" or do not start with "<pool>/" are not datasets (flags,
// tokens, mountpoints). A bare pool name is refused. Property assignments (k=v) are checked
// on the value only for the origin/clone cases, which always are datasets and carry a slash.
func GuardZFS(pool, root string, args []string) error {
	if pool == "" || root == "" {
		return errors.New("harness: no pool configured")
	}
	for i, a := range args {
		if i == 0 || strings.HasPrefix(a, "-") {
			continue
		}
		cands := []string{a}
		if k, v, ok := strings.Cut(a, "="); ok && !strings.HasPrefix(a, "/") {
			_ = k
			cands = []string{v}
		}
		for _, c := range cands {
			for _, part := range strings.Split(c, ",") { // zfs destroy a@s1%s2, snapshot lists
				name := part
				if j := strings.IndexAny(name, "@#"); j >= 0 {
					name = name[:j]
				}
				if name == pool {
					return fmt.Errorf("harness: refusing zfs argument %q: the pool itself", a)
				}
				if strings.HasPrefix(name, pool+"/") && name != root && !strings.HasPrefix(name, root+"/") {
					return fmt.Errorf("harness: refusing zfs argument %q: outside %s", a, root)
				}
				if strings.HasPrefix(name, "@") { // "@snap" shorthand only valid with a dataset elsewhere
					continue
				}
			}
		}
	}
	return nil
}

// Printf prints to the report.
func (e *Env) Printf(format string, a ...any) { fmt.Fprintf(e.Out, format, a...) }

// Step prints a numbered-ish heading.
func (e *Env) Step(format string, a ...any) {
	fmt.Fprintf(e.Out, "\n== %s\n", fmt.Sprintf(format, a...))
}

// Note prints an indented note.
func (e *Env) Note(format string, a ...any) { fmt.Fprintf(e.Out, "   %s\n", fmt.Sprintf(format, a...)) }

// OnCleanup registers an undo that runs when the prototype finishes, aborts or panics
// (watchdogs, temporary interfaces). Persistent artifacts are removed by Spec.Cleanup.
func (e *Env) OnCleanup(f func()) { e.cleanups = append(e.cleanups, f) }

// RunCleanups runs registered cleanups in reverse order.
func (e *Env) RunCleanups() {
	for i := len(e.cleanups) - 1; i >= 0; i-- {
		func() {
			defer func() { recover() }()
			e.cleanups[i]()
		}()
	}
	e.cleanups = nil
}

// Check records a verdict line and prints it. It returns ok. In a dry run nothing is recorded.
func (e *Env) Check(name string, ok bool, detail string) bool {
	if e.DryRun {
		e.Printf("   [dry-run] check: %s\n", name)
		return true
	}
	e.Checks = append(e.Checks, Check{Name: name, Ok: ok, Detail: detail})
	st := "PASS"
	if !ok {
		st = "FAIL"
	}
	e.Printf("   [%s] %s: %s\n", st, name, detail)
	return ok
}

// Must records a check and aborts the prototype when it fails.
func (e *Env) Must(name string, ok bool, detail string) {
	if !e.Check(name, ok, detail) {
		panic(abortSignal{"required check failed: " + name})
	}
}

// Resolve records that an UNVERIFIED: assumption was settled (either way) by this run.
func (e *Env) Resolve(s string) {
	if e.DryRun {
		return
	}
	e.Resolved = append(e.Resolved, s)
	e.Printf("   [resolved] %s\n", s)
}

// Abort stops the prototype with a message.
func (e *Env) Abort(format string, a ...any) { panic(abortSignal{fmt.Sprintf(format, a...)}) }

func quote(argv []string) string {
	var b strings.Builder
	for i, a := range argv {
		if i > 0 {
			b.WriteByte(' ')
		}
		if a == "" || strings.ContainsAny(a, " \t\"'$&|;<>*?()[]{}\\#") {
			b.WriteString("'" + strings.ReplaceAll(a, "'", `'\''`) + "'")
		} else {
			b.WriteString(a)
		}
	}
	return b.String()
}

// Run executes argv (never a shell) with the default timeout. A dry run prints and returns Dry.
func (e *Env) Run(argv ...string) Result { return e.RunStdin("", argv...) }

// RunT is Run with an explicit timeout.
func (e *Env) RunT(timeout time.Duration, argv ...string) Result {
	return e.run(timeout, nil, argv)
}

// RunStdin is Run with stdin text.
func (e *Env) RunStdin(stdin string, argv ...string) Result {
	var r io.Reader
	if stdin != "" {
		r = strings.NewReader(stdin)
	}
	return e.run(e.Timeout, r, argv)
}

func (e *Env) run(timeout time.Duration, stdin io.Reader, argv []string) Result {
	if len(argv) == 0 {
		return Result{Err: errors.New("empty argv"), Code: -1}
	}
	if argv[0] == "zfs" {
		if err := GuardZFS(e.Pool, e.Root, argv); err != nil {
			panic(abortSignal{err.Error()})
		}
	}
	if e.DryRun {
		e.Printf("   DRY-RUN would run: %s\n", quote(argv))
		return Result{Dry: true}
	}
	e.Printf("   $ %s\n", quote(argv))
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = stdin
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	res := Result{Stdout: so.String(), Stderr: se.String()}
	if err != nil {
		res.Err = err
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.Code = ee.ExitCode()
		} else {
			res.Code = -1
		}
	}
	if e.Verbose || err != nil {
		if s := res.Combined(); s != "" {
			for _, l := range strings.Split(s, "\n") {
				e.Printf("     | %s\n", l)
			}
		}
	}
	return res
}

// Pipe runs `a | b` without a shell. Both commands are guarded like Run.
func (e *Env) Pipe(a, b []string) (Result, Result) {
	for _, argv := range [][]string{a, b} {
		if argv[0] == "zfs" {
			if err := GuardZFS(e.Pool, e.Root, argv); err != nil {
				panic(abortSignal{err.Error()})
			}
		}
	}
	if e.DryRun {
		e.Printf("   DRY-RUN would run: %s | %s\n", quote(a), quote(b))
		return Result{Dry: true}, Result{Dry: true}
	}
	e.Printf("   $ %s | %s\n", quote(a), quote(b))
	ctx, cancel := context.WithTimeout(context.Background(), e.Timeout)
	defer cancel()
	ca := exec.CommandContext(ctx, a[0], a[1:]...)
	cb := exec.CommandContext(ctx, b[0], b[1:]...)
	pr, pw := io.Pipe()
	ca.Stdout, cb.Stdin = pw, pr
	var ae, be, bo bytes.Buffer
	ca.Stderr, cb.Stdout, cb.Stderr = &ae, &bo, &be
	ra, rb := Result{}, Result{}
	if err := cb.Start(); err != nil {
		return Result{Err: err, Code: -1}, Result{Err: err, Code: -1}
	}
	errA := ca.Run()
	pw.Close()
	errB := cb.Wait()
	conv := func(err error, so, se string) Result {
		r := Result{Stdout: so, Stderr: se, Err: err}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			r.Code = ee.ExitCode()
		} else if err != nil {
			r.Code = -1
		}
		return r
	}
	ra, rb = conv(errA, "", ae.String()), conv(errB, bo.String(), be.String())
	for _, r := range []Result{ra, rb} {
		if r.Err != nil {
			e.Printf("     | %s\n", strings.ReplaceAll(r.Combined(), "\n", "\n     | "))
		}
	}
	return ra, rb
}

// Zfs runs `zfs args...` through the guard.
func (e *Env) Zfs(args ...string) Result { return e.Run(append([]string{"zfs"}, args...)...) }

// ZfsGet reads one property value with -H -p -o value. Dry runs return "".
func (e *Env) ZfsGet(prop, target string) string {
	r := e.quiet("zfs", "get", "-H", "-p", "-o", "value", prop, target)
	return r.Out()
}

// quiet runs a read-only command without printing it unless it fails.
func (e *Env) quiet(argv ...string) Result {
	if e.DryRun {
		return Result{Dry: true}
	}
	v := e.Verbose
	e.Verbose = false
	defer func() { e.Verbose = v }()
	save := e.Out
	e.Out = io.Discard
	r := e.run(e.Timeout, nil, argv)
	e.Out = save
	if r.Err != nil {
		e.Printf("   (read-only %s failed: %v %s)\n", quote(argv), r.Err, strings.TrimSpace(r.Stderr))
	}
	return r
}

// Quiet exposes quiet for read-only probes.
func (e *Env) Quiet(argv ...string) Result { return e.quiet(argv...) }

// DatasetExists reports whether a dataset exists (always false in a dry run).
func (e *Env) DatasetExists(ds string) bool {
	if e.DryRun {
		return false
	}
	r := e.quiet("zfs", "list", "-H", "-o", "name", ds)
	return r.Err == nil
}

// Have reports whether a command is on PATH.
func Have(cmd string) bool { _, err := exec.LookPath(cmd); return err == nil }

// Require aborts unless every command exists (dry runs only warn).
func (e *Env) Require(cmds ...string) {
	for _, c := range cmds {
		if !Have(c) {
			if e.DryRun {
				e.Note("(warning) %s not on PATH", c)
				continue
			}
			e.Abort("required command %q not found", c)
		}
	}
}

// Setup is called by Main to let a prototype register flags before parsing.
type Setup func(e *Env, fs *flag.FlagSet)

// Spec describes one prototype to Main.
type Spec struct {
	N       int
	Name    string
	Summary string
	// Plan prints, in plain sentences, what the prototype will do. Printed always before running.
	Plan    []string
	Flags   Setup
	Run     func(e *Env)
	Cleanup func(e *Env)
	// NoPool is true for prototypes that need no ZFS (7). They still honour the flags.
	NoPool bool
}

// Main parses flags, enforces safety, runs the prototype and prints the verdict. It returns the exit code.
func Main(s Spec, args []string, out io.Writer) int {
	e := &Env{N: s.N, Name: s.Name, Out: out, Timeout: 120 * time.Second}
	fs := flag.NewFlagSet(s.Name, flag.ContinueOnError)
	fs.SetOutput(out)
	fs.BoolVar(&e.Yes, YesFlag, false, "required: confirm the pool is a scratch pool")
	fs.BoolVar(&e.DryRun, "dry-run", false, "print what would be done and run nothing")
	fs.BoolVar(&e.Cleanup, "cleanup", false, "remove everything this prototype created, then exit")
	fs.BoolVar(&e.Verbose, "v", false, "show command output even on success")
	fs.StringVar(&e.Pool, "pool", os.Getenv("JAILOPS_PROTO_POOL"), "scratch pool (default $JAILOPS_PROTO_POOL)")
	fs.DurationVar(&e.Timeout, "cmd-timeout", e.Timeout, "default timeout per command")
	if s.Flags != nil {
		s.Flags(e, fs)
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	e.Pool = strings.Trim(e.Pool, "/ ")
	host, _ := os.Hostname()
	v := Verdict{Prototype: s.N, Host: host}
	fmt.Fprintf(out, "JailOps prototype %d: %s\n%s\n", s.N, s.Name, s.Summary)
	fmt.Fprintf(out, "NOTE: written without access to FreeBSD; every UNVERIFIED: assumption is being tested by this run.\n")
	if !s.NoPool {
		if e.Pool == "" {
			fmt.Fprintf(out, "refusing: set JAILOPS_PROTO_POOL or --pool to a scratch pool\n")
			return 2
		}
		if strings.ContainsAny(e.Pool, " /@#") {
			fmt.Fprintf(out, "refusing: --pool must be a pool name, not a dataset path\n")
			return 2
		}
		e.Root = RootName(e.Pool, s.N)
		fmt.Fprintf(out, "dataset root: %s (the only ZFS tree this prototype touches)\n", e.Root)
	}
	fmt.Fprintf(out, "interface/jail prefix: %s\n\nPLAN:\n", NamePrefix)
	for _, p := range s.Plan {
		fmt.Fprintf(out, "  - %s\n", p)
	}
	if e.Cleanup {
		fmt.Fprintf(out, "  - (--cleanup) undo everything above that exists, nothing else\n")
	}
	e.Printf("\n")
	if !e.DryRun {
		if runtime.GOOS != "freebsd" {
			fmt.Fprintf(out, "refusing: GOOS=%s; these prototypes run only on FreeBSD (use --dry-run elsewhere)\n", runtime.GOOS)
			return 2
		}
		if !e.Yes {
			fmt.Fprintf(out, "refusing: pass --%s to confirm %q is disposable\n", YesFlag, e.Pool)
			return 2
		}
		if os.Geteuid() != 0 {
			fmt.Fprintf(out, "refusing: run as root\n")
			return 2
		}
	}
	body := s.Run
	if e.Cleanup {
		body = s.Cleanup
		if body == nil {
			fmt.Fprintf(out, "no cleanup defined\n")
			return 2
		}
	}
	func() {
		defer func() {
			if r := recover(); r != nil {
				if a, ok := r.(abortSignal); ok {
					e.aborted = a.msg
				} else {
					e.aborted = fmt.Sprintf("panic: %v", r)
				}
				if e.DryRun {
					e.Printf("   (dry run stopped early: %s; later steps depend on real command output)\n", e.aborted)
				} else {
					e.Printf("\nABORT: %s\n", e.aborted)
				}
			}
			if !e.DryRun {
				e.Printf("\n== running registered cleanups\n")
			}
			e.RunCleanups()
		}()
		body(e)
	}()
	if e.Cleanup {
		fmt.Fprintf(out, "cleanup finished\n")
		return 0
	}
	v.DryRun = e.DryRun
	v.Aborted = e.aborted
	v.Checks = e.Checks
	if v.Checks == nil {
		v.Checks = []Check{}
	}
	v.UnverifiedResolved = e.Resolved
	if v.UnverifiedResolved == nil {
		v.UnverifiedResolved = []string{}
	}
	v.Pass = !e.DryRun && e.aborted == "" && len(v.Checks) > 0
	for _, c := range v.Checks {
		if !c.Ok {
			v.Pass = false
		}
	}
	b, _ := json.Marshal(v)
	fmt.Fprintf(out, "\n%s\n", b)
	if v.Pass || e.DryRun {
		return 0
	}
	return 1
}

// Exit is a convenience for main().
func Exit(s Spec) { os.Exit(Main(s, os.Args[1:], os.Stdout)) }
