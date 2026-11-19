package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
	"jailops.net/jailops/sdk/tarcopy"
)

// jailops cp: files and directory trees between this machine and a jail, or between two jails, without a shell in the
// jail and without ssh. The data travels as a tar stream through the daemon's file routes (sdk/api/jailfiles.go), so
// it works through a hub and under the same project and role rules as exec; every transfer is audited.
//
// A jail path is NAME:/path (the path starts with a slash); anything else is a local path. A local path with a colon
// is written ./a:b. With -l or --all the destination is :/path, the same path in every jail the selector matches.

var cpJailRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// cpSpec is one side of a copy.
type cpSpec struct {
	jail   string // "" for a local path, and for ":/path" (the jails of a selector)
	path   string
	remote bool
}

// parseCpSpec reads NAME:/path, :/path or a local path.
func parseCpSpec(s string) cpSpec {
	i := strings.IndexByte(s, ':')
	if i >= 0 && i+1 < len(s) && s[i+1] == '/' && !strings.Contains(s[:i], "/") && (i == 0 || cpJailRE.MatchString(s[:i])) {
		return cpSpec{jail: s[:i], path: s[i+1:], remote: true}
	}
	return cpSpec{path: s}
}

type cpOpts struct {
	recursive bool
	preserve  bool
	mkdir     bool
	parallel  int
}

func (a *app) cpCmd() *cobra.Command {
	var o cpOpts
	cmd := &cobra.Command{
		Use:   "cp [-r] [-p] SOURCE... DESTINATION",
		Short: "Copy files between this machine and a jail, or between jails",
		Long: `Copy files and directory trees into a jail, out of a jail, or from one jail to another. A path in a jail is
NAME:/path with an absolute path; anything else is a local path (write ./a:b for a local file with a colon in
its name). The data goes through the daemon as a tar stream: nothing runs in the jail, no ssh server is needed, it
works through a hub (--host), and every transfer is audited like an exec. It works on a running jail, and on a
stopped one whose root is mounted on the host; the root of a stopped jail usually is not (the answer is busy).

The rules are those of cp: with several sources the destination is a directory; a source that is a directory needs
-r; a destination that exists as a directory receives the sources inside it, any other destination is the new name
of the single source. A file that exists is replaced (written to a temporary name and renamed); a directory is
never replaced by a file. Symbolic links are never followed, in a path or in a tree (a link is copied as a link);
devices, FIFOs and sockets are skipped; the set-user-id, set-group-id and sticky bits are not set. Without -p
the files are owned by root and get the time of the copy; -p keeps owner, group and times.

With -l KEY=VALUE or --all the destination is :/PATH and the copy goes to every running jail the selector matches
(at most --parallel at a time, with a line per jail and a summary like exec). A transfer is limited to
limits.file_copy_bytes of the daemon (default 2 GiB). Jail to jail passes the data through this machine.`,
		Example: `jailops cp ./nginx.conf web:/usr/local/etc/nginx/nginx.conf
jailops cp -r ./site/ web:/usr/local/www/
jailops cp web:/var/log/messages ./messages
jailops cp -r web:/usr/local/etc ./etc-backup
jailops cp web:/etc/rc.conf db:/etc/rc.conf.web
jailops cp ./motd -l role=web :/etc/motd
jailops cp --mkdir -r ./app web:/opt/app`,
		Args:              cobra.MinimumNArgs(2),
		ValidArgsFunction: completeFiles,
	}
	targets := a.jailTargets(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		selected := cmd.Flags().Changed("selector") || cmd.Flags().Changed("all")
		var srcArgs []string
		var dstArg string
		srcArgs, dstArg = args[:len(args)-1], args[len(args)-1]
		return a.runCp(cmd.Context(), srcArgs, dstArg, selected, targets, o)
	}
	cmd.Flags().BoolVarP(&o.recursive, "recursive", "r", false, "copy directories with everything in them")
	cmd.Flags().BoolVarP(&o.preserve, "preserve", "p", false, "keep owner, group and modification times")
	cmd.Flags().BoolVar(&o.mkdir, "mkdir", false, "create the destination directory (and the ones above it) when it is missing")
	cmd.Flags().IntVar(&o.parallel, "parallel", DefaultExecParallel, "jails that receive the copy at once, with -l or --all")
	return cmd
}

func (a *app) runCp(ctx context.Context, srcArgs []string, dstArg string, selected bool, targets func(context.Context, []string) ([]string, bool, error), o cpOpts) error {
	var srcs []cpSpec
	nRemote := 0
	for _, s := range srcArgs {
		sp := parseCpSpec(s)
		srcs = append(srcs, sp)
		if sp.remote {
			nRemote++
		}
	}
	dst := parseCpSpec(dstArg)
	switch {
	case nRemote != 0 && nRemote != len(srcs):
		return usagef("name sources either all in a jail or all on this machine, not both")
	case nRemote == 0 && !dst.remote:
		return usagef("one side of a copy is a jail: write NAME:/path").withHint("jailops cp ./file web:/etc/file")
	case nRemote != 0 && len(srcs) > 1:
		for _, s := range srcs[1:] {
			if s.jail != srcs[0].jail {
				return usagef("sources in a jail must all be in the same jail")
			}
		}
	}
	for _, s := range srcs {
		if s.remote && s.jail == "" {
			return usagef("a source in a jail names the jail: NAME:/path")
		}
	}
	if selected {
		if nRemote != 0 || !dst.remote || dst.jail != "" {
			return usagef("with -l or --all the sources are on this machine and the destination is :/path, the same path in every jail")
		}
		jails, bulk, err := targets(ctx, nil)
		if err != nil {
			return err
		}
		return a.cpToMany(ctx, srcs, dst.path, jails, bulk, o)
	}
	if dst.remote && dst.jail == "" {
		return usagef("the destination :/path needs -l or --all; name the jail otherwise: NAME:/path")
	}
	if o.parallel < 1 || o.parallel > MaxExecParallel {
		return usagef("--parallel must be between 1 and %d", MaxExecParallel)
	}
	switch {
	case nRemote == 0:
		return a.cpToJail(ctx, srcs, dst, o, true)
	case dst.remote:
		return a.cpJailToJail(ctx, srcs, dst, o)
	default:
		return a.cpFromJail(ctx, srcs, dst, o)
	}
}

// ---- local side ----

// localSource opens the directory that holds a local source as a root and says what the source is. A symlink named on
// the command line is followed once; nothing below it is.
func localSource(p string, recursive bool) (*os.Root, string, bool, error) {
	if p == "" {
		return nil, "", false, usagef("a source is empty")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return nil, "", false, err
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	} else if errors.Is(err, os.ErrNotExist) {
		return nil, "", false, fmt.Errorf("%s: no such file or directory", p)
	}
	st, err := os.Lstat(abs)
	if err != nil {
		return nil, "", false, err
	}
	if st.IsDir() && !recursive {
		return nil, "", false, usagef("%s is a directory: copy it with -r", p)
	}
	if abs == "/" {
		return nil, "", false, usagef("the root of this machine is not copied")
	}
	r, err := os.OpenRoot(filepath.Dir(abs))
	if err != nil {
		return nil, "", false, err
	}
	return r, filepath.Base(abs), st.IsDir(), nil
}

// tarPipe reads the tar stream of a local source. err is set (and the reader fails) when making the stream failed.
func tarPipe(r *os.Root, name, as string, o cpOpts) (io.ReadCloser, func() (tarcopy.Stats, error)) {
	pr, pw := io.Pipe()
	var st tarcopy.Stats
	var cerr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		st, cerr = tarcopy.Create(r, name, as, pw, tarcopy.CreateOpts{Recursive: o.recursive, Preserve: o.preserve})
		pw.CloseWithError(cerr)
	}()
	return pr, func() (tarcopy.Stats, error) { <-done; return st, cerr }
}

// destination decides, like cp, where one source goes: the directory the tar is written below, and the name of
// the entries. isDir says whether the destination exists as a directory (or ends with a slash).
func destination(dstPath string, isDir bool, src string) (dir, as string) {
	if isDir || strings.HasSuffix(dstPath, "/") {
		return strings.TrimSuffix(dstPath, "/"), filepath.Base(strings.TrimSuffix(src, "/"))
	}
	return path.Dir(dstPath), path.Base(dstPath)
}

func summarize(st tarcopy.Stats) string {
	s := plural(st.Files, "file")
	if st.Dirs > 0 {
		s += ", " + plural(st.Dirs, "directory")
		s = strings.Replace(s, "directorys", "directories", 1)
	}
	if st.Symlinks > 0 {
		s += ", " + plural(st.Symlinks, "link")
	}
	return fmt.Sprintf("%s, %d bytes", s, st.Bytes)
}

// ---- local to jail ----

func (a *app) cpToJail(ctx context.Context, srcs []cpSpec, dst cpSpec, o cpOpts, verbose bool) error {
	if ok, err := a.dry(fmt.Sprintf("copy %d source(s) into jail %s:%s", len(srcs), dst.jail, dst.path), map[string]any{"sources": specPaths(srcs), "jail": dst.jail, "path": dst.path}); ok || err != nil {
		return err
	}
	c, err := a.connect(ctx)
	if err != nil {
		return err
	}
	isDir, err := remoteIsDir(ctx, c, dst.jail, dst.path)
	if err != nil {
		return err
	}
	if len(srcs) > 1 && !isDir {
		return usagef("with several sources %s:%s must be a directory", dst.jail, dst.path)
	}
	for _, s := range srcs {
		if err := a.putOne(ctx, c, s.path, dst, isDir, o, verbose); err != nil {
			return err
		}
	}
	return nil
}

// putOne copies one local source into a jail.
func (a *app) putOne(ctx context.Context, c *sdk.Client, src string, dst cpSpec, dstIsDir bool, o cpOpts, verbose bool) error {
	root, name, srcIsDir, err := localSource(src, o.recursive)
	if err != nil {
		return err
	}
	defer root.Close()
	if !dstIsDir && srcIsDir {
		if st, err := c.Jails().FileStat(ctx, dst.jail, dst.path); err == nil && st.Type != api.FileTypeNone && st.Type != api.FileTypeDir {
			return fmt.Errorf("cannot overwrite %s:%s, which is not a directory, with the directory %s", dst.jail, dst.path, src)
		}
	}
	dir, as := destination(dst.path, dstIsDir, name)
	r, finish := tarPipe(root, name, as, o)
	defer r.Close()
	res, perr := c.Jails().FilePut(ctx, dst.jail, strings.TrimPrefix(dir, "/"), r, sdk.FilePutOptions{Archive: true, Mkdir: o.mkdir, Preserve: o.preserve})
	_ = r.Close()
	if _, cerr := finish(); cerr != nil && !errors.Is(cerr, io.ErrClosedPipe) {
		return cpErr(cerr, src)
	}
	if perr != nil {
		return perr
	}
	if verbose {
		a.say("%s -> %s:%s (%s)\n", src, dst.jail, path.Join(dir, as), summarize(tarStats(res)))
	}
	return nil
}

func tarStats(r api.JailFileCopy) tarcopy.Stats {
	return tarcopy.Stats{Files: r.Files, Dirs: r.Dirs, Symlinks: r.Symlinks, Skipped: r.Skipped, Bytes: r.Bytes}
}

func specPaths(s []cpSpec) []string {
	out := make([]string, len(s))
	for i, x := range s {
		out[i] = x.path
	}
	return out
}

// cpErr turns what tarcopy says about a local source into an error of the command.
func cpErr(err error, src string) error {
	switch {
	case errors.Is(err, tarcopy.ErrNotExist):
		return fmt.Errorf("%s: no such file or directory", src)
	case errors.Is(err, tarcopy.ErrRefused):
		return usagef("%s: %s", src, strings.TrimPrefix(err.Error(), tarcopy.ErrRefused.Error()+": "))
	case errors.Is(err, tarcopy.ErrTooLarge):
		return fmt.Errorf("%s: %s", src, strings.TrimPrefix(err.Error(), tarcopy.ErrTooLarge.Error()+": "))
	}
	return fmt.Errorf("%s: %w", src, err)
}

// remoteIsDir says whether a path of a jail is a directory, or ends with a slash.
func remoteIsDir(ctx context.Context, c *sdk.Client, jail, p string) (bool, error) {
	if strings.HasSuffix(p, "/") || p == "/" {
		return true, nil
	}
	st, err := c.Jails().FileStat(ctx, jail, p)
	if err != nil {
		return false, err
	}
	return st.Type == api.FileTypeDir, nil
}

// ---- jail to local ----

func (a *app) cpFromJail(ctx context.Context, srcs []cpSpec, dst cpSpec, o cpOpts) error {
	if ok, err := a.dry(fmt.Sprintf("copy %d path(s) out of jail %s", len(srcs), srcs[0].jail), map[string]any{"jail": srcs[0].jail, "paths": specPaths(srcs), "to": dst.path}); ok || err != nil {
		return err
	}
	c, err := a.connect(ctx)
	if err != nil {
		return err
	}
	jail := srcs[0].jail
	dstPath := dst.path
	st, serr := os.Stat(dstPath)
	isDir := serr == nil && st.IsDir() || strings.HasSuffix(dstPath, "/")
	if len(srcs) > 1 && !isDir {
		return usagef("with several sources %s must be a directory", dstPath)
	}
	for _, s := range srcs {
		if err := a.getOne(ctx, c, jail, s.path, dstPath, isDir, o); err != nil {
			return err
		}
	}
	return nil
}

func (a *app) getOne(ctx context.Context, c *sdk.Client, jail, src, dstPath string, dstIsDir bool, o cpOpts) error {
	cleaned := strings.TrimSuffix(src, "/")
	if cleaned == "" {
		return usagef("name a file or a directory of the jail, not its root")
	}
	sst, err := c.Jails().FileStat(ctx, jail, cleaned)
	if err != nil {
		return err
	}
	switch sst.Type {
	case api.FileTypeNone:
		return &api.Error{Code: api.CodeNotFound, Message: fmt.Sprintf("%s:%s: no such file or directory", jail, src)}
	case api.FileTypeDir:
		if !o.recursive {
			return usagef("%s:%s is a directory: copy it with -r", jail, src)
		}
	case api.FileTypeOther:
		return fmt.Errorf("%s:%s is a device, FIFO or socket: only files, directories and links are copied", jail, src)
	}
	dir, as := destination(dstPath, dstIsDir, path.Base(cleaned))
	if !dstIsDir && sst.Type == api.FileTypeDir {
		if st, err := os.Stat(dstPath); err == nil && !st.IsDir() {
			return fmt.Errorf("cannot overwrite %s, which is not a directory, with the directory %s:%s", dstPath, jail, src)
		}
	}
	if o.mkdir {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("%s: %w", dir, err)
	}
	defer root.Close()
	dl, err := c.Jails().FileGet(ctx, jail, cleaned, sdk.FileGetOptions{Archive: true, Recursive: o.recursive, Preserve: o.preserve, As: as})
	if err != nil {
		return err
	}
	defer dl.Close()
	st, err := tarcopy.Extract(root, dl, tarcopy.ExtractOpts{Preserve: o.preserve})
	if err != nil {
		var ae *api.Error
		if errors.As(err, &ae) {
			return err
		}
		return cpErr(err, jail+":"+src)
	}
	// the end of the stream: a failure that came after the first byte is in a trailer
	if _, err := io.Copy(io.Discard, dl); err != nil {
		return err
	}
	if n := dl.Skipped(); n > 0 {
		fprintf(a.env.Stderr, "note: %s left out of %s:%s (devices, FIFOs and sockets are not copied)\n", plural(n, "file"), jail, src)
	}
	a.say("%s:%s -> %s (%s)\n", jail, src, filepath.Join(dir, as), summarize(st))
	return nil
}

// ---- jail to jail ----

func (a *app) cpJailToJail(ctx context.Context, srcs []cpSpec, dst cpSpec, o cpOpts) error {
	if ok, err := a.dry(fmt.Sprintf("copy %d path(s) from jail %s to jail %s", len(srcs), srcs[0].jail, dst.jail), map[string]any{"from": srcs[0].jail, "paths": specPaths(srcs), "to": dst.jail, "path": dst.path}); ok || err != nil {
		return err
	}
	c, err := a.connect(ctx)
	if err != nil {
		return err
	}
	isDir, err := remoteIsDir(ctx, c, dst.jail, dst.path)
	if err != nil {
		return err
	}
	if len(srcs) > 1 && !isDir {
		return usagef("with several sources %s:%s must be a directory", dst.jail, dst.path)
	}
	for _, s := range srcs {
		cleaned := strings.TrimSuffix(s.path, "/")
		if cleaned == "" {
			return usagef("name a file or a directory of the jail, not its root")
		}
		sst, err := c.Jails().FileStat(ctx, s.jail, cleaned)
		if err != nil {
			return err
		}
		switch sst.Type {
		case api.FileTypeNone:
			return &api.Error{Code: api.CodeNotFound, Message: fmt.Sprintf("%s:%s: no such file or directory", s.jail, s.path)}
		case api.FileTypeDir:
			if !o.recursive {
				return usagef("%s:%s is a directory: copy it with -r", s.jail, s.path)
			}
		case api.FileTypeOther:
			return fmt.Errorf("%s:%s is a device, FIFO or socket: only files, directories and links are copied", s.jail, s.path)
		}
		dir, as := destination(dst.path, isDir, path.Base(cleaned))
		dl, err := c.Jails().FileGet(ctx, s.jail, cleaned, sdk.FileGetOptions{Archive: true, Recursive: o.recursive, Preserve: o.preserve, As: as})
		if err != nil {
			return err
		}
		res, perr := c.Jails().FilePut(ctx, dst.jail, strings.TrimPrefix(dir, "/"), dl, sdk.FilePutOptions{Archive: true, Mkdir: o.mkdir, Preserve: o.preserve})
		_ = dl.Close()
		if perr != nil {
			return perr
		}
		a.say("%s:%s -> %s:%s (%s)\n", s.jail, s.path, dst.jail, path.Join(dir, as), summarize(tarStats(res)))
	}
	return nil
}

// ---- local to many jails ----

func (a *app) cpToMany(ctx context.Context, srcs []cpSpec, dstPath string, jails []string, bulk bool, o cpOpts) error {
	if o.parallel < 1 || o.parallel > MaxExecParallel {
		return usagef("--parallel must be between 1 and %d", MaxExecParallel)
	}
	if len(jails) == 0 {
		a.say("no jail matched\n")
		return nil
	}
	if ok, err := a.dry(fmt.Sprintf("copy %d source(s) to %s in %d jail(s): %s", len(srcs), dstPath, len(jails), strings.Join(jails, ", ")), map[string]any{"sources": specPaths(srcs), "path": dstPath, "jails": jails}); ok || err != nil {
		return err
	}
	c, err := a.connect(ctx)
	if err != nil {
		return err
	}
	results := make([]execResult, len(jails))
	var mu sync.Mutex
	sem := make(chan struct{}, o.parallel)
	var wg sync.WaitGroup
	for i, name := range jails {
		results[i] = execResult{jail: name}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[i].fail = "interrupted"
				return
			}
			j, err := c.Jails().Get(ctx, name)
			switch {
			case err != nil:
				results[i].fail = errorText(err)
				return
			case j.Status.Power != api.PowerRunning:
				results[i].skipped = "not running"
				return
			}
			isDir, err := remoteIsDir(ctx, c, name, dstPath)
			if err == nil && len(srcs) > 1 && !isDir {
				err = fmt.Errorf("with several sources %s must be a directory", dstPath)
			}
			if err != nil {
				results[i].fail = errorText(err)
				return
			}
			quiet := a.quiet
			for _, s := range srcs {
				if err := a.putQuiet(ctx, c, s.path, cpSpec{jail: name, path: dstPath, remote: true}, isDir, o); err != nil {
					results[i].fail = errorText(err)
					return
				}
			}
			if !quiet {
				mu.Lock()
				fprintf(a.env.Stdout, "[%s] copied\n", colored(termText(name, false)))
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return a.execSummary(results, bulk)
}

// putQuiet is putOne without the line of output: the many-jail copy writes one line per jail itself.
func (a *app) putQuiet(ctx context.Context, c *sdk.Client, src string, dst cpSpec, dstIsDir bool, o cpOpts) error {
	return a.putOne(ctx, c, src, dst, dstIsDir, o, false)
}
