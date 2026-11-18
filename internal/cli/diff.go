package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

var diffDatasets = []string{"root", "etc", "local", "var-db", "var-log", "var-mail", "var-spool", "home", "rootdir"}

func (a *app) jailDiff(d *kindDesc[api.Jail]) *cobra.Command {
	var limit int
	var dataset string
	var summary, packages bool
	cmd := &cobra.Command{
		Use:   "diff NAME",
		Short: "Show what a thin jail holds on top of its image",
		Long: `Show the files a thin jail added, changed or removed since its image. The root is
compared with the image generation it was cloned from. The persistent datasets
(etc, local, home and the others) are compared with their snapshot from when the jail was
made; a jail made before that snapshot existed is compared file by file with
the image, and the output says so.

Each line is a kind and a path: + added, M changed, - removed, R renamed (old
-> new), grouped by dataset. --summary prints the counts only. --limit sets
how many lines each dataset lists (200 by default); the counts are always
complete. --dataset keeps one dataset. --packages adds the packages installed
on purpose, which needs a running jail.

A jail with a full copy of its image (a thick or Linux jail) has nothing to
compare with. A jail with a locked encrypted volume is refused until it is
unlocked. -o json and -o yaml print the server's reply. Reading needs the
operator role for the jail's project.`,
		Example: `jailops jail diff web
jailops jail diff web --summary
jailops jail diff web --dataset local --limit 50
jailops jail diff web --packages -o json`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeArgs(completeKind(a, d)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if limit < 1 || limit > api.MaxDiffLimit {
				return usagef("--limit takes a number from 1 to %d", api.MaxDiffLimit)
			}
			c, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			r, err := c.Jails().Diff(cmd.Context(), args[0], sdk.DiffOptions{Limit: limit, Dataset: dataset, Packages: packages})
			if err != nil {
				return err
			}
			if a.format == fmtJSON || a.format == fmtYAML {
				return a.emit(r, nil, nil, nil)
			}
			a.printDiff(r, summary)
			return nil
		},
	}
	f := cmd.Flags()
	f.IntVar(&limit, "limit", api.DefaultDiffLimit, "lines listed for each dataset")
	f.StringVar(&dataset, "dataset", "", "one dataset only: root, or a persistent dataset such as etc, local or var-log")
	f.BoolVar(&summary, "summary", false, "print the counts only")
	f.BoolVar(&packages, "packages", false, "add the packages installed on purpose (needs a running jail)")
	_ = cmd.RegisterFlagCompletionFunc("dataset", fixedValues(diffDatasets...))
	return cmd
}

var diffMarks = map[string]string{api.DiffAdded: "+", api.DiffModified: "M", api.DiffRemoved: "-", api.DiffRenamed: "R"}

// diffCounts is "3 changes (2 added, 1 modified)", or "no changes".
func diffCounts(total int, counts map[string]int) string {
	if total == 0 {
		return "no changes"
	}
	var parts []string
	for _, k := range []string{api.DiffAdded, api.DiffModified, api.DiffRemoved, api.DiffRenamed} {
		if n := counts[k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, k))
		}
	}
	return plural(total, "change") + " (" + strings.Join(parts, ", ") + ")"
}

func diffBase(d api.DiffDataset, image string) string {
	switch d.Base {
	case api.DiffBaseImage:
		return "since " + image
	case api.DiffBaseBaseline:
		return "since the jail was made"
	case api.DiffBaseTree:
		return "compared with the image, not with a snapshot"
	}
	return d.Base
}

func (a *app) printDiff(r api.JailDiff, summary bool) {
	w := a.env.Stdout
	all := map[string]int{}
	wn, wp := 0, 0
	for _, d := range r.Datasets {
		wn, wp = max(wn, len(d.Name)), max(wp, len(d.Path))
	}
	for _, d := range r.Datasets {
		for k, n := range d.Counts {
			all[k] += n
		}
		head := fmt.Sprintf("%s %s: %s", d.Name, d.Path, diffBase(d, r.Image))
		if summary {
			fprintln(w, fmt.Sprintf("%-*s  %-*s  %s", wn, d.Name, wp, d.Path, diffCounts(d.Total, d.Counts)))
			continue
		}
		if d.Total == 0 && d.Note == "" {
			continue
		}
		fprintln(w, head)
		if d.Note != "" && d.Base != api.DiffBaseTree {
			fprintln(w, "  "+d.Note)
		}
		for _, e := range d.Entries {
			line := "  " + diffMarks[e.Kind] + " " + terminalSafe(e.Path)
			if e.To != "" {
				line += " -> " + terminalSafe(e.To)
			}
			fprintln(w, line)
		}
		if d.Truncated {
			fprintln(w, fmt.Sprintf("  ... and %d more (--limit %d lists them all)", d.Total-len(d.Entries), min(d.Total, api.MaxDiffLimit)))
		}
	}
	if r.Packages != nil {
		fprintln(w, "packages installed on purpose")
		for _, p := range r.Packages.Packages {
			fprintln(w, "  "+terminalSafe(p))
		}
		if len(r.Packages.Packages) == 0 && r.Packages.Note == "" {
			fprintln(w, "  none")
		}
		if r.Packages.Note != "" {
			fprintln(w, "  "+r.Packages.Note)
		}
	}
	fprintln(w, "total: "+diffCounts(r.Total(), all))
}
