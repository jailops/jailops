package jailos

import (
	"context"
	"sort"
	"strings"
	"time"

	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/sdk/api"
)

// Command implements Jails over run.Runner (jail, jexec, jls).
type Command struct {
	R run.Runner
	// Timeouts; zero values use these defaults.
	StartTimeout time.Duration // default 120s (jail -c may run exec.start)
}

var _ Jails = (*Command)(nil)

// NewCommand returns a run-based implementation.
func NewCommand(r run.Runner) *Command { return &Command{R: r} }

func mapJailErr(name string, err error) error {
	if err == nil {
		return nil
	}
	ee, ok := run.IsExit(err)
	if !ok {
		return api.Errorf(api.CodeInternal, "jail: %v", err)
	}
	low := strings.ToLower(ee.Stderr)
	msg := strings.TrimSpace(ee.Stderr)
	switch {
	case strings.Contains(low, "not found"), strings.Contains(low, "no such jail"):
		return api.Errorf(api.CodeNotFound, "jail %s: %s", name, msg)
	case strings.Contains(low, "already exists"):
		return api.Errorf(api.CodeAlreadyExists, "jail %s: %s", name, msg)
	}
	return api.Errorf(api.CodeInternal, "jail %s: %s", name, msg)
}

// Create implements Controller.
func (c *Command) Create(ctx context.Context, confPath, name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if err := validateConf(confPath); err != nil {
		return err
	}
	t := c.StartTimeout
	if t == 0 {
		t = 120 * time.Second
	}
	_, err := c.R.Run(ctx, run.Cmd{Name: "jail", Args: []string{"-f", confPath, "-c", name}, Timeout: t})
	return mapJailErr(name, err)
}

// Remove implements Controller.
func (c *Command) Remove(ctx context.Context, confPath, name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if err := validateConf(confPath); err != nil {
		return err
	}
	_, err := c.R.Run(ctx, run.Cmd{Name: "jail", Args: []string{"-f", confPath, "-r", name}, Timeout: 120 * time.Second})
	return mapJailErr(name, err)
}

// Exec implements Controller.
func (c *Command) Exec(ctx context.Context, name string, o ExecOpts) (run.Result, error) {
	if err := ValidateName(name); err != nil {
		return run.Result{}, err
	}
	if err := validateUser(o.User); err != nil {
		return run.Result{}, err
	}
	if len(o.Argv) == 0 || o.Argv[0] == "" || o.Argv[0][0] == '-' {
		return run.Result{}, api.Errorf(api.CodeInvalid, "exec needs a command")
	}
	args := []string{}
	if o.Login {
		args = append(args, "-l")
	}
	if o.User != "" {
		args = append(args, "-U", o.User)
	}
	args = append(args, name)
	args = append(args, o.Argv...)
	res, err := c.R.Run(ctx, run.Cmd{
		Name: "jexec", Args: args, Stdin: o.Stdin, Stdout: o.Stdout,
		Timeout: o.Timeout,
	})
	if err != nil {
		if ee, ok := run.IsExit(err); ok {
			// The command's own failure is the caller's business; keep the result.
			return res, ee
		}
		return res, api.Errorf(api.CodeInternal, "jexec: %v", err)
	}
	return res, nil
}

// List implements Observer. It asks jls for libxo JSON and falls back to
// the name=value format if that fails.
func (c *Command) List(ctx context.Context) ([]Jail, error) {
	res, err := c.R.Run(ctx, run.Cmd{Name: "jls", Args: []string{"-v", "--libxo=json"}, Timeout: 15 * time.Second})
	if err == nil {
		if js, perr := ParseJLSJSON(res.Stdout); perr == nil {
			sort.Slice(js, func(i, j int) bool { return js[i].JID < js[j].JID })
			return js, nil
		}
	}
	res, err = c.R.Run(ctx, run.Cmd{Name: "jls", Args: []string{"-n", "jid", "name", "path", "host.hostname", "vnet", "dying"}, Timeout: 15 * time.Second})
	if err != nil {
		return nil, mapJailErr("", err)
	}
	js := ParseJLSNamed(string(res.Stdout))
	sort.Slice(js, func(i, j int) bool { return js[i].JID < js[j].JID })
	return js, nil
}

// Get implements Observer.
func (c *Command) Get(ctx context.Context, name string) (Jail, bool, error) {
	if err := ValidateName(name); err != nil {
		return Jail{}, false, err
	}
	js, err := c.List(ctx)
	if err != nil {
		return Jail{}, false, err
	}
	for _, j := range js {
		if j.Name == name {
			return j, true, nil
		}
	}
	return Jail{}, false, nil
}
