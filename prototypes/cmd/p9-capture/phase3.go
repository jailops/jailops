package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"jailops.net/jailops/prototypes/internal/capture"
	"jailops.net/jailops/prototypes/internal/harness"
)

// test3 settles section 16 rows 11-12: an independent archiver/extractor reads a snapshot tree
// and restores it into a fresh dataset, preserving everything test 1 checked.
func test3(e *harness.Env) {
	e.Step("3. round trip: independent archiver/extractor (row 11-12)")
	src := mp("live") + "/.zfs/snapshot/s1"
	if _, err := os.Stat(src); err != nil && !e.DryRun {
		e.Check("round trip source (.zfs/snapshot/s1) exists", false, fmt.Sprintf("%v (test 1 must run first)", err))
		return
	}
	archiveFile := mountBase + "/archive.jop9"
	restore := e.Root + "/restore"
	e.Must("create restore dataset", e.Zfs("create", "-o", "mountpoint="+mp("restore"), "-o", "acltype=nfsv4", "-o", "aclmode=passthrough", "-o", "aclinherit=passthrough", restore).OK(), "fresh dataset for the restored tree")

	if e.DryRun {
		e.Note("would archive %s -> %s, then extract into %s", src, archiveFile, mp("restore"))
		e.Check("round trip", true, "dry run")
		return
	}

	n1, err := buildArchive(src, archiveFile)
	e.Check("archive built", err == nil, fmt.Sprintf("%d entries, err=%v", n1, err))
	if err != nil {
		return
	}
	n2, err := extractArchive(archiveFile, mp("restore"))
	e.Check("archive extracted", err == nil, fmt.Sprintf("%d entries, err=%v", n2, err))
	if err != nil {
		return
	}
	e.Check("archive entry count matches extracted entry count", n1 == n2, fmt.Sprintf("%d vs %d", n1, n2))

	srcTree, errS := WalkTree(src)
	dstTree, errD := WalkTree(mp("restore"))
	e.Must("walk both trees", errS == nil && errD == nil, fmt.Sprintf("src err=%v dst err=%v", errS, errD))

	d1, d2 := capture.Digest(srcTree), capture.Digest(dstTree)
	e.Check("metadata digest matches (mode/uid/gid/size/mtime/link/xattr/acl/flags)", d1 == d2,
		fmt.Sprintf("src=%s dst=%s", d1, d2))

	if d1 != d2 {
		// Name the fields that differ so the result says WHAT does not round trip.
		shown := 0
		counts := map[string]int{}
		for _, p := range sortedPaths(srcTree) {
			b, ok := dstTree[p]
			if !ok {
				e.Note("round trip: %s missing in the restored tree", p)
				continue
			}
			for _, fd := range capture.EntryDiff(srcTree[p], b) {
				counts[string(fd)]++
				if shown < 25 {
					e.Note("round trip diff: %s field %s: src=%+v dst=%+v", p, string(fd), srcTree[p], b)
					shown++
				}
			}
		}
		e.Note("round trip differing fields (count of entries): %v", counts)
	}

	c1, err1 := contentDigest(src, srcTree)
	c2, err2 := contentDigest(mp("restore"), dstTree)
	e.Check("file content digest matches", err1 == nil && err2 == nil && c1 == c2,
		fmt.Sprintf("src=%s (err=%v) dst=%s (err=%v)", c1, err1, c2, err2))

	hlOK, hlDetail := hardlinksPreserved(srcTree, dstTree)
	e.Check("hard link groups preserved", hlOK, hlDetail)

	e.Resolve(fmt.Sprintf("round trip through the independent archiver preserves metadata (digest match=%v), content (match=%v) and hard links (match=%v)",
		d1 == d2, c1 == c2 && err1 == nil && err2 == nil, hlOK))
}

// contentDigest hashes every regular file's bytes, in sorted path order, into one digest.
func contentDigest(root string, t capture.Tree) (string, error) {
	h := sha256.New()
	for _, p := range sortedPaths(t) {
		if t[p].Kind != 'f' {
			continue
		}
		f, err := os.Open(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00", p)
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// hardlinksPreserved checks that every set of paths sharing an inode in src also shares one
// (not necessarily the same numeric value) inode in dst.
func hardlinksPreserved(src, dst capture.Tree) (bool, string) {
	groups := map[uint64][]string{}
	for p, e := range src {
		if e.Kind == 'f' && e.Nlink > 1 {
			groups[e.Inode] = append(groups[e.Inode], p)
		}
	}
	ok := true
	var detail []string
	for _, paths := range groups {
		if len(paths) < 2 {
			continue
		}
		sort.Strings(paths)
		var inos []uint64
		for _, p := range paths {
			inos = append(inos, dst[p].Inode)
		}
		same := true
		for _, i := range inos {
			if i != inos[0] {
				same = false
			}
		}
		detail = append(detail, fmt.Sprintf("%v -> dst inodes %v (same=%v)", paths, inos, same))
		if !same {
			ok = false
		}
	}
	if len(detail) == 0 {
		return true, "no hard link groups to check"
	}
	return ok, fmt.Sprintf("%v", detail)
}
