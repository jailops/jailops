package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

func (a *app) jailExport(d *kindDesc[api.Jail]) *cobra.Command {
	var to, snapshot string
	var withVolumes, zstd, force bool
	cmd := &cobra.Command{
		Use:   "export NAME --to FILE",
		Short: "Write a jail to a file that another host or project can import",
		Long: `Write a jail to one file: its specification, the objects it refers to (networks, firewall policies, address
sets), and its data. The jail keeps running, and is busy (no stop, no upgrade) until the file is written. What the file
holds is the state of the datasets at one moment, as if the host had lost power then: a database recovers as it does
after a crash.

A thin jail's data is its persistent datasets (etc, local, var, home): the root is the image's and is not in the file,
so the file is small and the importing host needs the same image. A thick jail, and a Linux jail, carries its root.
Volumes are not in the file unless you ask for them with --with-volumes; a volume that is a directory of the host never
is. An encrypted volume is written as it is stored and stays encrypted: the file holds no key and the volume is locked
after the import.

--to - writes to the standard output (not to a terminal). A file is written complete or not at all, and checked after
it was written. --snapshot exports the jail as it was when 'jail snapshot create' took that snapshot. --zstd
compresses the data in the file. Reading the file needs the operator role for the jail's project; the export is audited.
Make a jail of the file with 'jailops jail import'.`,
		Example: `jailops jail export web --to web.jailexport
jailops jail export web --to web.jailexport --with-volumes --zstd
jailops --host host-a jail export web --to - | jailops --host host-b jail import -`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeArgs(completeKind(a, d)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if to == "" {
				return usagef("--to FILE is required (or --to - for the standard output)")
			}
			c, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			req := api.JailExportRequest{Snapshot: snapshot, WithVolumes: withVolumes, Zstd: zstd}
			if to == "-" {
				if a.env.IsTTY(a.env.Stdout) {
					return usagef("the export is binary: write it to a file with --to FILE, or pipe it")
				}
				return c.Jails().Export(cmd.Context(), args[0], req, a.env.Stdout)
			}
			if _, err := os.Lstat(to); err == nil && !force {
				return usagef("%s exists: choose another file, or --force to replace it", to)
			}
			tmp, err := os.CreateTemp(filepath.Dir(to), "."+filepath.Base(to)+".partial-*")
			if err != nil {
				return err
			}
			defer os.Remove(tmp.Name())
			if err := c.Jails().Export(cmd.Context(), args[0], req, tmp); err != nil {
				tmp.Close()
				return err
			}
			if err := tmp.Sync(); err != nil {
				tmp.Close()
				return err
			}
			if err := tmp.Close(); err != nil {
				return err
			}
			// the file is read back once: what is on the disk is a whole file
			parts, size, err := verifyExportFile(tmp.Name())
			if err != nil {
				return fmt.Errorf("the exported file does not verify: %w", err)
			}
			if err := os.Chmod(tmp.Name(), 0o600); err != nil {
				return err
			}
			if err := os.Rename(tmp.Name(), to); err != nil {
				return err
			}
			a.say("jail/%s exported to %s (%s, %s, complete)\n", args[0], to, plural(parts, "dataset stream"), humanBytes(size))
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&to, "to", "", "the file to write, or - for the standard output")
	f.StringVar(&snapshot, "snapshot", "", "export the jail as of this snapshot of it instead of as of now")
	f.BoolVar(&withVolumes, "with-volumes", false, "carry the volumes the jail attaches that are datasets of the project")
	f.BoolVar(&zstd, "zstd", false, "compress the data in the file")
	f.BoolVar(&force, "force", false, "replace the file when it exists")
	return cmd
}

// verifyExportFile reads a whole export file and checks it; it returns the number of streams and the size.
func verifyExportFile(path string) (parts int, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	parts, err = sdk.ScanExport(f)
	st, _ := f.Stat()
	if st != nil {
		size = st.Size()
	}
	return parts, size, err
}

func (a *app) jailImport() *cobra.Command {
	var as string
	var keepIP, start bool
	var netMap, volMap []string
	cmd := &cobra.Command{
		Use:   "import FILE",
		Short: "Make a jail of a file written by 'jail export'",
		Long: `Make a new jail of an export file, in the project you work in (--project, or the current one). The file is read
once: its manifest first, which is checked against this host and project (the same checks a new jail goes through),
then its data, which is received into new datasets and verified. A file that is cut off or damaged leaves no jail
behind. FILE - reads the standard input.

The new jail has a new uuid and does not start at boot. It gets new addresses, or with --keep-ip the ones in the file
when they are free here. A thin jail's root is made from the image of the same name on this host, so the image has to
exist (the error says how to make it); the jail then runs on this host's newest generation of it. Networks of the file
that this host lacks are mapped with --network-map OLD=NEW (a network belongs to the host and is never made by an
import); volumes the file carries become volumes of the project, renamed with --volume-map OLD=NEW when the name is
taken. Firewall policies and address sets the project lacks are made from the file. An encrypted volume stays locked:
unlock it with its passphrase.

--dry-run reads the whole file and prints what the import would do: what is missing, what it would create or change,
and what stops it. It changes nothing. Importing needs the admin role for the project, like creating a jail.`,
		Example: `jailops jail import web.jailexport
jailops jail import web.jailexport --as web2 --project staging --start
jailops jail import web.jailexport --network-map lan=office --volume-map db=db-copy
jailops --dry-run jail import web.jailexport`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			req := api.JailImportRequest{As: as, KeepIP: keepIP, Start: start}
			var err error
			if req.NetworkMap, err = parseMap("--network-map", netMap); err != nil {
				return err
			}
			if req.VolumeMap, err = parseMap("--volume-map", volMap); err != nil {
				return err
			}
			var src io.Reader = a.env.Stdin
			if args[0] != "-" {
				f, err := os.Open(args[0])
				if err != nil {
					return err
				}
				defer f.Close()
				src = f
			}
			// the manifest is read first, and what was read is given back to the stream that goes to the server
			var head bytes.Buffer
			m, err := sdk.ReadExportManifest(io.TeeReader(src, &head))
			if err != nil {
				return fmt.Errorf("%s: %w", args[0], err)
			}
			c, err := a.connect(ctx)
			if err != nil {
				return err
			}
			pf, err := c.Jails().ImportPreflight(ctx, api.JailImportPreflight{Manifest: m, Request: req})
			if err != nil {
				return err
			}
			if a.dryRun {
				// the rest of the file is read here: a file that is cut off shows before anything is made
				if _, err := sdk.ScanExport(io.MultiReader(&head, src)); err != nil {
					return fmt.Errorf("%s: %w", args[0], err)
				}
				if a.format == fmtJSON || a.format == fmtYAML {
					return a.emit(map[string]any{"dry_run": true, "preflight": pf}, nil, nil, nil)
				}
				a.printImportPlan(m, pf)
				fprintln(a.env.Stdout, "the file is complete; its checksums are verified when it is imported; nothing was changed")
				return nil
			}
			if !pf.OK {
				a.printImportPlan(m, pf)
				return fmt.Errorf("the import cannot be done: %s", strings.Join(pf.Errors, "; "))
			}
			res, err := c.Jails().Import(ctx, req, io.MultiReader(&head, src))
			if err != nil {
				return err
			}
			for _, w := range append(append([]string(nil), m.Warnings...), res.Warnings...) {
				fprintln(a.env.Stderr, "note: "+w)
			}
			return a.emitOne(res.Jail, "jail", res.Jail.Metadata.Name, "imported")
		},
	}
	f := cmd.Flags()
	f.StringVar(&as, "as", "", "the name of the new jail (default: the name in the file)")
	f.BoolVar(&keepIP, "keep-ip", false, "keep the addresses of the file when they are free here")
	f.BoolVar(&start, "start", false, "start the jail when it is made")
	f.StringArrayVar(&netMap, "network-map", nil, "use this host's network NEW for the file's network OLD: OLD=NEW (repeatable)")
	f.StringArrayVar(&volMap, "volume-map", nil, "name the file's volume OLD NEW in the project: OLD=NEW (repeatable)")
	return cmd
}

func parseMap(flag string, vals []string) (map[string]string, error) {
	if len(vals) == 0 {
		return nil, nil
	}
	m := map[string]string{}
	for _, v := range vals {
		k, to, ok := strings.Cut(v, "=")
		if !ok || k == "" || to == "" {
			return nil, usagef("%s takes OLD=NEW, not %q", flag, v)
		}
		m[k] = to
	}
	return m, nil
}

func (a *app) printImportPlan(m api.ExportManifest, pf api.RestorePreflight) {
	w := a.env.Stdout
	layout := string(m.Layout)
	fprintf(w, "file: jail %s of project %s (%s, image %s)\n", m.Name, m.Project, layout, m.Image.Name)
	for _, l := range m.Warnings {
		fprintln(w, "  note: "+l)
	}
	for _, l := range pf.Missing {
		fprintln(w, "  missing: "+l)
	}
	for _, l := range pf.Changes {
		fprintln(w, "  "+l)
	}
	for _, l := range pf.Errors {
		fprintln(w, "  error: "+l)
	}
}
