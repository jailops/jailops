package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"jailops.net/jailops/internal/recipe"
	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

// recipeFlags are the inputs of the variables of a recipe.
type recipeFlags struct {
	set, setFile []string
	vars         string
}

// values reads the flags into the values of the variables (internal/recipe). A file of --set-file and --vars is read here:
// the content of a secret goes from the file into the request and nowhere else.
func (f recipeFlags) values() (recipe.Values, error) {
	var v recipe.Values
	pairs := func(flag string, in []string) (map[string]string, error) {
		out := map[string]string{}
		for _, kv := range in {
			k, val, ok := strings.Cut(kv, "=")
			if !ok || k == "" {
				return nil, usagef("%s %q: want NAME=VALUE", flag, strings.SplitN(kv, "=", 2)[0])
			}
			if _, dup := out[k]; dup {
				return nil, usagef("%s %s: given twice", flag, k)
			}
			out[k] = val
		}
		return out, nil
	}
	var err error
	if v.Set, err = pairs("--set", f.set); err != nil {
		return v, err
	}
	paths, err := pairs("--set-file", f.setFile)
	if err != nil {
		return v, err
	}
	if len(paths) > 0 {
		v.SetFile = map[string]string{}
	}
	for k, p := range paths {
		if v.SetFile[k], err = recipe.ReadValueFile(p); err != nil {
			return v, fmt.Errorf("--set-file %s: %w", k, err)
		}
	}
	if f.vars != "" {
		b, err := os.ReadFile(f.vars)
		if err != nil {
			return v, err
		}
		if v.Vars, err = recipe.ParseVarsFile(b); err != nil {
			return v, usagef("--vars %s: %v", f.vars, err)
		}
	}
	return v, nil
}

// loadRecipe renders a recipe file (a directory means DIR/recipe.yaml; "-" is standard input) with the variables, reads
// the files beside it and checks the result with the validator of the daemon. Nothing is sent anywhere.
func (a *app) loadRecipe(file string, f recipeFlags) (*recipe.Loaded, error) {
	v, err := f.values()
	if err != nil {
		return nil, err
	}
	var l *recipe.Loaded
	if file == "-" {
		b, rerr := a.readInput(file)
		if rerr != nil {
			return nil, rerr
		}
		l, err = recipe.LoadBytes(b, v)
	} else {
		l, err = recipe.LoadPath(file, v)
	}
	if err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			return nil, err // the file is missing or unreadable: not a mistake in the recipe
		}
		return nil, usagef("%v", err)
	}
	return l, l.Validate()
}

func (a *app) jailApply(d *kindDesc[api.Jail]) *cobra.Command {
	var file string
	var rf recipeFlags
	var validateOnly bool
	cmd := &cobra.Command{
		Use:   "apply [NAME | -l KEY=VAL | --all] -f RECIPE [flags]",
		Short: "Run a recipe in a running jail: packages, files, rc variables, services, commands",
		Long: `Run a recipe in a running FreeBSD jail. A recipe is a YAML file with a list of steps that run in the
order of the list; -f DIR means DIR/recipe.yaml. Nothing runs by itself: you run it when you want, as often as
you want. Every step looks at what the jail has and changes only what differs, so a second run of the same
recipe changes nothing, and the report says so. The jail is never stopped, restarted or changed in its
specification.

  requirements:                            # optional: checked in the jail, before the first step
    - check: [test, -x, /usr/sbin/sshd]    # argv run in the jail; exit status 0 means met
      message: sshd is not installed in the jail   # shown when it is not met
  steps:
    - package: [nginx]                     # installs only what is missing
    - file:                                # a file beside the recipe, or inline text
        path: /usr/local/etc/nginx/nginx.conf
        source: nginx.conf                 # content: | ... for text in the recipe
        mode: "0644"                       # owner and group are names or numbers; mode defaults to 0644
    - dir: {path: /var/db/site, mode: "0750", owner: www}   # one directory, never recursive
    - sysrc: {nginx_enable: "YES"}         # rc.conf variables (in: periodic.conf for that file)
    - service: {name: nginx, enabled: true, running: true}   # starts when not running, never restarts
    - run: [/usr/local/sbin/nginx, -t]     # argv, never a shell; the login PATH of the jail
    - run: [pw, useradd, deploy, -m]
      unless: [pw, usershow, deploy]       # skipped when this exits 0 (creates: PATH skips when it exists)

A step is reported changed, unchanged, skipped, or failed with the last lines of its output; a run without
creates or unless is reported executed, because JailOps cannot know whether it changed anything. The first
failed step stops the run and the steps before it stay: fix the recipe and run it again. A file is text
of at most 1 MiB (8 MiB for the whole recipe), written whole with its final mode and owner; files under /etc and
the other base directories are refused, local configuration goes to /usr/local/etc (the exceptions are
sysctl.conf, periodic.conf, ssh/sshd_config and newsyslog.conf, and rc variables go through sysrc and service).
sysrc edits the file from the host: the jail needs no sysrc tool.

Requirements are checked first, all of them, before any step: when one is not met no step runs, nothing is
changed, and the error says which requirement and the exit status of its check. JailOps does not know what a
check asks; what can be fixed inside the jail is fixed by an ordinary step of the recipe, a change of the jail's
parameters or of the host is for the administrator.

--dry-run reads the state (the package list, the files, rc.conf, service status) and reports what would change.
It runs none of your commands, installs nothing and writes nothing: a run with unless is reported as 'would
check', a run without a guard as 'would execute', a requirement as 'not checked'.

Variables make one recipe serve many jails. A recipe declares them at the top (vars:, each with default, required,
secret or split) and uses ${name} in the values of its steps and in files that say template: true; $${ is a literal
${. The values come from --set NAME=VALUE, from --set-file NAME=FILE (the text of a file: the way to give a secret or
a value of several lines) and from --vars FILE (lines NAME=VALUE; names the recipe does not declare are ignored);
--set beats --vars beats the default. A secret is never given on the command line. --validate renders the recipe
and checks it, with no jail and no connection to the daemon; NAME is not needed then.

One jail is named. Several are selected by label with -l, or all of the project with --all. Recipes are for FreeBSD
jails: a Linux jail that is named is refused, with no requirement and no step run, and a Linux jail is not part of a
selection. The recipe is rendered once, and each jail is done on its own, one after the other, with one block of
result per jail: a failure is reported for that jail and the run goes on with the others, and the exit code is
non-zero if any jail failed. --dry-run and --validate work with a selection as with one jail.`,
		Example: `jailops jail apply web -f web/recipe.yaml --dry-run
jailops jail apply web -f web/
jailops jail apply db -f small-postgresql-server/ --vars db.vars --set-file db_password=app.password
jailops jail apply -f web/ --set site=example.test --validate
jailops jail apply --all -f hardening/ --dry-run
jailops jail apply -l tier=web -f web/ --set-file tls_key=site.key`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeKind(a, d),
	}
	resolve := a.jailTargetsOS(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if file == "" {
			return usagef("name the recipe: -f FILE")
		}
		l, err := a.loadRecipe(file, rf)
		if err != nil {
			return err
		}
		if validateOnly {
			// nothing is sent: the recipe was rendered and checked here
			res := api.RecipeValidateResult{Valid: true, Steps: len(l.Recipe.Steps), Requirements: len(l.Recipe.Requirements)}
			if a.format != fmtTable {
				return a.emit(res, nil, nil, func() []string { return nil })
			}
			fprintf(a.env.Stdout, "recipe is valid: %d steps", res.Steps)
			if res.Requirements > 0 {
				fprintf(a.env.Stdout, ", %d requirements (run in the jail when the recipe is applied)", res.Requirements)
			}
			fprintf(a.env.Stdout, "\n")
			return nil
		}
		selected := cmd.Flags().Changed("all") || cmd.Flags().Changed("selector")
		if selected && len(args) > 0 {
			return usagef("name one jail or select jails with -l or --all, not both")
		}
		if !selected && len(args) != 1 {
			return usagef("name the jail: jailops jail apply NAME -f RECIPE, or select jails with -l key=value or --all (--validate needs no jail)")
		}
		names, oses := args, map[string]api.OS(nil)
		if selected {
			var err error
			if names, oses, _, err = resolve(cmd.Context(), nil); err != nil {
				return err
			}
			if names, err = a.freebsdJails(cmd.Context(), names, oses, true); err != nil {
				return err
			}
			if len(names) == 0 {
				fprintln(a.env.Stderr, "No jails match.")
				return nil
			}
		}
		c, err := a.connect(cmd.Context())
		if err != nil {
			return err
		}
		// the recipe was rendered once, above: the same request goes to every jail
		req := l.Request(a.dryRun)
		failed := 0
		for _, name := range names {
			err := a.applyRecipe(cmd.Context(), c, name, req, selected)
			if !selected {
				return err
			}
			if err != nil {
				failed++
				fprintf(a.env.Stderr, "jail/%s: failed: %s\n", name, firstLine(err.Error()))
			}
		}
		if failed > 0 {
			return &exitError{code: ExitFailure}
		}
		return nil
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "the recipe: a YAML file, a directory with recipe.yaml, or - for standard input")
	cmd.Flags().StringArrayVar(&rf.set, "set", nil, "a value of a variable: NAME=VALUE (one line; not for a secret), repeatable")
	cmd.Flags().StringArrayVar(&rf.setFile, "set-file", nil, "a value of a variable from a file: NAME=FILE (several lines, or a secret), repeatable")
	cmd.Flags().StringVar(&rf.vars, "vars", "", "a file of lines NAME=VALUE for the variables; names the recipe does not declare are ignored")
	cmd.Flags().BoolVar(&validateOnly, "validate", false, "render and check the recipe here: no jail, no connection to the daemon")
	return cmd
}

// applyRecipe runs the rendered recipe in one jail and says what the daemon did.
func (a *app) applyRecipe(ctx context.Context, c *sdk.Client, name string, req api.ApplyRequest, many bool) error {
	if !a.dryRun {
		// a jail that jailops-sync keeps (its labels say so) is put back by the next sync: the person should know
		if j, err := c.Jails().Get(ctx, name); err == nil {
			for k := range j.Metadata.Labels {
				if strings.HasPrefix(k, "sync.jailops.net/") {
					fprintf(a.env.Stderr, "warning: jail/%s is managed by sync, the next sync may revert this\n", name)
					break
				}
			}
		}
	}
	// the global --dry-run is the dry run of the daemon here: it reads the jail and changes nothing
	h, err := handle(c.Jails().Apply(ctx, name, req))
	if err != nil {
		return err
	}
	if !a.waiting() {
		return a.finishOp(ctx, h, "jail", name, "applying the recipe", nil)
	}
	v, _, err := a.await(ctx, h)
	if err != nil {
		return err
	}
	res, _ := v.(api.ApplyResult)
	if a.format != fmtTable {
		return a.emit(res, nil, nil, func() []string { return []string{name} })
	}
	switch {
	case res.DryRun:
		fprintf(a.env.Stdout, "jail/%s: dry run, nothing was changed\n", name)
	case many:
		fprintf(a.env.Stdout, "jail/%s:\n", name)
	}
	for _, l := range res.Lines() {
		fprintf(a.env.Stdout, "  %s\n", l)
	}
	return nil
}
