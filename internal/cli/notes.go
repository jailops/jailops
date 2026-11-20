package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

// addNotes adds "notes" to the commands of a kind whose spec has notes: print them, and the subcommands edit, set
// and clear. notes points at the notes member of an object.
func addNotes[T any](a *app, parent *cobra.Command, d *kindDesc[T], notes func(*T) *string) {
	art := article(d.noun) + " " + d.noun
	upper := strings.ToUpper(strings.ReplaceAll(d.noun, " ", "_"))
	cmd := &cobra.Command{
		Use:   "notes " + upper,
		Short: "Show the notes of " + art,
		Long: `Print the notes of ` + art + ` as they were written: a long text about it, such as what it does,
how it was set up and who to ask. Nothing is printed when there are none. With -o json
the answer is {"notes": "..."}.

The comment stays the one line that lists show; the notes are the rest. They are plain
text of at most 64 KiB; the web console shows Markdown in them (headings, lists, code,
links).`,
		Example:           "jailops @P@ notes NAME\njailops @P@ notes edit NAME\njailops @P@ notes set NAME --file NOTES.md",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeArgs(completeKind(a, d)),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			obj, _, err := d.get(cmd.Context(), c, args[0])
			if err != nil {
				return err
			}
			text := *notes(&obj)
			if a.format != fmtTable {
				return a.emit(map[string]string{"notes": text}, nil, nil, func() []string { return []string{args[0]} })
			}
			if text != "" {
				fprintln(a.env.Stdout, text)
			}
			return nil
		},
	}
	cmd.Example = strings.ReplaceAll(cmd.Example, "@P@", strings.TrimPrefix(parent.CommandPath(), "jailops "))
	cmd.Example = strings.ReplaceAll(cmd.Example, "NAME", exampleName(d.noun))

	edit := &cobra.Command{
		Use:   "edit " + upper,
		Short: "Edit the notes of " + art + " in your editor",
		Long: `Open the notes of ` + art + ` in $JAILOPS_EDITOR, $VISUAL or $EDITOR (default vi) and
save them when the editor exits. The file holds the notes alone. Saving the file unchanged
changes nothing; saving it empty removes the notes.

The update carries the version you started from: if somebody changed the ` + d.noun + ` in the
meantime the server answers "precondition_failed", nothing is overwritten and your text
stays in a temporary file whose path is printed.`,
		Example:           "EDITOR=nano jailops @P@ notes edit NAME",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeArgs(completeKind(a, d)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return editNotes(a, cmd.Context(), d, notes, args[0], cmd.Parent().Parent().CommandPath())
		},
	}
	var file string
	set := &cobra.Command{
		Use:   "set " + upper + " --file FILE",
		Short: "Replace the notes of " + art + " with a file",
		Long: `Replace the notes of ` + art + ` with the content of FILE, or of standard input when
FILE is "-". An empty file removes the notes.`,
		Example:           "jailops @P@ notes set NAME --file NOTES.md\nprintf 'Owner: ops team\\n' | jailops @P@ notes set NAME --file -",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeArgs(completeKind(a, d)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if file == "" {
				return usagef("--file is required (- reads standard input)")
			}
			b, err := a.readInput(file)
			if err != nil {
				return err
			}
			return writeNotes(a, cmd.Context(), d, notes, args[0], string(b))
		},
	}
	set.Flags().StringVarP(&file, "file", "f", "", "the file with the notes; - reads standard input")
	clear := &cobra.Command{
		Use:               "clear " + upper,
		Short:             "Remove the notes of " + art,
		Long:              `Remove the notes of ` + art + `. The comment stays.`,
		Example:           "jailops @P@ notes clear NAME",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeArgs(completeKind(a, d)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return writeNotes(a, cmd.Context(), d, notes, args[0], "")
		},
	}
	for _, sub := range []*cobra.Command{edit, set, clear} {
		sub.Example = strings.ReplaceAll(sub.Example, "@P@", strings.TrimPrefix(parent.CommandPath(), "jailops "))
		sub.Example = strings.ReplaceAll(sub.Example, "NAME", exampleName(d.noun))
		cmd.AddCommand(sub)
	}
	parent.AddCommand(cmd)
}

// exampleName is the name the examples of a kind use.
func exampleName(noun string) string {
	switch noun {
	case "jail":
		return "web"
	case "network":
		return "lan"
	case "volume":
		return "data"
	case "project":
		return "shop"
	}
	return "NAME"
}

// cleanNotes is the form notes are sent in: line feeds only, no blanks at the end.
func cleanNotes(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.TrimRight(s, " \t\n")
}

func editNotes[T any](a *app, ctx context.Context, d *kindDesc[T], notes func(*T) *string, name, path string) error {
	c, err := a.connect(ctx)
	if err != nil {
		return err
	}
	obj, etag, err := d.get(ctx, c, name)
	if err != nil {
		return err
	}
	cur := *notes(&obj)
	f, err := os.CreateTemp("", "jailops-"+strings.ReplaceAll(d.noun, " ", "-")+"-"+name+"-notes-*.md")
	if err != nil {
		return err
	}
	keep := f.Name()
	text := cur
	if text != "" {
		text += "\n"
	}
	_, werr := f.WriteString(text)
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
	b, err := os.ReadFile(keep)
	if err != nil {
		return err
	}
	if cleanNotes(string(b)) == cleanNotes(cur) {
		_ = os.Remove(keep)
		fprintf(a.env.Stderr, "%s/%s notes unchanged\n", d.noun, name)
		return nil
	}
	if err := writeNotesTo(a, ctx, c, d, notes, name, obj, etag, string(b)); err != nil {
		hint := fmt.Sprintf("your text is saved in %s; apply it with: %s notes set %s --file %s", keep, path, name, keep)
		var ae *api.Error
		if errors.As(err, &ae) {
			if ae.Code == api.CodePreconditionFailed {
				hint = fmt.Sprintf("someone changed the %s while you edited; %s", d.noun, hint)
			}
			return ae.WithHint(hint)
		}
		return &hintError{err: err, hint: hint}
	}
	_ = os.Remove(keep)
	return nil
}

// writeNotes reads the object and replaces its notes, with the version it read.
func writeNotes[T any](a *app, ctx context.Context, d *kindDesc[T], notes func(*T) *string, name, text string) error {
	c, err := a.connect(ctx)
	if err != nil {
		return err
	}
	obj, etag, err := d.get(ctx, c, name)
	if err != nil {
		return err
	}
	return writeNotesTo(a, ctx, c, d, notes, name, obj, etag, text)
}

func writeNotesTo[T any](a *app, ctx context.Context, c *sdk.Client, d *kindDesc[T], notes func(*T) *string, name string, obj T, etag, text string) error {
	text = cleanNotes(text)
	if text == *notes(&obj) {
		fprintf(a.env.Stderr, "%s/%s notes unchanged\n", d.noun, name)
		return nil
	}
	if ok, err := a.dry("change the notes of "+d.noun+" "+name, map[string]string{"notes": text}); ok || err != nil {
		return err
	}
	*notes(&obj) = text
	if _, err := d.update(ctx, c, name, obj, etag); err != nil {
		return err
	}
	what := "updated"
	if text == "" {
		what = "removed"
	}
	fprintf(a.env.Stderr, "%s/%s notes %s\n", d.noun, name, what)
	return nil
}

// notesSummary is the line a show prints for notes: the first line, cut, and how to read the rest ("" for none).
func notesSummary(notes, cmd string) string {
	if notes == "" {
		return ""
	}
	first, rest, more := strings.Cut(notes, "\n")
	if r := []rune(first); len(r) > 60 {
		first, more = string(r[:60])+"...", true
	}
	if !more {
		return first
	}
	n := strings.Count(rest, "\n") + 1
	if rest == "" {
		n = 0
	}
	return fmt.Sprintf("%s (%d more lines: %s)", first, n, cmd)
}
