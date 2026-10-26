// Prototype 9: backup capture assumptions on a live ZFS dataset (design 9.4, rows 9-11 of
// docs/design.md section 16).
//
// HONESTY: written on Linux without access to FreeBSD. Every FreeBSD behaviour it assumes is
// marked UNVERIFIED: and settled (either way) by the run.
//
// SAFETY: all ZFS work is confined to $JAILOPS_PROTO_POOL/jailops-proto-9 (the harness enforces
// this on every `zfs` invocation). Nothing outside that dataset tree, and nothing under /etc, is
// read or written. --cleanup removes the dataset tree and the mount-base directory it created.
package main

import (
	"flag"
	"fmt"
	"os"

	"jailops.net/jailops/prototypes/internal/capture"
	"jailops.net/jailops/prototypes/internal/harness"
)

var (
	mountBase   string
	seed        int64
	fuzzOps     int
	manySnaps   int
	skipRound   bool
	skipMany    bool
	writerEvery = 150 // ms
)

func main() {
	harness.Exit(harness.Spec{
		N:       9,
		Name:    "capture",
		Summary: "ZFS capture assumptions: snapshot metadata fidelity, inode/ctime stability, L0 vs zfs-diff equivalence, archive round trip, .zfs/snapshot behaviour",
		Plan: []string{
			"create <pool>/jailops-proto-9/live (acltype=nfsv4) with a mixed tree (small/large/sparse files, hard links, symlinks, a long name, xattrs, ACLs, flags) plus a writer goroutine that keeps modifying a known 'hot' subset",
			"test 1 (row 9): snapshot live@s1 while the writer runs; compare every non-hot file's live stat against its .zfs/snapshot/s1 view; take @s2 and compare inode/ctime stability between s1 and s2; repeat the comparison across a zfs rollback, a clone, and a send|recv",
			"test 2 (row 10): on a separate dataset, run a seeded random sequence of create/modify/truncate/rename/hardlink/delete/chmod/chown/xattr/acl/flag/dir-rename operations in snapshotted batches; after each batch compare a full L0 walk against `zfs diff -FHt`; report every disagreement with the exact operations that produced it",
			"test 3 (row 11-12): archive a snapshot tree with a minimal, independent archiver (stdlib + x/sys/unix only), extract it into a fresh dataset, and compare a deterministic metadata digest plus file content between the two trees",
			"test 4: .zfs/snapshot automount delay, snapdir visibility, behaviour with many snapshots, and reading a file under .zfs/snapshot while that snapshot is destroyed",
			"--cleanup: destroy the dataset tree and remove --mount-base",
		},
		Flags: func(e *harness.Env, fs *flag.FlagSet) {
			fs.StringVar(&mountBase, "mount-base", "/jop-proto-9", "directory the datasets are mounted under; last element must start with jop")
			fs.Int64Var(&seed, "seed", 20261021, "seed for the randomized operation sequence of test 2")
			fs.IntVar(&fuzzOps, "fuzz-ops", 300, fmt.Sprintf("number of randomized operations for test 2, run in batches of %d", fuzzBatch))
			fs.IntVar(&manySnaps, "many-snapshots", 60, "number of snapshots created for the 'many snapshots' part of test 4")
			fs.BoolVar(&skipRound, "skip-roundtrip", false, "skip test 3 (archive/extract round trip)")
			fs.BoolVar(&skipMany, "skip-many-snapshots", false, "skip the many-snapshots part of test 4")
		},
		Run:     run,
		Cleanup: cleanup,
	})
}

func mp(name string) string { return mountBase + "/" + name }

func run(e *harness.Env) {
	e.Require("zfs", "mount", "umount")
	if lastElem(mountBase) == "" {
		e.Abort("bad --mount-base %q", mountBase)
	}
	if e.DatasetExists(e.Root) {
		e.Abort("%s already exists; run with --cleanup first", e.Root)
	}
	e.Must("create root", e.Zfs("create", "-p", "-o", "mountpoint=none", "-o", "jailops:proto=9", e.Root).OK(), "dataset root")

	live := e.Root + "/live"
	e.Must("create live dataset (nfsv4 ACLs)", e.Zfs("create",
		"-o", "mountpoint="+mp("live"), "-o", "acltype=nfsv4", "-o", "aclmode=passthrough", "-o", "aclinherit=passthrough",
		live).OK(), "UNVERIFIED: acltype=nfsv4/aclmode=passthrough/aclinherit=passthrough accepted on FreeBSD 15 OpenZFS")

	hot := populateLive(e, mp("live"))
	wq := startWriter(e, mp("live"), hot)
	defer wq.stop()

	test1(e, live, hot, wq)
	test2(e, e.Root)
	if !skipRound {
		test3(e)
	} else {
		e.Note("test 3 (round trip) skipped by --skip-roundtrip")
	}
	test4(e, live, mp("live"))

	e.Note("datasets left in place for inspection; run with --cleanup to remove them")
}

func cleanup(e *harness.Env) {
	if e.DatasetExists(e.Root) {
		if e.ZfsGet("jailops:proto", e.Root) != "9" {
			e.Abort("%s exists but is not marked jailops:proto=9; refusing to destroy", e.Root)
		}
		// Flags set during the run (schg etc.) would block destroy; best-effort clear first.
		e.Run("sh", "-c", fmt.Sprintf("chflags -R noschg,nosappnd %s 2>/dev/null; true", mp("live")))
		e.Zfs("destroy", "-r", "-f", e.Root)
	} else {
		e.Note("%s does not exist", e.Root)
	}
	if !e.DryRun {
		os.RemoveAll(mountBase)
	}
}

func lastElem(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}

func oneLine(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

func fieldNames(fs []capture.FieldDiff) string {
	out := ""
	for i, f := range fs {
		if i > 0 {
			out += ","
		}
		out += string(f)
	}
	return out
}
