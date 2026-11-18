package priv

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"jailops.net/jailops/internal/sys/run"
)

// checkVolumeMountTarget holds a nullfs mount that gives a running jail a volume or a host directory to the checks an
// fstab gets when the jail starts (checkFstabPaths, checkFstabNesting): the mount point is a plain path below the
// jail's root with no link in it, it is not over /dev, /proc, /sys or the mirror of a Linux jail, and nothing is
// mounted below a volume or over one. The jail's root is the jail's own tree: a link planted there would carry the
// mount to another jail's root or to a volume another jail writes, and checkMountLanded only undoes a mount that
// already landed. The mount of a Linux jail's mirror (the jail's root itself as the source) is not a volume.
func checkVolumeMountTarget(ctx context.Context, l *Local, src, target string) error {
	root, ok := l.cfg.jailRootPath(target)
	if !ok || src == root {
		return nil
	}
	if err := plainBelow(root, target); err != nil {
		return forbidden("mount point %s: %v", target, err)
	}
	logical := strings.TrimPrefix(target, root)
	if within("/compat/linux", logical) {
		logical = strings.TrimPrefix(logical, "/compat/linux")
		if logical == "" {
			return forbidden("mount point %s is the mirror of the jail's root", target)
		}
	}
	for _, sys := range []string{"/dev", "/proc", "/sys", "/compat"} {
		if within(sys, logical) {
			return forbidden("mount point %s is %s or below it, which the jail's own file systems use", target, sys)
		}
	}
	system := map[string]bool{}
	for _, p := range []string{"/dev", "/dev/fd", "/proc", "/sys", "/compat/linux", "/compat/linux/dev/shm", "/compat/linux/proc", "/compat/linux/sys"} {
		system[root+p] = true
	}
	if l.d.Runner == nil {
		return nil
	}
	for _, mp := range l.nullfsMountPoints(ctx) {
		if !within(root, mp) || mp == root || system[mp] {
			continue
		}
		switch {
		case mp == target:
			return forbidden("mount point %s is a mount point already", target)
		case within(mp, target):
			return forbidden("mount point %s is inside the mount point %s: nothing is mounted below a volume", target, mp)
		case within(target, mp):
			return forbidden("mount point %s would cover the mount point %s", target, mp)
		}
	}
	return nil
}

// jailRootPath is the root of the jail p lies in (<storage mount>/projects/<project>/jails/<name>/root), whether or not
// p is the root itself; the second result is false for a path that is not below the root of a jail.
func (c Config) jailRootPath(p string) (string, bool) {
	best := ""
	for _, m := range c.reg.mounts() {
		if within(m, p) && len(m) > len(best) {
			best = m
		}
	}
	if best == "" {
		return "", false
	}
	rel, err := filepath.Rel(best, p)
	if err != nil {
		return "", false
	}
	parts := strings.Split(rel, "/")
	if len(parts) < 5 || parts[0] != "projects" || parts[2] != "jails" || parts[4] != "root" {
		return "", false
	}
	return filepath.Join(best, "projects", parts[1], "jails", parts[3], "root"), true
}

// nullfsMountPoints lists the nullfs mounts of the host (volumes, host directories and the mirrors of Linux jails). The
// datasets of a jail's own tree (a thin jail's /usr/local, for one) are mounted below its root too, and are not what the
// nesting rule is about: only a nullfs mount can be written to by another jail.
func (l *Local) nullfsMountPoints(ctx context.Context) []string {
	res, err := l.d.Runner.Run(ctx, run.Cmd{Name: "mount", Args: []string{"-p"}, Timeout: 10 * time.Second})
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		if f := strings.Fields(line); len(f) >= 3 && f[2] == "nullfs" {
			out = append(out, f[1])
		}
	}
	return out
}
