// walk.go builds a capture.Tree from a real directory: stat (via rawStat, see stat_*.go),
// extended attributes (`lsextattr`/`getextattr`), NFSv4 ACLs (`getfacl`) and file flags (decoded
// from the stat flags field). It is specific to prototype 9 and is not imported by the archiver
// (archiver.go), which design 9.4's own round-trip test requires to stand alone.
//
// UNVERIFIED: `lsextattr -q`/`getextattr -q` output shape and `getfacl -q` text are from
// extattr(8)/acl(3) documentation memory, not a FreeBSD run.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"jailops.net/jailops/prototypes/internal/capture"
)

const (
	sIFMT   = 0o170000
	sIFREG  = 0o100000
	sIFDIR  = 0o040000
	sIFLNK  = 0o120000
	sIFIFO  = 0o010000
	sIFSOCK = 0o140000
	sIFBLK  = 0o060000
	sIFCHR  = 0o020000
)

func kindOf(mode uint32) byte {
	switch mode & sIFMT {
	case sIFREG:
		return 'f'
	case sIFDIR:
		return 'd'
	case sIFLNK:
		return 'l'
	case sIFIFO:
		return 'p'
	case sIFSOCK:
		return 's'
	case sIFBLK:
		return 'b'
	case sIFCHR:
		return 'c'
	}
	return '?'
}

// extattrNamespace is the only namespace this prototype uses, matching design 9.4's mention of
// extattr_* (ordinary per-file user attributes, not system attributes which need privilege).
const extattrNamespace = "user"

// readXattrPairs lists and reads a file's user extended attributes as "namespace.name=value".
func readXattrPairs(path string) ([]string, error) {
	out, err := exec.Command("lsextattr", "-q", extattrNamespace, path).Output()
	if err != nil {
		if isNotFoundOrUnsupported(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("lsextattr %s: %w", path, err)
	}
	names := strings.Fields(string(out))
	var pairs []string
	for _, n := range names {
		v, err := exec.Command("getextattr", "-q", extattrNamespace, n, path).Output()
		if err != nil {
			return nil, fmt.Errorf("getextattr %s %s: %w", n, path, err)
		}
		pairs = append(pairs, extattrNamespace+"."+n+"="+string(v))
	}
	return pairs, nil
}

// isNotFoundOrUnsupported reports an exec.Error (the lsextattr binary itself is missing, e.g.
// this binary running off FreeBSD): treated as "no extended attributes" rather than a failure.
func isNotFoundOrUnsupported(err error) bool {
	var ee *exec.Error
	return errors.As(err, &ee)
}

// readACL returns `getfacl -q`'s text for a path, "" if the tool is unavailable or the file
// carries only a trivial ACL equivalent to its mode bits (best-effort: this prototype does not
// try to tell those apart beyond what -q already strips, and records whatever it gets).
func readACL(path string) string {
	out, err := exec.Command("getfacl", "-q", path).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// WalkTree builds a capture.Tree of every entry under root (root itself excluded), keyed by the
// path relative to root with forward slashes, so two different roots compare directly.
func WalkTree(root string) (capture.Tree, error) {
	t := capture.Tree{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		st, err := doLstat(p)
		if err != nil {
			return fmt.Errorf("lstat %s: %w", p, err)
		}
		entry := capture.Entry{
			Kind: kindOf(st.Mode), Mode: st.Mode, Uid: st.Uid, Gid: st.Gid, Size: st.Size,
			MtimeNs: st.MtimeNs, CtimeNs: st.CtimeNs, Inode: st.Ino, Nlink: st.Nlink,
			Flags: decodeFlags(st.Flags),
		}
		if entry.Kind == 'l' {
			target, err := os.Readlink(p)
			if err != nil {
				return fmt.Errorf("readlink %s: %w", p, err)
			}
			entry.LinkTarget = target
		}
		if entry.Kind == 'f' || entry.Kind == 'd' {
			pairs, err := readXattrPairs(p)
			if err != nil {
				return err
			}
			entry.XattrHash = capture.XattrHash(pairs)
			entry.Acl = readACL(p)
		}
		t[rel] = entry
		return nil
	})
	if err != nil {
		return nil, err
	}
	return t, nil
}

// sortedPaths is a small helper used by several phases to iterate a Tree deterministically.
func sortedPaths(t capture.Tree) []string {
	paths := make([]string, 0, len(t))
	for p := range t {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}
