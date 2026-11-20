package priv

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"jailops.net/jailops/internal/sys/run"
)

// maxManifest is the longest manifest of a package the helper reads (the largest base package has about 300 KiB).
const maxManifest = 4 << 20

// sealRecordManifest is what makes the registration of a package record mean something. A record can name scripts that
// pkg runs as root on the host when the package is removed or upgraded later (docs/security-model.md, "Package
// scripts run as root"), and the daemon writes the manifest file. So the helper registers only what the image
// generation of the jail's root itself says: it finds the generation through the origin of the root's dataset, asks the
// generation's own package database for the manifest of that name, and refuses a file that differs by one byte. pkg
// then reads a copy the helper made (root's, mode 0700, removed afterwards), never the file the daemon can change.
// Anything but a register passes unchanged.
func (l *Local) sealRecordManifest(ctx context.Context, argv []string) ([]string, func(), error) {
	none := func() {}
	// pkg -r <tree> register -t -M <manifest>
	if len(argv) != 7 || argv[3] != "register" {
		return argv, none, nil
	}
	tree, manifest := argv[2], argv[6]
	name := strings.TrimSuffix(filepath.Base(manifest), ".ucl")
	if !recordNameOK(name) {
		return nil, none, forbidden("pkg-records register: %q is not the manifest of a package", filepath.Base(manifest))
	}
	genTree, err := l.generationTreeOf(ctx, tree)
	if err != nil {
		return nil, none, err
	}
	body, err := l.readRecordManifest(manifest)
	if err != nil {
		return nil, none, err
	}
	res, err := l.d.Runner.Run(ctx, run.Cmd{Name: pkgProgram(), Args: []string{"-r", genTree, "info", "--raw", "--raw-format", "ucl", name},
		Timeout: 60 * time.Second, MaxOutput: maxManifest})
	if err != nil {
		return nil, none, fmt.Errorf("pkg-records register: the generation has no record of %s: %w", name, err)
	}
	if res.Truncated || !bytes.Equal(res.Stdout, body) {
		return nil, none, forbidden("pkg-records register: the manifest of %s is not the one of the image generation of this jail", name)
	}
	dir, err := os.MkdirTemp(l.cfg.StateDir, ".pkgrec-")
	if err != nil {
		return nil, none, fmt.Errorf("sealing the manifest: %w", err)
	}
	cleanup := func() { os.RemoveAll(dir) }
	sealed := filepath.Join(dir, name+".ucl")
	if err := os.WriteFile(sealed, body, 0o600); err != nil {
		cleanup()
		return nil, none, fmt.Errorf("sealing the manifest: %w", err)
	}
	out := append([]string(nil), argv...)
	out[6] = sealed
	return out, cleanup, nil
}

// readRecordManifest reads a regular file of the state or work area without following a link in its path.
func (l *Local) readRecordManifest(path string) ([]byte, error) {
	r, err := l.cfg.resolveRead(path)
	if err != nil {
		return nil, err
	}
	root, err := r.openRoot()
	if err != nil {
		return nil, mapFSErr(err)
	}
	defer root.Close()
	f, err := root.OpenFile(r.rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, mapFSErr(err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil, invalid("pkg-records register: %q is not a regular file", path)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxManifest+1))
	if err != nil {
		return nil, mapFSErr(err)
	}
	if len(b) > maxManifest {
		return nil, forbidden("pkg-records register: the manifest is longer than %d bytes", maxManifest)
	}
	return b, nil
}

// generationTreeOf is the mounted tree of the image generation the root at tree was cloned from: the origin of the
// dataset mounted there, without its snapshot. The helper looks at the mounts and at ZFS, not at anything the daemon
// or the jail wrote.
func (l *Local) generationTreeOf(ctx context.Context, tree string) (string, error) {
	mounts, err := l.d.Runner.Run(ctx, run.Cmd{Name: "mount", Args: []string{"-p"}, Timeout: 10 * time.Second})
	if err != nil {
		return "", fmt.Errorf("pkg-records register: the mounts cannot be read: %w", err)
	}
	devs := parseMountDevices(string(mounts.Stdout))
	ds := devs[filepath.Clean(tree)]
	if ds == "" {
		return "", forbidden("pkg-records register: nothing is mounted at %s", tree)
	}
	o, err := l.d.Runner.Run(ctx, run.Cmd{Name: "zfs", Args: []string{"get", "-H", "-o", "value", "origin", ds}, Timeout: 30 * time.Second})
	if err != nil {
		return "", fmt.Errorf("pkg-records register: the origin of %s: %w", ds, err)
	}
	gen, snap, ok := strings.Cut(strings.TrimSpace(string(o.Stdout)), "@")
	if !ok || gen == "" || snap == "" {
		return "", forbidden("pkg-records register: the root at %s is not a clone of an image generation", tree)
	}
	for mp, d := range devs {
		if d == gen {
			return mp, nil
		}
	}
	return "", forbidden("pkg-records register: the generation %s is not mounted", gen)
}

// parseMountDevices maps the mount points of mount -p to the device (the dataset of a ZFS mount) mounted there.
func parseMountDevices(out string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) >= 3 {
			m[f[1]] = f[0]
		}
	}
	return m
}

// verifyPkgRecords is what the helper looks at before it opens a package database as root. The tree is a root no jail
// runs in (the same rule as pkg -r: a running jail could change what root opens), and var, var/db and var/db/pkg in it
// are real directories and the database a regular file: a jail that left a link there, say var/db/pkg to the package
// database of the host, would have the host's records changed by the next start.
func verifyPkgRecords(ctx context.Context, l *Local, args []string) error {
	if err := verifyPkgTarget(ctx, l, args); err != nil {
		return err
	}
	if len(args) < 2 {
		return nil
	}
	tree := args[1]
	for _, rel := range []string{"var", "var/db", "var/db/pkg", "var/db/pkg/local.sqlite"} {
		fi, err := os.Lstat(filepath.Join(tree, rel))
		switch {
		case err != nil && os.IsNotExist(err) && rel == "var/db/pkg/local.sqlite":
			continue
		case err != nil:
			return forbidden("pkg-records: %s of %s cannot be read: %v", rel, tree, err)
		case fi.Mode()&os.ModeSymlink != 0:
			return forbidden("pkg-records: %s of %s is a symbolic link", rel, tree)
		case rel != "var/db/pkg/local.sqlite" && !fi.IsDir(), rel == "var/db/pkg/local.sqlite" && !fi.Mode().IsRegular():
			return forbidden("pkg-records: %s of %s is not what a package database is", rel, tree)
		}
	}
	return nil
}
