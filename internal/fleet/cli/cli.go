// Package cli is the command line of jailops-sync. cmd/jailops-sync only calls Run.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"jailops.net/jailops/internal/fleet/applylog"
	"jailops.net/jailops/internal/fleet/config"
	"jailops.net/jailops/internal/fleet/plan"
	"jailops.net/jailops/internal/fleet/secrets"
)

// Environment variables.
const (
	EnvFleetPath = "JAILOPS_SYNC_FLEET_PATH"
	EnvStateDir  = "JAILOPS_SYNC_STATE_DIR"
)

// Env is everything the commands take from the process, so tests can run the
// real commands against in-memory servers.
type Env struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	Getenv         func(string) string
	Version        string
	// NewConnector builds the connector of a command run. Nil means the SDK
	// connector (https, local socket or hub).
	NewConnector func(*config.Fleet, *secrets.Store) plan.Connector
}

// ExitError carries a process exit code. An empty message prints nothing.
type ExitError struct {
	Code int
	Msg  string
}

func (e *ExitError) Error() string { return e.Msg }

// Exit codes.
const (
	ExitOK      = 0
	ExitFailure = 1
	ExitFatal   = 2
)

type app struct {
	env       Env
	fleetPath string
	stateDir  string
	identity  string
	red       *secrets.Redactor
	out, err  io.Writer
	started   bool // a command body began: an error before that is a usage error
}

func (e Env) getenv(k string) string {
	if e.Getenv != nil {
		return e.Getenv(k)
	}
	return os.Getenv(k)
}

// Run executes jailops-sync with args (without the program name) and returns
// the process exit code.
func Run(env Env, args []string) int {
	if env.Stdout == nil {
		env.Stdout = io.Discard
	}
	if env.Stderr == nil {
		env.Stderr = io.Discard
	}
	if env.Stdin == nil {
		env.Stdin = strings.NewReader("")
	}
	a := &app{env: env, red: secrets.NewRedactor()}
	a.out, a.err = a.red.Writer(env.Stdout), a.red.Writer(env.Stderr)
	if len(args) > 0 && args[0] == "--version" {
		args = append([]string{"version"}, args[1:]...)
	}
	root := a.root()
	root.SetArgs(args)
	root.SetOut(a.out)
	root.SetErr(a.err)
	root.SetIn(env.Stdin)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := root.ExecuteContext(ctx)
	if err == nil {
		return ExitOK
	}
	var ee *ExitError
	if errors.As(err, &ee) {
		if ee.Msg != "" {
			fmt.Fprintln(a.err, "Error:", ee.Msg)
		}
		return ee.Code
	}
	fmt.Fprintln(a.err, "Error:", err)
	if !a.started {
		// an unknown command or flag, or the wrong arguments: the check itself failed, nothing was done
		fmt.Fprintln(a.err, "run 'jailops-sync --help' for usage")
		return ExitFatal
	}
	return ExitFailure
}

func (a *app) root() *cobra.Command {
	root := &cobra.Command{
		Use:   "jailops-sync",
		Short: "Declarative fleet sync for JailOps",
		Long: `jailops-sync applies a git repository of YAML to a fleet of JailOps hosts
through the API. It plans before it applies, writes only objects it owns
(managed_by: sync:<fleet-id>), and never deletes anything it does not own.

The fleet repository is resolved from --fleet-path, $` + EnvFleetPath + `, or the
current directory. The age identity that opens secrets/*.age comes from --identity,
$` + secrets.EnvIdentityFile + ` (a file) or $` + secrets.EnvKey + `. The SOPS files
(shared/secrets.sops.yaml, hosts/<host>/remote.sops.yaml) open with the key SOPS
reads from SOPS_AGE_KEY, SOPS_AGE_KEY_FILE or SOPS_AGE_KEY_CMD.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(*cobra.Command, []string) {
			a.started = true
		},
	}
	pf := root.PersistentFlags()
	pf.StringVarP(&a.fleetPath, "fleet-path", "C", "", "fleet repository (default $"+EnvFleetPath+" or the current directory)")
	pf.StringVar(&a.stateDir, "state-dir", "", "state directory for apply logs, locks and rollouts (default $"+EnvStateDir+" or the XDG state dir)")
	pf.StringVar(&a.identity, "identity", "", "age identity file (default $"+secrets.EnvIdentityFile+" or $"+secrets.EnvKey+")")
	_ = root.RegisterFlagCompletionFunc("fleet-path", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return nil, cobra.ShellCompDirectiveFilterDirs
	})
	_ = root.RegisterFlagCompletionFunc("state-dir", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return nil, cobra.ShellCompDirectiveFilterDirs
	})
	root.AddGroup(
		&cobra.Group{ID: "read", Title: "Read-only:"},
		&cobra.Group{ID: "write", Title: "Changes hosts:"},
		&cobra.Group{ID: "setup", Title: "Setup:"},
	)
	add := func(group string, cmds ...*cobra.Command) {
		for _, c := range cmds {
			c.GroupID = group
			root.AddCommand(c)
		}
	}
	add("setup", a.cmdDoctor(), a.cmdValidate(), a.cmdSecrets(), a.cmdVault(), a.cmdRemote())
	add("read", a.cmdRender(), a.cmdPlan(), a.cmdDrift(), a.cmdPruneCheck(), a.cmdAdopt())
	add("write", a.cmdSync(), a.cmdRollout(), a.cmdWatch())
	root.AddCommand(a.cmdVersion(), a.cmdMan())
	return root
}

func (a *app) path() (string, error) {
	p := a.fleetPath
	if p == "" {
		p = a.env.getenv(EnvFleetPath)
	}
	if p == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		p = wd
	}
	return p, nil
}

func (a *app) state() string {
	if a.stateDir != "" {
		return applylog.StateDir(a.stateDir)
	}
	if v := a.env.getenv(EnvStateDir); v != "" {
		return v
	}
	return applylog.StateDir("")
}

// load reads and checks the fleet. Check errors are returned with every problem.
func (a *app) load() (*config.Fleet, error) {
	p, err := a.path()
	if err != nil {
		return nil, err
	}
	f, err := config.Load(p)
	if err != nil {
		return nil, err
	}
	if err := f.Check(); err != nil {
		return f, fmt.Errorf("the fleet repository is invalid:\n%w", err)
	}
	return f, nil
}

func (a *app) store(f *config.Fleet) *secrets.Store {
	return secrets.NewStore(filepath.Join(f.Path, "secrets"), secrets.Source{File: a.identity, Getenv: a.env.Getenv}, a.red).WithSops(f.Path, a.env.Getenv)
}

func (a *app) session(f *config.Fleet) *plan.Session {
	st := a.store(f)
	var conn plan.Connector
	if a.env.NewConnector != nil {
		conn = a.env.NewConnector(f, st)
	} else {
		conn = &plan.SDKConnector{Fleet: f, Store: st, Getenv: a.env.Getenv}
	}
	return plan.NewSession(f, conn, st)
}

// hosts resolves --host values (names or @groups); empty means every host.
func hostsOf(f *config.Fleet, refs []string) ([]string, error) {
	if len(refs) == 0 {
		return f.HostNames(), nil
	}
	return f.ExpandHosts(refs)
}

func (a *app) printWarnings(f *config.Fleet) {
	for _, w := range f.Warnings {
		fmt.Fprintln(a.err, "warning:", w)
	}
}

// printPlan writes the plan grouped by host. Unchanged objects are shown only with all.
func (a *app) printPlan(w io.Writer, p *plan.Plan, all bool) {
	byHost := map[string][]plan.Item{}
	for _, it := range p.Items {
		byHost[it.Host] = append(byHost[it.Host], it)
	}
	for _, h := range p.Hosts {
		fmt.Fprintf(w, "host %s\n", h)
		shown := 0
		for _, it := range byHost[h] {
			if it.Action == plan.ActionMatch && !all {
				continue
			}
			shown++
			fmt.Fprintf(w, "  %-9s %s\n", it.Action, it.Ref())
			for _, d := range it.Details {
				fmt.Fprintf(w, "              %s\n", d)
			}
			for _, d := range it.Dangers {
				fmt.Fprintf(w, "              DANGER: %s\n", d)
			}
		}
		for _, he := range p.Errors {
			if he.Host == h {
				fmt.Fprintf(w, "  ERROR     %v\n", he.Err)
				shown++
			}
		}
		if shown == 0 {
			fmt.Fprintln(w, "  in sync")
		}
	}
	if len(p.ProjectHosts) > 0 {
		fmt.Fprintln(w, "\nprojects")
		last := ""
		for _, ph := range p.ProjectHosts {
			if ph.Project != last {
				fmt.Fprintf(w, "  %s\n", ph.Project)
				last = ph.Project
			}
			fmt.Fprintf(w, "    %-12s %s\n", ph.Host, ph.State)
		}
	}
	fmt.Fprintf(w, "\n%s\n", p.Summary())
}

func sortedStrings(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (a *app) cmdVersion() *cobra.Command {
	return &cobra.Command{
		Use: "version", Short: "Print the version", Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			v := a.env.Version
			if v == "" {
				v = "dev"
			}
			fmt.Fprintln(cmd.OutOrStdout(), "jailops-sync", v)
		},
	}
}
