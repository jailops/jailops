package cli

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

// kindDesc describes one resource kind to the generic list, show, create,
// edit and delete commands.
type kindDesc[T any] struct {
	noun, plural string
	kind         api.Kind
	meta         func(*T) *api.ObjectMeta
	specOf       func(*T) any
	// parse builds an object from a document (envelope or bare spec).
	parse func(meta api.ObjectMeta, doc map[string]any, name string) (T, error)
	// setSpec copies the editable parts of src into dst.
	setSpec func(dst *T, src T)

	list   func(ctx context.Context, c *sdk.Client, opts ...sdk.ListOption) iter.Seq2[T, error]
	get    func(ctx context.Context, c *sdk.Client, name string) (T, string, error)
	update func(ctx context.Context, c *sdk.Client, name string, v T, etag string) (T, error)
	// create returns a handle when the server answered asynchronously.
	create func(ctx context.Context, c *sdk.Client, v T) (opHandle, T, error)
	del    func(ctx context.Context, c *sdk.Client, name string) (opHandle, error)

	head  []string
	row   func(now time.Time, x T) []string
	style styleFn
	// show prints the human form of one object; nil prints YAML.
	show func(a *app, x T) error
	// filters registers extra list flags and returns the predicate that uses
	// them (evaluated on the client after the flags are parsed).
	filters func(cmd *cobra.Command) func(x T) bool
}

func (d *kindDesc[T]) name(x T) string { return d.meta(&x).Name }

// newDesc builds the parts of a descriptor that follow from the types.
func newDesc[T, S any](noun, plural string, kind api.Kind, meta func(*T) *api.ObjectMeta, spec func(*T) *S) kindDesc[T] {
	d := kindDesc[T]{noun: noun, plural: plural, kind: kind, meta: meta}
	d.specOf = func(x *T) any { return spec(x) }
	d.parse = func(m api.ObjectMeta, doc map[string]any, name string) (T, error) {
		var x T
		pm, ps, err := parseObject[S](doc, kind, name)
		if err != nil {
			return x, err
		}
		_ = m
		*meta(&x) = pm
		*spec(&x) = ps
		return x, nil
	}
	d.setSpec = func(dst *T, src T) {
		*spec(dst) = *spec(&src)
		meta(dst).Labels = meta(&src).Labels
	}
	return d
}

// fromResource wires the client calls of a plain Resource.
func fromResource[T any](d *kindDesc[T], r func(*sdk.Client) *sdk.Resource[T]) {
	d.list = func(ctx context.Context, c *sdk.Client, o ...sdk.ListOption) iter.Seq2[T, error] {
		return r(c).List(ctx, o...)
	}
	d.get = func(ctx context.Context, c *sdk.Client, n string) (T, string, error) { return r(c).GetETag(ctx, n) }
	d.update = func(ctx context.Context, c *sdk.Client, n string, v T, etag string) (T, error) {
		return r(c).Update(ctx, n, v, etag)
	}
	d.create = func(ctx context.Context, c *sdk.Client, v T) (opHandle, T, error) {
		out, err := r(c).Create(ctx, v)
		return nil, out, err
	}
	d.del = func(ctx context.Context, c *sdk.Client, n string) (opHandle, error) {
		return nil, r(c).Delete(ctx, n)
	}
}

func collect[T any](seq iter.Seq2[T, error]) ([]T, error) {
	var out []T
	for v, err := range seq {
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func labelOpts(labels []string) ([]sdk.ListOption, error) {
	var out []sdk.ListOption
	for _, l := range labels {
		k, v, ok := strings.Cut(l, "=")
		if !ok || k == "" {
			return nil, usagef("label %q must be key=value", l)
		}
		out = append(out, sdk.Label(k, v))
	}
	return out, nil
}

func (a *app) completeNames(list func(ctx context.Context, c *sdk.Client) ([]string, error)) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		c, err := a.connect(cmd.Context())
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		names, err := list(cmd.Context(), c)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return names, cobra.ShellCompDirectiveNoFileComp
	}
}

func completeKind[T any](a *app, d *kindDesc[T]) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return a.completeNames(func(ctx context.Context, c *sdk.Client) ([]string, error) {
		items, err := collect(d.list(ctx, c))
		var n []string
		for _, it := range items {
			n = append(n, d.name(it))
		}
		return n, err
	})
}

// addList adds "list".
func addList[T any](a *app, parent *cobra.Command, d *kindDesc[T], extraExample string) {
	var labels []string
	var keep func(T) bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List " + d.plural,
		Long:    "List " + d.plural + " known to the server, optionally filtered by label.\nThe default output is a table; -o json and -o yaml print the full objects, -o name the names only.",
		Example: "jailops " + "@P@" + " list\njailops " + "@P@" + " list -l env=prod -o name" + extraExample,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			opts, err := labelOpts(labels)
			if err != nil {
				return err
			}
			items, err := collect(d.list(cmd.Context(), c, opts...))
			if err != nil {
				return err
			}
			if keep != nil {
				kept := items[:0]
				for _, it := range items {
					if keep(it) {
						kept = append(kept, it)
					}
				}
				items = kept
			}
			return a.emit(nonNil(items), d.style, func() ([]string, [][]string) {
				rows := make([][]string, len(items))
				for i, it := range items {
					rows[i] = d.row(a.env.Now(), it)
				}
				return d.head, rows
			}, func() []string {
				var n []string
				for _, it := range items {
					n = append(n, d.name(it))
				}
				return n
			})
		},
	}
	cmd.Flags().StringArrayVarP(&labels, "label", "l", nil, "only objects with this label, key=value (repeatable)")
	if d.filters != nil {
		keep = d.filters(cmd)
	}
	parent.AddCommand(cmd)
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// addShow adds "show".
func addShow[T any](a *app, parent *cobra.Command, d *kindDesc[T]) {
	path := "@P@"
	parent.AddCommand(&cobra.Command{
		Use:               "show NAME",
		Aliases:           []string{"get"},
		Short:             "Show one " + d.noun,
		Long:              "Show one " + d.noun + " with its specification and, where the server reports it, its observed status.\n-o json and -o yaml print the complete object.",
		Example:           "jailops " + path + " show NAME\njailops " + path + " show NAME -o yaml",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKind(a, d),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			x, _, err := d.get(cmd.Context(), c, args[0])
			if err != nil {
				return err
			}
			if a.format == fmtTable && d.show != nil {
				return d.show(a, x)
			}
			return a.emit(x, nil, nil, func() []string { return []string{d.name(x)} })
		},
	})
}

// addCreate adds "create". flagBuild registers flags and returns the builder
// used when no file is given; it may be nil when only -f is supported.
func addCreate[T any](a *app, parent *cobra.Command, d *kindDesc[T], long, example string, flagBuild func(*cobra.Command) func(name string) (T, error)) {
	var file string
	cmd := &cobra.Command{
		Use:   "create [NAME]",
		Short: "Create a " + d.noun,
		Long: long + `

With -f the object comes from a YAML or JSON file ("-" reads standard input).
The file may use the declarative envelope (apiVersion, kind, metadata, spec) or
be a bare spec, in which case NAME is required. A file may hold several
documents; each one is created in order.`,
		Example: example,
		Args:    cobra.MaximumNArgs(1),
	}
	var build func(string) (T, error)
	if flagBuild != nil {
		build = flagBuild(cmd)
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "create from this YAML or JSON file, or - for standard input")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		var objs []T
		nameArg := ""
		if len(args) == 1 {
			nameArg = args[0]
		}
		if file != "" {
			b, err := a.readInput(file)
			if err != nil {
				return err
			}
			docs, err := readDocs(b)
			if err != nil {
				return err
			}
			if nameArg != "" && len(docs) > 1 {
				return usagef("NAME cannot be combined with a file of several documents")
			}
			for _, doc := range docs {
				x, err := d.parse(api.ObjectMeta{}, doc, nameArg)
				if err != nil {
					return err
				}
				objs = append(objs, x)
			}
		} else {
			if nameArg == "" {
				return usagef("NAME is required (or use -f FILE)")
			}
			if build == nil {
				return usagef("a %s is created from a file: use -f FILE", d.noun)
			}
			x, err := build(nameArg)
			if err != nil {
				return err
			}
			objs = append(objs, x)
		}
		for _, x := range objs {
			name := d.name(x)
			if ok, err := a.dry("create "+d.noun+" "+name, document(d.kind, *d.meta(&x), d.specOf(&x))); ok || err != nil {
				if err != nil {
					return err
				}
				continue
			}
			c, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			h, out, err := d.create(cmd.Context(), c, x)
			if err != nil {
				return err
			}
			if err := a.finishOp(cmd.Context(), h, d.noun, name, "created", out); err != nil {
				return err
			}
		}
		return nil
	}
	parent.AddCommand(cmd)
}

// addDelete adds "delete".
func addDelete[T any](a *app, parent *cobra.Command, d *kindDesc[T]) {
	path := "@P@"
	parent.AddCommand(&cobra.Command{
		Use:               "delete NAME...",
		Aliases:           []string{"rm"},
		Short:             "Delete " + d.plural,
		Long:              "Delete one or more " + d.plural + ". The server refuses when another object still uses them.\nThe command asks for confirmation unless -y is given.",
		Example:           "jailops " + path + " delete NAME\njailops " + path + " delete -y NAME1 NAME2",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeKind(a, d),
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, name := range args {
				if ok, err := a.dry("delete "+d.noun+" "+name, nil); ok || err != nil {
					if err != nil {
						return err
					}
					continue
				}
				if err := a.confirm(fmt.Sprintf("Delete %s %q?", d.noun, name)); err != nil {
					return err
				}
				c, err := a.connect(cmd.Context())
				if err != nil {
					return err
				}
				h, err := d.del(cmd.Context(), c, name)
				if err != nil {
					return err
				}
				if err := a.finishOp(cmd.Context(), h, d.noun, name, "deleted", nil); err != nil {
					return err
				}
			}
			return nil
		},
	})
}

// addEdit adds "edit": an editor session on the YAML document, written back
// with the ETag read at the start so a concurrent change is detected.
func addEdit[T any](a *app, parent *cobra.Command, d *kindDesc[T]) {
	var file string
	cmd := &cobra.Command{
		Use:   "edit NAME",
		Short: "Edit a " + d.noun + " in your editor",
		Long: `Open the specification of a ` + d.noun + ` as YAML in $JAILOPS_EDITOR, $VISUAL or $EDITOR
(default vi), and send it back when the editor exits. The update carries the
ETag of the version you started from: if somebody else changed the object in
the meantime the server answers "conflict" and nothing is overwritten; your
text stays in a temporary file whose path is printed.

Saving without a change, or an empty file, cancels. Only the specification and
the labels can be changed; server-owned fields are not shown. With -f the
document is read from a file instead of an editor, which is how scripts replace
a ` + d.noun + `. With --dry-run the edited document is validated and printed,
not sent.`,
		Example:           "EDITOR=nano jailops @P@ edit NAME\njailops @P@ edit NAME -f NAME.yaml",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKind(a, d),
		RunE: func(cmd *cobra.Command, args []string) error {
			return editObject(a, cmd.Context(), d, args[0], file, strings.TrimPrefix(cmd.Parent().CommandPath(), "jailops "))
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "take the new document from this file instead of an editor")
	parent.AddCommand(cmd)
}

func (a *app) editorArgv() []string {
	for _, k := range []string{"JAILOPS_EDITOR", "VISUAL", "EDITOR"} {
		if v := strings.TrimSpace(a.env.Getenv(k)); v != "" {
			return strings.Fields(v)
		}
	}
	return []string{"vi"}
}

func editObject[T any](a *app, ctx context.Context, d *kindDesc[T], name, file, path string) error {
	c, err := a.connect(ctx)
	if err != nil {
		return err
	}
	obj, etag, err := d.get(ctx, c, name)
	if err != nil {
		return err
	}
	orig, err := toYAML(document(d.kind, *d.meta(&obj), d.specOf(&obj)))
	if err != nil {
		return err
	}
	var edited []byte
	keep := ""
	if file != "" {
		if edited, err = a.readInput(file); err != nil {
			return err
		}
	} else {
		header := fmt.Sprintf("# Editing %s %q (generation %d). Save and quit to apply.\n# No change or an empty file cancels. Server-owned fields are not shown.\n",
			d.noun, name, d.meta(&obj).Generation)
		f, err := os.CreateTemp("", "jailops-"+d.noun+"-"+name+"-*.yaml")
		if err != nil {
			return err
		}
		keep = f.Name()
		_, werr := f.WriteString(header + string(orig))
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return werr
		}
		argv := a.editorArgv()
		ec := exec.CommandContext(ctx, argv[0], append(argv[1:], keep)...)
		ec.Stdin, ec.Stdout, ec.Stderr = a.env.Stdin, a.env.Stdout, a.env.Stderr
		if err := ec.Run(); err != nil {
			return &hintError{err: fmt.Errorf("editor %q failed: %w", argv[0], err), hint: "your text is in " + keep}
		}
		if edited, err = os.ReadFile(keep); err != nil {
			return err
		}
	}
	strip := func(b []byte) string {
		var out []string
		for _, l := range strings.Split(string(b), "\n") {
			if !strings.HasPrefix(l, "#") {
				out = append(out, l)
			}
		}
		return strings.TrimSpace(strings.Join(out, "\n"))
	}
	cleanup := func() {
		if keep != "" {
			_ = os.Remove(keep)
		}
	}
	if strip(edited) == "" {
		cleanup()
		fprintln(a.env.Stderr, "Edit cancelled: the file is empty.")
		return nil
	}
	if strip(edited) == strip(orig) {
		cleanup()
		fprintf(a.env.Stdout, "%s/%s unchanged\n", d.noun, name)
		return nil
	}
	fail := func(err error) error {
		if keep == "" {
			return err
		}
		return &hintError{err: err, hint: fmt.Sprintf("your edit is saved in %s; fix it and apply with: jailops %s edit %s -f %s", keep, path, name, keep)}
	}
	docs, err := readDocs(edited)
	if err != nil {
		return fail(err)
	}
	if len(docs) != 1 {
		return fail(usagef("edit takes exactly one document, found %d", len(docs)))
	}
	nu, err := d.parse(api.ObjectMeta{}, docs[0], name)
	if err != nil {
		return fail(err)
	}
	next := obj
	d.setSpec(&next, nu)
	if ok, err := a.dry("update "+d.noun+" "+name, document(d.kind, *d.meta(&next), d.specOf(&next))); ok || err != nil {
		cleanup()
		return err
	}
	out, err := d.update(ctx, c, name, next, etag)
	if err != nil {
		var ae *api.Error
		if errors.As(err, &ae) && ae.Code == api.CodeConflict && keep != "" {
			return ae.WithHint(fmt.Sprintf("someone changed the %s while you edited; your text is saved in %s, run edit again and re-apply it", d.noun, keep))
		}
		return fail(err)
	}
	cleanup()
	return a.finishOp(ctx, nil, d.noun, name, "updated", out)
}

// crudCommands adds list, show, create, edit and delete.
func crudCommands[T any](a *app, parent *cobra.Command, d *kindDesc[T], createLong, createExample string, flagBuild func(*cobra.Command) func(string) (T, error)) {
	addList(a, parent, d, "")
	addShow(a, parent, d)
	addCreate(a, parent, d, createLong, createExample, flagBuild)
	addEdit(a, parent, d)
	addDelete(a, parent, d)
}
