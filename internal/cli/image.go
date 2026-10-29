package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

func imageDesc() *kindDesc[api.Image] {
	d := newDesc("image", "images", api.KindImage,
		func(i *api.Image) *api.ObjectMeta { return &i.Metadata },
		func(i *api.Image) *api.ImageSpec { return &i.Spec })
	fromResource(&d, func(c *sdk.Client) *sdk.Resource[api.Image] { return c.Images().Resource })
	d.head = []string{"NAME", "OS", "RELEASE", "ARCH", "GENERATIONS", "ALIASES"}
	d.row = func(_ time.Time, i api.Image) []string {
		return []string{i.Metadata.Name, string(i.Spec.OS), i.Spec.Release, i.Spec.Arch,
			strconv.Itoa(len(i.Status.Generations)), joinOrDash(i.Spec.Aliases)}
	}
	d.show = func(a *app, i api.Image) error { return a.showObject(i.Metadata, nil, i.Spec, i.Status) }
	return &d
}

// parseGeneration reads a generation written as gN or N, or as IMAGE#gN when
// image names the image it must belong to; what names the flag or argument
// for the error. It returns the number.
func parseGeneration(what, s, image string) (int, error) {
	ref := s
	if img, g, ok := strings.Cut(s, "#"); ok {
		if image != "" && img != image {
			return 0, usagef("%s: %q belongs to image %s, not %s", what, s, img, image)
		}
		ref = g
	}
	n, err := strconv.Atoi(strings.TrimPrefix(ref, "g"))
	if err != nil || n < 1 {
		return 0, usagef("%s must be a generation such as g2, not %q", what, s)
	}
	return n, nil
}

// generationNames lists the generations of an image as gN, for completion.
func generationNames(ctx context.Context, c *sdk.Client, image string) ([]string, error) {
	gens, err := collect(c.Images().Generations(ctx, image))
	var n []string
	for _, g := range gens {
		n = append(n, fmt.Sprintf("g%d", g.Number))
	}
	return n, err
}

// completeGenerations completes a generation of the image named first.
func (a *app) completeGenerations() completer {
	return a.completeWith(func(ctx context.Context, c *sdk.Client, args []string) ([]string, error) {
		if len(args) == 0 {
			return nil, nil
		}
		return generationNames(ctx, c, args[0])
	})
}

// completeAliases completes an alias of the image named first.
func (a *app) completeAliases() completer {
	return a.completeWith(func(ctx context.Context, c *sdk.Client, args []string) ([]string, error) {
		if len(args) == 0 {
			return nil, nil
		}
		img, err := c.Images().Get(ctx, args[0])
		if err != nil {
			return nil, err
		}
		return sortedKeys(img.Status.Aliases), nil
	})
}

func (a *app) imageCmd() *cobra.Command {
	d := imageDesc()
	ic := group("image", "Manage images and their generations",
		`An image is a named line of immutable base generations built from packages.
Every build adds a generation (g1, g2, ...); a jail's root is a clone of one
generation, and 'jailops jail upgrade' moves a jail to a newer one. Aliases such as
"stable" name the generation to follow.`,
		`jailops image list
jailops image build freebsd-15
jailops image generations freebsd-15
jailops image alias set freebsd-15 stable g2`, "images")
	crudCommands(a, ic, d, `Define an image. The definition says where the base comes from; 'jailops image
build' then produces its first generation. Without --arch the image is built for
the architecture of the server.`,
		`jailops image create freebsd-15 --os freebsd --release 15.1-RELEASE --alias stable
jailops image create freebsd-15-arm --release 15.1-RELEASE --arch arm64
jailops image create -f freebsd-15.yaml`,
		func(cmd *cobra.Command) func(string) (api.Image, error) {
			var os_, release, arch, stype, repo, keys string
			var sets, aliases []string
			var keepLast, keepDays int
			f := cmd.Flags()
			f.StringVar(&os_, "os", "freebsd", "guest operating system: freebsd or linux")
			f.StringVar(&release, "release", "", "release, for example 15.1-RELEASE (required without -f)")
			f.StringVar(&arch, "arch", "", "architecture: amd64 or arm64 (default: the server's)")
			f.StringVar(&stype, "source-type", "pkgbase", "how the base is built: pkgbase, minirootfs or debootstrap")
			enumFlag(cmd, "os", string(api.OSFreeBSD), string(api.OSLinux))
			enumFlag(cmd, "arch", "amd64", "arm64")
			enumFlag(cmd, "source-type", "pkgbase", "minirootfs", "debootstrap")
			f.StringVar(&repo, "repo", "", "package repository or rootfs mirror")
			f.StringVar(&keys, "keys", "", "keyring or key directory that checks the base's signature (required for debootstrap)")
			f.StringArrayVar(&sets, "set", nil, "package set to install (repeatable)")
			f.StringArrayVar(&aliases, "alias", nil, "movable alias such as stable (repeatable)")
			f.IntVar(&keepLast, "keep-last", 0, "always keep this many generations")
			f.IntVar(&keepDays, "keep-days", 0, "keep unused generations this many days")
			return func(name string) (api.Image, error) {
				if release == "" {
					return api.Image{}, usagef("--release is required (or use -f FILE)")
				}
				if arch == "" {
					var err error
					if arch, err = a.serverArch(cmd.Context()); err != nil {
						return api.Image{}, err
					}
				}
				return api.Image{Metadata: api.ObjectMeta{Name: name}, Spec: api.ImageSpec{
					OS: api.OS(os_), Release: release, Arch: arch,
					Source:    api.ImageSource{Type: stype, Repo: repo, Keys: keys, Sets: sets},
					Aliases:   aliases,
					Retention: api.ImageRetention{KeepLast: keepLast, KeepDays: keepDays},
				}}, nil
			}
		})
	for _, c := range ic.Commands() {
		if c.Name() == "delete" {
			c.Long = "Delete one or more images with all their generations and aliases. The server refuses, naming the jails, while a jail uses any generation.\nThe command asks for confirmation unless -y is given."
		}
	}
	ic.AddCommand(a.imageGenerations(d), a.imageBuild(d), a.imageUpgrade(d), a.imageAlias(d), a.imageDeleteGen(d))
	return ic
}

// serverArch asks the server for its architecture, the default of a new
// image's --arch.
func (a *app) serverArch(ctx context.Context) (string, error) {
	c, err := a.connect(ctx)
	if err != nil {
		return "", err
	}
	info, err := c.Info(ctx)
	if err != nil {
		return "", err
	}
	if info.Environment.Arch == "" {
		return "", &hintError{err: errors.New("the server does not report its architecture"), hint: "pass --arch amd64 or --arch arm64"}
	}
	return info.Environment.Arch, nil
}

func (a *app) imageUpgrade(d *kindDesc[api.Image]) *cobra.Command {
	var noJails bool
	cmd := &cobra.Command{
		Use:   "upgrade IMAGE",
		Short: "Build the next generation and move the jails that use the image to it",
		Long: `Upgrade an image: build the next generation from its newest one (its packages
upgraded), then move every thin jail that uses the image to it, one at a time,
through the steps of 'jailops jail upgrade' (snapshot, new root, /etc merge,
swap, health check, root rollback on failure), with a record in each jail's
update history. The first jail that fails or needs a human stops the operation;
the jails after it are not attempted. When the package set did not change, no
generation is kept and no jail is touched.

Jails of every project are moved unless --project names one (the flag only; the
current project of the remote does not narrow an image upgrade). Thick jails,
Linux jails, jails with another update strategy and jails pinned to a
generation are skipped and listed. A stopped jail is moved too: it is started for
the health check and stopped again.

--no-jails builds the generation only. --dry-run plans the build and lists the
jails that would move; nothing changes. The build can take minutes; the command
waits unless --no-wait is given. The exit status is 1 when a jail failed.`,
		Example: `jailops image upgrade freebsd-15 --dry-run
jailops image upgrade freebsd-15
jailops image upgrade freebsd-15 --project shop
jailops image upgrade freebsd-15 --no-jails`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKind(a, d),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, image := cmd.Context(), args[0]
			u := api.ImageUpgrade{NoJails: noJails, Project: a.project, DryRun: a.dryRun}
			c, err := a.connect(ctx)
			if err != nil {
				return err
			}
			h, err := handle(c.Images().Upgrade(ctx, image, u))
			if err != nil {
				return err
			}
			if !a.waiting() && !a.dryRun {
				return a.finishOp(ctx, h, "image", image, "upgrading", nil)
			}
			v, _, err := a.await(ctx, h)
			if err != nil {
				return err
			}
			res, _ := v.(api.ImageUpgradeResult)
			return a.emitImageUpgrade(image, res)
		},
	}
	cmd.Flags().BoolVar(&noJails, "no-jails", false, "build the generation only; move no jail")
	return cmd
}

// emitImageUpgrade prints the result of an image upgrade: what the build did,
// then one line per jail. A failed jail makes the exit status 1.
func (a *app) emitImageUpgrade(image string, res api.ImageUpgradeResult) error {
	if a.format == fmtTable {
		switch {
		case res.Unchanged:
			fprintf(a.env.Stdout, "image/%s unchanged: %s already has the current package set; nothing to upgrade\n", image, res.From)
		case res.DryRun:
			n := 0
			for _, j := range res.Jails {
				if j.Status == api.ImageUpgradePlanned {
					n++
				}
			}
			fprintf(a.env.Stdout, "dry-run: would build the next generation of %s from %s and move %d of %d jails\n", image, res.From, n, len(res.Jails))
		case res.Generation != nil:
			fprintf(a.env.Stdout, "generation/%s built from %s\n", res.Generation.Name, res.From)
		}
		if len(res.Jails) == 0 {
			fprintf(a.env.Stdout, "no jail uses %s\n", image)
			return nil
		}
	}
	err := a.emit(res, func(col int, v string) string {
		if col != 2 {
			return ""
		}
		switch v {
		case api.ImageUpgradeUpgraded:
			return "32"
		case api.ImageUpgradeFailed:
			return "31"
		case api.ImageUpgradeNotAttempted:
			return "33"
		}
		return ""
	}, func() ([]string, [][]string) {
		if len(res.Jails) == 0 {
			return nil, nil
		}
		var rows [][]string
		for _, j := range res.Jails {
			rows = append(rows, []string{j.Jail, j.Project, j.Status, dash(j.From), dash(j.To), dash(j.Reason)})
		}
		return []string{"JAIL", "PROJECT", "STATUS", "FROM", "TO", "REASON"}, rows
	}, func() []string {
		var n []string
		for _, j := range res.Jails {
			n = append(n, j.Jail)
		}
		return n
	})
	if err != nil {
		return err
	}
	for _, j := range res.Jails {
		if j.Status == api.ImageUpgradeFailed {
			return &hintError{err: fmt.Errorf("jail %s failed; the jails after it were not attempted", j.Jail),
				hint: "'jailops update history " + j.Jail + " --project " + j.Project + "' shows what happened; run the upgrade again once it is resolved"}
		}
	}
	return nil
}

func (a *app) imageGenerations(d *kindDesc[api.Image]) *cobra.Command {
	return &cobra.Command{
		Use:               "generations IMAGE",
		Aliases:           []string{"gens"},
		Short:             "List the generations of an image",
		Long:              "List the generations of an image with the jails that use each one. A generation can be deleted only when no jail uses it.",
		Example:           "jailops image generations freebsd-15\njailops image generations freebsd-15 -o json",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKind(a, d),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			gens, err := collect(c.Images().Generations(cmd.Context(), args[0]))
			if err != nil {
				return err
			}
			return a.emit(nonNil(gens), nil, func() ([]string, [][]string) {
				var rows [][]string
				for _, g := range gens {
					m := g.Manifest
					if len(m) > 19 {
						m = m[:19]
					}
					rows = append(rows, []string{g.Name, strconv.Itoa(g.Number), dash(parentName(g.Parent)), strconv.Itoa(g.Packages),
						dash(m), age(a.env.Now(), g.CreatedAt), joinOrDash(g.Refs)})
				}
				return []string{"NAME", "NUMBER", "PARENT", "PACKAGES", "MANIFEST", "AGE", "USED BY"}, rows
			}, func() []string {
				var n []string
				for _, g := range gens {
					n = append(n, g.Name)
				}
				return n
			})
		},
	}
}

func parentName(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("g%d", n)
}

func (a *app) imageBuild(d *kindDesc[api.Image]) *cobra.Command {
	var from string
	cmd := &cobra.Command{
		Use:   "build IMAGE",
		Short: "Build the next generation of an image",
		Long: `Build the next generation of an image from its source, or from an earlier
generation with --from (which upgrades that generation's packages). The build
can take minutes; the command waits unless --no-wait is given.

--dry-run asks the server to plan the build without keeping the result.`,
		Example:           "jailops image build freebsd-15\njailops image build freebsd-15 --from g2 --dry-run",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKind(a, d),
		RunE: func(cmd *cobra.Command, args []string) error {
			b := api.ImageBuild{DryRun: a.dryRun}
			if from != "" {
				n, err := parseGeneration("--from", from, args[0])
				if err != nil {
					return err
				}
				b.FromGeneration = n
			}
			c, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			h, err := handle(c.Images().Build(cmd.Context(), args[0], b))
			if err != nil {
				return err
			}
			if a.dryRun {
				v, _, err := a.await(cmd.Context(), h)
				if err != nil {
					return err
				}
				g, _ := v.(api.ImageGeneration)
				return a.preview(fmt.Sprintf("build %s (%d packages)", dash(g.Name), g.Packages), b)
			}
			if !a.waiting() {
				return a.finishOp(cmd.Context(), h, "image", args[0], "built", nil)
			}
			v, _, err := a.await(cmd.Context(), h)
			if err != nil {
				return err
			}
			g, _ := v.(api.ImageGeneration)
			return a.emitOne(g, "generation", g.Name, "built")
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "build from this earlier generation, gN")
	_ = cmd.RegisterFlagCompletionFunc("from", a.completeGenerations())
	return cmd
}

func (a *app) imageAlias(d *kindDesc[api.Image]) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "alias",
		Short: "Point a movable alias of an image at a generation, or delete it",
		Long: `Aliases such as "stable" or "latest" name a generation of an image. Jails that
reference IMAGE@ALIAS follow the generation the alias points to. A build moves the
aliases of the image to the new generation; 'alias set' pins one to any generation.`,
		Example: "jailops image alias set freebsd-15 stable 2\njailops image alias delete freebsd-15 old",
	}
	cmd.AddCommand(a.imageAliasSet(d), a.imageAliasDelete(d))
	return cmd
}

func (a *app) imageAliasSet(d *kindDesc[api.Image]) *cobra.Command {
	return &cobra.Command{
		Use:   "set IMAGE ALIAS [GENERATION]",
		Short: "Point an alias at a generation (the newest when omitted)",
		Long: `Create the alias or move it to GENERATION (gN, or the number N). Without
GENERATION it points at the newest generation. The alias is also recorded in the
image spec, so later builds keep moving it.`,
		Example:           "jailops image alias set freebsd-15 stable g2\njailops image alias set freebsd-15 latest",
		Args:              cobra.RangeArgs(2, 3),
		ValidArgsFunction: completeArgs(completeKind(a, d), a.completeAliases(), a.completeGenerations()),
		RunE: func(cmd *cobra.Command, args []string) error {
			n := 0
			if len(args) == 3 {
				var err error
				if n, err = parseGeneration("GENERATION", args[2], args[0]); err != nil {
					return err
				}
			}
			target := "the newest generation"
			if n > 0 {
				target = fmt.Sprintf("generation g%d", n)
			}
			if ok, err := a.dry(fmt.Sprintf("point alias %s of image %s at %s", args[1], args[0], target), nil); ok || err != nil {
				return err
			}
			c, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			img, err := c.Images().SetAlias(cmd.Context(), args[0], args[1], n)
			if err != nil {
				return err
			}
			return a.finishOp(cmd.Context(), nil, "image", args[0], fmt.Sprintf("alias %s set to g%d", args[1], img.Status.Aliases[args[1]]), nil)
		},
	}
}

func (a *app) imageAliasDelete(d *kindDesc[api.Image]) *cobra.Command {
	return &cobra.Command{
		Use:   "delete IMAGE ALIAS",
		Short: "Delete an alias of an image",
		Long: `Delete the alias from the image and from its spec, so a later build does not bring
it back. Jails that reference IMAGE@ALIAS can no longer resolve it.`,
		Example:           "jailops image alias delete freebsd-15 old",
		Aliases:           []string{"rm"},
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeArgs(completeKind(a, d), a.completeAliases()),
		RunE: func(cmd *cobra.Command, args []string) error {
			if ok, err := a.dry(fmt.Sprintf("delete alias %s of image %s", args[1], args[0]), nil); ok || err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Delete alias %s of image %s?", args[1], args[0])); err != nil {
				return err
			}
			c, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			if _, err := c.Images().DeleteAlias(cmd.Context(), args[0], args[1]); err != nil {
				return err
			}
			return a.finishOp(cmd.Context(), nil, "image", args[0], "alias "+args[1]+" deleted", nil)
		},
	}
}

func (a *app) imageDeleteGen(d *kindDesc[api.Image]) *cobra.Command {
	return &cobra.Command{
		Use:               "delete-generation IMAGE GENERATION",
		Short:             "Delete a generation of an image that no jail uses",
		Long:              "Delete one generation (gN, or the number N) of an image. The server refuses (image_in_use) while a jail uses it. Aliases that pointed at it move to the next newest remaining generation, or are removed when none remains.",
		Example:           "jailops image delete-generation freebsd-15 g1",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeArgs(completeKind(a, d), a.completeGenerations()),
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := parseGeneration("GENERATION", args[1], args[0])
			if err != nil {
				return err
			}
			label := fmt.Sprintf("%s#g%d", args[0], n)
			if ok, err := a.dry("delete generation "+label, nil); ok || err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Delete generation %s?", label)); err != nil {
				return err
			}
			c, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			h, err := handle(c.Images().DeleteGeneration(cmd.Context(), args[0], n))
			if err != nil {
				return err
			}
			return a.finishOp(cmd.Context(), h, "generation", label, "deleted", nil)
		},
	}
}
