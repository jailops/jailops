package cli

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"jailops.net/jailops/internal/validate"
	"jailops.net/jailops/sdk/api"
)

// loadRecipe reads a recipe file (a directory means DIR/recipe.yaml; "-" is standard input) and fills the content of
// every file step that names a source: the sources are files beside the recipe, read here, never by the daemon.
func (a *app) loadRecipe(file string) (api.Recipe, error) {
	var r api.Recipe
	base := "."
	if file != "-" {
		if fi, err := os.Stat(file); err == nil && fi.IsDir() {
			file = filepath.Join(file, "recipe.yaml")
		}
		base = filepath.Dir(file)
	}
	b, err := a.readInput(file)
	if err != nil {
		return r, err
	}
	docs, err := readDocs(b)
	if err != nil {
		return r, err
	}
	if len(docs) != 1 {
		return r, usagef("a recipe is one document, %s has %d", file, len(docs))
	}
	if err := decodeStrict(docs[0], &r); err != nil {
		return r, err
	}
	for i := range r.Steps {
		f := r.Steps[i].File
		if f == nil || f.Source == "" {
			continue
		}
		fld := fmt.Sprintf("steps[%d].file", i)
		if f.Content != nil {
			return r, usagef("%s: give a source or content, not both", fld)
		}
		if path.IsAbs(f.Source) || path.Clean(f.Source) != f.Source || f.Source == ".." || len(f.Source) > 2 && f.Source[:3] == "../" {
			return r, usagef("%s.source: %q must be a path below the directory of the recipe", fld, f.Source)
		}
		p := filepath.Join(base, filepath.FromSlash(f.Source))
		if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() || fi.Size() > validate.RecipeFileMax {
			return r, usagef("%s.source: %s is not a regular file of at most %d MiB", fld, p, validate.RecipeFileMax>>20)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return r, err
		}
		if !utf8.Valid(data) {
			return r, usagef("%s.source: %s is not text; copy other files with 'jailops cp'", fld, p)
		}
		text := string(data)
		f.Content, f.Source = &text, ""
	}
	return r, validate.Recipe(r)
}

func (a *app) jailApply(d *kindDesc[api.Jail]) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "apply NAME -f RECIPE [--dry-run]",
		Short: "Run a recipe in a running jail: packages, files, rc variables, services, commands",
		Long: `Run a recipe in a running FreeBSD jail. A recipe is a YAML file with a list of steps that run in the
order of the list; -f DIR means DIR/recipe.yaml. Nothing runs by itself: you run it when you want, as often as
you want. Every step looks at what the jail has and changes only what differs, so a second run of the same
recipe changes nothing, and the report says so. The jail is never stopped, restarted or changed in its
specification.

  steps:
    - package: [nginx]                     # installs only what is missing
    - file:                                # a file beside the recipe, or inline text
        path: /usr/local/etc/nginx/nginx.conf
        source: nginx.conf                 # content: | ... for text in the recipe
        mode: "0644"                       # owner and group are names or numbers; mode defaults to 0644
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

--dry-run reads the state (the package list, the files, rc.conf, service status) and reports what would change.
It runs none of your commands, installs nothing and writes nothing: a run with unless is reported as 'would
check', a run without a guard as 'would execute'.`,
		Example: `jailops jail apply web -f web/recipe.yaml --dry-run
jailops jail apply web -f web/
jailops jail apply web -f web/recipe.yaml`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKind(a, d),
		RunE: func(cmd *cobra.Command, args []string) error {
			if file == "" {
				return usagef("name the recipe: -f FILE")
			}
			r, err := a.loadRecipe(file)
			if err != nil {
				return err
			}
			c, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			// the global --dry-run is the dry run of the daemon here: it reads the jail and changes nothing
			h, err := handle(c.Jails().Apply(cmd.Context(), args[0], api.ApplyRequest{Recipe: r, DryRun: a.dryRun}))
			if err != nil {
				return err
			}
			if !a.waiting() {
				return a.finishOp(cmd.Context(), h, "jail", args[0], "applying the recipe", nil)
			}
			v, _, err := a.await(cmd.Context(), h)
			if err != nil {
				return err
			}
			res, _ := v.(api.ApplyResult)
			if a.format != fmtTable {
				return a.emit(res, nil, nil, func() []string { return []string{args[0]} })
			}
			if res.DryRun {
				fprintf(a.env.Stdout, "jail/%s: dry run, nothing was changed\n", args[0])
			}
			for _, l := range res.Lines() {
				fprintf(a.env.Stdout, "  %s\n", l)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "the recipe: a YAML file, a directory with recipe.yaml, or - for standard input")
	return cmd
}
