package cli

import (
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
	return &d
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
jailops image alias set freebsd-15 stable 2`, "images")
	crudCommands(a, ic, d, `Define an image. The definition says where the base comes from; 'jailops image
build' then produces its first generation.`,
		`jailops image create freebsd-15 --os freebsd --release 15.1-RELEASE --arch amd64 --alias stable
jailops image create -f freebsd-15.yaml`,
		func(cmd *cobra.Command) func(string) (api.Image, error) {
			var os_, release, arch, stype, repo, keys string
			var sets, aliases []string
			var keepLast, keepDays int
			f := cmd.Flags()
			f.StringVar(&os_, "os", "freebsd", "guest operating system: freebsd or linux")
			f.StringVar(&release, "release", "", "release, for example 15.1-RELEASE (required without -f)")
			f.StringVar(&arch, "arch", "amd64", "architecture: amd64 or arm64")
			f.StringVar(&stype, "source-type", "pkgbase", "how the base is built: pkgbase, minirootfs or debootstrap")
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
	ic.AddCommand(a.imageGenerations(d), a.imageBuild(d), a.imageAlias(d), a.imageDeleteGen(d))
	return ic
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
				n, err := strconv.Atoi(strings.TrimPrefix(from, "g"))
				if err != nil || n < 1 {
					return usagef("--from must be a generation such as g2")
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
	cmd.Flags().StringVar(&from, "from", "", "build from this earlier generation, for example g2")
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
		Long: `Create the alias or move it to GENERATION (a number such as 2 or g2). Without
GENERATION it points at the newest generation. The alias is also recorded in the
image spec, so later builds keep moving it.`,
		Example:           "jailops image alias set freebsd-15 stable 2\njailops image alias set freebsd-15 latest",
		Args:              cobra.RangeArgs(2, 3),
		ValidArgsFunction: completeKind(a, d),
		RunE: func(cmd *cobra.Command, args []string) error {
			n := 0
			if len(args) == 3 {
				var err error
				if n, err = strconv.Atoi(strings.TrimPrefix(args[2], "g")); err != nil || n < 1 {
					return usagef("GENERATION must be a generation number such as 2")
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
		ValidArgsFunction: completeKind(a, d),
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
		Use:               "delete-generation IMAGE NUMBER",
		Short:             "Delete a generation of an image that no jail uses",
		Long:              "Delete one generation of an image. The server refuses (image_in_use) while a jail uses it. Aliases that pointed at it move to the next newest remaining generation, or are removed when none remains.",
		Example:           "jailops image delete-generation freebsd-15 1",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeKind(a, d),
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := strconv.Atoi(strings.TrimPrefix(args[1], "g"))
			if err != nil {
				return usagef("NUMBER must be a generation number such as 3")
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
