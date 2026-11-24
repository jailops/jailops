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
	renameFrom(&d, func(c *sdk.Client) *sdk.Resource[api.Image] { return c.Images().Resource })
	d.renameLong = renameLong("image", `its datasets and generations on every pool, the jails on it (running
ones too: their image reference, their records and their datasets' properties), the image lists of
projects and the records of its nightly upgrade`)
	d.head = []string{"NAME", "OS", "RELEASE", "ARCH", "GENERATIONS"}
	d.row = func(_ time.Time, i api.Image) []string {
		return []string{i.Metadata.Name, string(i.Spec.OS), i.Spec.Release, i.Spec.Arch,
			strconv.Itoa(len(i.Status.Generations))}
	}
	d.show = func(a *app, i api.Image) error { return a.showImage(i) }
	d.emptyHintFrom = func(ctx context.Context, c *sdk.Client) string {
		release := "15.1-RELEASE"
		if info, err := c.Info(ctx); err == nil && api.ImageNameForRelease(info.Environment.KernelVersion) != "" {
			release = api.BaseRelease(info.Environment.KernelVersion)
		}
		name := api.ImageNameForRelease(release)
		return "jailops image create " + name + " --release " + release + ", then jailops image build " + name
	}
	return &d
}

// imageRefs completes an image reference: the name of each image.
func imageRefs(ctx context.Context, c *sdk.Client) ([]string, error) {
	imgs, err := collect(c.Images().List(ctx))
	var out []string
	for _, i := range imgs {
		out = append(out, i.Metadata.Name)
	}
	return out, err
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

func (a *app) imageCmd() *cobra.Command {
	d := imageDesc()
	ic := group("image", "Manage images and their generations",
		`An image is one release, such as freebsd-15-1 for FreeBSD 15.1, built from
packages. Every build adds an immutable generation (g1, g2, ...): the patch
levels of the release. A new jail is a copy of the newest generation (or of the
one its image reference pins) and depends on it no further: it is upgraded in
place with 'jailops jail upgrade'. 'jailops image upgrade' builds the next
generation for the jails made later. A new release is a new image.`,
		`jailops image list
jailops image build freebsd-15-1
jailops image upgrade freebsd-15-1
jailops image generation list freebsd-15-1`, "images")
	crudCommands(a, ic, d, `Define an image. The definition says where the base comes from; 'jailops image
build' then produces its first generation. Without --arch the image is built for
the architecture of the server. A new image name has at most 32 characters.`,
		`jailops image create freebsd-15-1 --release 15.1-RELEASE
jailops image create freebsd-15-1-arm --release 15.1-RELEASE --arch arm64
jailops image create -f freebsd-15-1.yaml`,
		func(cmd *cobra.Command) func(string) (api.Image, error) {
			var os_, release, arch, stype, repo, keys, comment, upgradeAt string
			var autoUpgrade bool
			var sets []string
			var keepLast, keepDays int
			f := cmd.Flags()
			f.StringVar(&comment, "comment", "", "what the image is for")
			f.StringVar(&os_, "os", "freebsd", "guest operating system: freebsd or linux")
			f.StringVar(&release, "release", "", "release, for example 15.1-RELEASE (required without -f)")
			f.StringVar(&arch, "arch", "", "architecture: amd64 or arm64 (default: the server's)")
			f.StringVar(&stype, "source-type", "pkgbase", "how the base is built: pkgbase, minirootfs or debootstrap")
			enumFlag(cmd, "os", string(api.OSFreeBSD), string(api.OSLinux))
			enumFlag(cmd, "arch", "amd64", "arm64")
			enumFlag(cmd, "source-type", "pkgbase", "minirootfs", "debootstrap")
			f.StringVar(&repo, "repo", "", "package repository or rootfs mirror")
			f.StringVar(&keys, "keys", "", "keyring or key directory that checks the base's signature (required for debootstrap; pkgbase defaults to the key directory of the host's FreeBSD-base repository)")
			f.StringArrayVar(&sets, "package-set", nil, "package set to install (repeatable)")
			f.IntVar(&keepLast, "keep-last", 0, "always keep this many generations")
			f.IntVar(&keepDays, "keep-days", 0, "keep unused generations this many days")
			f.BoolVar(&autoUpgrade, "auto-upgrade", false, "upgrade the image every night: build the next generation (the patch updates of the release) for the jails made later; no jail is touched by it (a jail takes its patches by its own update policy: jailops config set JAIL update.mode=managed)")
			f.StringVar(&upgradeAt, "upgrade-at", api.DefaultAutoUpgradeAt, "time of day of the nightly upgrade, HH:MM in the host's time zone")
			return func(name string) (api.Image, error) {
				if release == "" && !a.partial {
					return api.Image{}, usagef("--release is required (or use -f FILE)")
				}
				if arch == "" && !a.partial { // set builds from the flags given and has the stored architecture
					var err error
					if arch, err = a.serverArch(cmd.Context()); err != nil {
						return api.Image{}, err
					}
				}
				spec := api.ImageSpec{
					Comment: comment, OS: api.OS(os_), Release: release, Arch: arch,
					Source:    api.ImageSource{Type: stype, Repo: repo, Keys: keys, Sets: sets},
					Retention: api.ImageRetention{KeepLast: keepLast, KeepDays: keepDays},
				}
				if autoUpgrade || cmd.Flags().Changed("auto-upgrade") || cmd.Flags().Changed("upgrade-at") {
					spec.AutoUpgrade = &api.ImageAutoUpgrade{Enabled: autoUpgrade, At: upgradeAt}
				}
				return api.Image{Metadata: api.ObjectMeta{Name: name}, Spec: spec}, nil
			}
		})
	for _, c := range ic.Commands() {
		switch c.Name() {
		case "delete":
			c.Long = "Delete one or more images with all their generations. No jail depends on an image: a jail is an independent copy,\nso the jails made from it are not touched. The command asks for confirmation unless -y is given."
		case "create":
			a.createThenBuild(c)
		}
	}
	ic.AddCommand(a.imageBuild(d), a.imageUpgrade(d), a.imageGeneration(d))
	return ic
}

// createThenBuild adds --build to 'image create': the first generation is built right after the definition is stored,
// the way the console's create dialog does it. A definition that cannot be stored builds nothing, and one that cannot
// be built stays (build it again with 'jailops image build').
func (a *app) createThenBuild(cmd *cobra.Command) {
	var build bool
	cmd.Flags().BoolVar(&build, "build", false, "build the first generation after creating the image (it can take minutes)")
	run := cmd.RunE
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if build && len(args) == 0 {
			return usagef("--build needs the name of the image on the command line, not -f FILE")
		}
		if err := run(cmd, args); err != nil || !build || a.dryRun {
			return err
		}
		c, err := a.connect(cmd.Context())
		if err != nil {
			return err
		}
		for _, name := range args {
			h, err := handle(c.Images().Build(cmd.Context(), name, api.ImageBuild{}))
			if err != nil {
				return err
			}
			if err := a.finishOp(cmd.Context(), h, "image", name, "built", nil); err != nil {
				return err
			}
		}
		return nil
	}
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
	return &cobra.Command{
		Use:   "upgrade IMAGE",
		Short: "Build the next generation of an image with its packages upgraded",
		Long: `Upgrade an image: build the next generation from its newest one, with the
packages of its repository at their newest versions (the patch levels of its
release). New jails are copied from the new generation. No jail is touched: a jail
is a copy of a generation and is upgraded in place with 'jailops jail upgrade'.
When the package set did not change, no generation is kept.

--dry-run plans the build and changes nothing. The build can take minutes; the
command waits unless --no-wait is given.

The order of the steps for the host and its jails, the checks to run after an
upgrade and the way back are in docs/lifecycle.md.`,
		Example: `jailops image upgrade freebsd-15-1 --dry-run
jailops image upgrade freebsd-15-1`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKind(a, d),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, image := cmd.Context(), args[0]
			u := api.ImageUpgrade{DryRun: a.dryRun}
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
}

// emitImageUpgrade prints the result of an image upgrade: what the build did.
func (a *app) emitImageUpgrade(image string, res api.ImageUpgradeResult) error {
	if a.format != fmtTable {
		return a.emit(res, nil, nil, func() []string {
			if res.Generation == nil {
				return nil
			}
			return []string{res.Generation.Name}
		})
	}
	switch {
	case res.Unchanged:
		fprintf(a.env.Stdout, "image/%s unchanged: %s already has the current package set; nothing to upgrade\n", image, res.From)
	case res.DryRun:
		fprintf(a.env.Stdout, "dry-run: would build the next generation of %s from %s\n", image, res.From)
		for _, l := range res.Plan {
			fprintf(a.env.Stdout, "  %s\n", l)
		}
	case res.Generation != nil:
		fprintf(a.env.Stdout, "generation/%s built from %s\n", res.Generation.Name, res.From)
	}
	return nil
}

func (a *app) imageGenerationList(d *kindDesc[api.Image]) *cobra.Command {
	return &cobra.Command{
		Use:               "list IMAGE",
		Short:             "List the generations of an image",
		Long:              "List the generations of an image. No jail depends on a generation: a jail is a copy, and a generation can be deleted\nwhatever jails were made from it.\nThe number (g1, g2, ...) is the JailOps generation counter of the image; FREEBSD is the FreeBSD version its base\npackages were built from (15.1-RELEASE-p6: release 15.1, patch level 6), or - for a generation built before JailOps\nrecorded it.",
		Example:           "jailops image generation list freebsd-15-1\njailops image generation list freebsd-15-1 -o json",
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
					rows = append(rows, []string{g.Name, strconv.Itoa(g.Number), dash(parentName(g.Parent)), dash(g.FreeBSDVersion), strconv.Itoa(g.Packages),
						dash(m), age(a.env.Now(), g.CreatedAt)})
				}
				return []string{"NAME", "NUMBER", "PARENT", "FREEBSD", "PACKAGES", "MANIFEST", "AGE"}, rows
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
generation with --from-generation (which upgrades that generation's packages). The build
can take minutes; the command waits unless --no-wait is given.

--dry-run asks the server to plan the build without keeping the result.`,
		Example:           "jailops image build freebsd-15-1\njailops image build freebsd-15-1 --from-generation g2 --dry-run",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKind(a, d),
		RunE: func(cmd *cobra.Command, args []string) error {
			b := api.ImageBuild{DryRun: a.dryRun}
			if from != "" {
				n, err := parseGeneration("--from-generation", from, args[0])
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
	cmd.Flags().StringVar(&from, "from-generation", "", "build from this earlier generation, gN")
	_ = cmd.RegisterFlagCompletionFunc("from-generation", a.completeGenerations())
	return cmd
}

func (a *app) imageGenerationDelete(d *kindDesc[api.Image]) *cobra.Command {
	return &cobra.Command{
		Use:               "delete IMAGE GENERATION",
		Short:             "Delete a generation of an image",
		Long:              "Delete one generation (gN, or the number N) of an image. No jail depends on a generation: a jail is an independent\ncopy, so the jails made from it are not touched.",
		Example:           "jailops image generation delete freebsd-15-1 g1",
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

// imageGeneration is the group of the generations of an image: list and delete.
func (a *app) imageGeneration(d *kindDesc[api.Image]) *cobra.Command {
	gc := group("generation", "List and delete the generations of an image",
		"Every build of an image is a generation (g1, g2, ...). A generation can be deleted when no jail uses it.",
		"jailops image generation list freebsd-15-1\njailops image generation delete freebsd-15-1 g1")
	gc.AddCommand(a.imageGenerationList(d), a.imageGenerationDelete(d))
	return gc
}
