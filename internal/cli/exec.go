package cli

import (
	"context"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

// Terminal hooks of Env. The defaults work on real files; tests replace them.

func defaultRawTerminal(in any) (func(), error) {
	f, ok := in.(interface{ Fd() uintptr })
	if !ok {
		return func() {}, nil
	}
	fd := int(f.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	return func() { _ = term.Restore(fd, old) }, nil
}

func defaultTermSize(out any) (rows, cols uint16, ok bool) {
	f, isFile := out.(interface{ Fd() uintptr })
	if !isFile {
		return 0, 0, false
	}
	w, h, err := term.GetSize(int(f.Fd()))
	if err != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return uint16(h), uint16(w), true
}

func defaultResizeSignal() (<-chan struct{}, func()) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGWINCH)
	out := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-sig:
				select {
				case out <- struct{}{}:
				default:
				}
			case <-done:
				return
			}
		}
	}()
	return out, func() { signal.Stop(sig); close(done) }
}

type execOpts struct {
	// noTTY runs without a pseudo-terminal even when stdin and stdout are
	// terminals, which is when one is allocated otherwise.
	noTTY     bool
	shell     bool
	shellPath string
	env       []string
	user      string
}

// runExec runs a command in a jail and returns its exit status as an
// exitError. With a terminal the local terminal is put in raw mode for the
// whole session and window size changes are forwarded. The server picks the
// shell and fails clearly when it is missing in the jail; the client never
// substitutes another one.
func (a *app) runExec(ctx context.Context, jail string, argv []string, o execOpts) error {
	if o.shellPath != "" && (!strings.HasPrefix(o.shellPath, "/") || strings.ContainsAny(o.shellPath, " \t\n")) {
		return usagef("the shell must be one absolute path inside the jail, not %q", o.shellPath)
	}
	if ok, err := a.dry("run a command in jail "+jail, map[string]any{"argv": argv, "shell": o.shell, "shell_path": o.shellPath}); ok || err != nil {
		return err
	}
	c, err := a.connect(ctx)
	if err != nil {
		return err
	}
	useTTY := !o.noTTY && a.env.IsTTY(a.env.Stdin) && a.env.IsTTY(a.env.Stdout)
	so := sdk.ExecOptions{TTY: useTTY, Shell: o.shell, ShellPath: o.shellPath, Env: o.env, User: o.user}
	if useTTY {
		if r, cl, ok := a.env.TermSize(a.env.Stdout); ok {
			so.Rows, so.Cols = r, cl
		}
	}
	sess, err := c.Jails().Exec(ctx, jail, argv, so)
	if err != nil {
		return err
	}
	defer sess.Close()
	if useTTY {
		restore, err := a.env.RawTerminal(a.env.Stdin)
		if err != nil {
			return err
		}
		defer restore()
		ch, stop := a.env.ResizeSignal()
		defer stop()
		go func() {
			for {
				select {
				case <-ch:
					if r, cl, ok := a.env.TermSize(a.env.Stdout); ok {
						_ = sess.Resize(r, cl)
					}
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	var in io.Reader = a.env.Stdin
	code, err := sess.Run(in, a.env.Stdout, a.env.Stderr)
	if err != nil {
		return err
	}
	if code != 0 {
		return &exitError{code: code}
	}
	return nil
}

// runConsole opens a console session (the HTTP protocol of the web console)
// and attaches the local terminal to it.
func (a *app) runConsole(ctx context.Context, jail string, o execOpts) error {
	if o.shellPath != "" && (!strings.HasPrefix(o.shellPath, "/") || strings.ContainsAny(o.shellPath, " \t\n")) {
		return usagef("the shell must be one absolute path inside the jail, not %q", o.shellPath)
	}
	if ok, err := a.dry("open a console in jail "+jail, map[string]any{"shell_path": o.shellPath}); ok || err != nil {
		return err
	}
	c, err := a.connect(ctx)
	if err != nil {
		return err
	}
	useTTY := a.env.IsTTY(a.env.Stdin) && a.env.IsTTY(a.env.Stdout)
	so := sdk.ExecOptions{Shell: true, ShellPath: o.shellPath, Env: o.env, User: o.user}
	if useTTY {
		if r, cl, ok := a.env.TermSize(a.env.Stdout); ok {
			so.Rows, so.Cols = r, cl
		}
	}
	sess, err := c.Jails().Console(ctx, jail, so)
	if err != nil {
		return err
	}
	defer sess.Close()
	if useTTY {
		restore, err := a.env.RawTerminal(a.env.Stdin)
		if err != nil {
			return err
		}
		defer restore()
		ch, stop := a.env.ResizeSignal()
		defer stop()
		go func() {
			for {
				select {
				case <-ch:
					if r, cl, ok := a.env.TermSize(a.env.Stdout); ok {
						_ = sess.Resize(r, cl)
					}
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	var in io.Reader = a.env.Stdin
	code, err := sess.Run(in, a.env.Stdout)
	if err != nil {
		return err
	}
	if code != 0 {
		return &exitError{code: code}
	}
	return nil
}

func (a *app) jailExec(d *kindDesc[api.Jail]) *cobra.Command {
	var o execOpts
	cmd := &cobra.Command{
		Use:   "exec NAME -- COMMAND [ARG...]",
		Short: "Run a command in a jail",
		Long: `Run a command inside a running jail and stream its input and output over one
connection. The command is run as given (never through a shell), and the exit
status of jailops is the exit status of the command. It works locally and
through a hub (--host) under the same trust and project rules, needs no SSH
server in the jail, and is audited.

With a terminal on both ends the command gets a pseudo-terminal: the local
terminal is switched to raw mode for the session and window size changes are
forwarded. -T runs without one (what a pipe gets anyway); -t names the default.`,
		Example: `jailops exec nginx -- ls -l /var/log
jailops exec nginx -- top
jailops exec -T nginx -- cat /var/log/messages > messages
jailops exec --user www nginx -- id
jailops --host host-b exec nginx -- service nginx status`,
		Args:              cobra.MinimumNArgs(2),
		ValidArgsFunction: completeArgs(completeKind(a, d), completeFiles),
		RunE: func(cmd *cobra.Command, args []string) error {
			argv := args[1:]
			if len(argv) > 0 && argv[0] == "--" { // kept by cobra after the name
				argv = argv[1:]
			}
			if len(argv) == 0 {
				return usagef("name the command to run after --")
			}
			return a.runExec(cmd.Context(), args[0], argv, o)
		},
	}
	var tty bool
	cmd.Flags().SetInterspersed(false)
	cmd.Flags().BoolVarP(&tty, "tty", "t", false, "allocate a pseudo-terminal when stdin and stdout are terminals (the default)")
	cmd.Flags().BoolVarP(&o.noTTY, "no-tty", "T", false, "never allocate a pseudo-terminal")
	cmd.Flags().StringArrayVarP(&o.env, "env", "e", nil, "add NAME=value to the environment (repeatable)")
	cmd.Flags().StringVarP(&o.user, "user", "u", "", "run as this user of the jail")
	return cmd
}

func (a *app) jailShell(d *kindDesc[api.Jail]) *cobra.Command {
	var o execOpts
	cmd := &cobra.Command{
		Use:   "shell NAME",
		Short: "Open a shell in a jail",
		Long: `Open a shell in a running jail with a terminal. It works locally and through a
hub (--host), with the same trust and project rules, and needs no SSH server
in the jail.

The shell is chosen in this order: --shell, then the jail's configured "shell"
key (a path inside the jail, not necessarily the login shell of /etc/passwd;
set it with 'jailops set NAME shell /usr/local/bin/bash'), then /bin/sh. If the
chosen shell does not exist in the jail the command fails with a clear error;
it never falls back to another shell.

Without a terminal on stdin and stdout the shell runs without a pseudo-terminal,
which is what a pipe such as 'echo id | jailops shell web' needs.`,
		Example:           "jailops shell web\njailops shell web --shell /usr/local/bin/bash\njailops --host host-b shell web",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKind(a, d),
		RunE: func(cmd *cobra.Command, args []string) error {
			o.shell = true
			return a.runExec(cmd.Context(), args[0], nil, o)
		},
	}
	cmd.Flags().StringVar(&o.shellPath, "shell", "", "shell to run, a path inside the jail (default: the jail's shell key, else /bin/sh)")
	cmd.Flags().StringVarP(&o.user, "user", "u", "", "run as this user of the jail")
	return cmd
}
