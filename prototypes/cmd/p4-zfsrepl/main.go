// Prototype 4: ZFS replication behaviour (design sections 4.5, 9 and 16 row 4).
//
// Written on Linux with no FreeBSD available: NOTHING here has been run. Every behaviour of
// FreeBSD 15 OpenZFS that is assumed carries an UNVERIFIED: tag. Where the true behaviour is
// unknown the check records what was OBSERVED (ok=true) instead of asserting a guess.
//
// All datasets live under $JAILOPS_PROTO_POOL/jailops-proto-4 (src trees) and its child dst.
package main

import (
	"crypto/rand"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"jailops.net/jailops/prototypes/internal/harness"
	"jailops.net/jailops/prototypes/internal/zfsrepl"
)

const tmpDir = "/var/tmp/jailops-proto-4"

func main() {
	// Hidden helper: copy the first N bytes of stdin to a file, then exit (truncated stream).
	if len(os.Args) == 4 && os.Args[1] == "__head" {
		os.Exit(head(os.Args[2], os.Args[3]))
	}
	harness.Exit(harness.Spec{
		N:       4,
		Name:    "zfsrepl",
		Summary: "zfs send/recv semantics: clones, bookmarks, holds, resume tokens, raw encrypted sends, guid continuity.",
		Plan: []string{
			"create " + "<pool>/jailops-proto-4 (+ src trees and dst receive area), mountpoints under " + tmpDir,
			"T1 send of a clone: plain send, send -R alone, send -R of the whole tree; does the receiver need the origin",
			"T2 incremental chain: bookmark as base after destroying the source snapshot; guid continuity; recv without -F refusals",
			"T3 zfs hold: destroy refused, destroy -d deferred, release",
			"T4 resume: truncated recv -s, receive_resume_token, send -t, recv -A",
			"T5 raw send (-w) of a natively encrypted dataset: receiver without key, incremental, later load-key",
			"nothing outside the dataset root is touched; --cleanup destroys the root and " + tmpDir,
		},
		Run:     run,
		Cleanup: cleanup,
	})
}

func head(n, path string) int {
	var c int64
	if _, err := fmt.Sscan(n, &c); err != nil {
		return 2
	}
	f, err := os.Create(path)
	if err != nil {
		return 2
	}
	defer f.Close()
	if _, err := io.CopyN(f, os.Stdin, c); err != nil && err != io.EOF {
		return 1
	}
	// Drain the rest so the sender is not blocked on a pipe nobody reads (the harness pipe
	// only closes after the sender exits). The file stays truncated at N bytes.
	io.Copy(io.Discard, os.Stdin)
	return 0
}

func cleanup(e *harness.Env) {
	if e.DatasetExists(e.Root) {
		if e.ZfsGet("jailops:proto", e.Root) != "4" {
			e.Abort("%s exists but is not marked jailops:proto=4; refusing to destroy", e.Root)
		}
		e.Zfs("destroy", "-r", "-f", e.Root)
	} else {
		e.Note("%s does not exist", e.Root)
	}
	if !e.DryRun {
		os.RemoveAll(tmpDir)
	} else {
		e.Note("would remove %s", tmpDir)
	}
}

func safe(e *harness.Env, name string, f func()) {
	e.Step("%s", name)
	defer func() {
		if r := recover(); r != nil {
			if !e.DryRun {
				e.Check(name+": completed", false, fmt.Sprint("aborted: ", r))
			}
		}
	}()
	f()
}

func write(e *harness.Env, path, content string) {
	if e.DryRun {
		e.Note("would write %s", path)
		return
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		e.Abort("write %s: %v", path, err)
	}
}

func snaps(e *harness.Env, ds string) []zfsrepl.Snap {
	r := e.Quiet("zfs", "list", "-H", "-p", "-t", "snapshot", "-o", "name,guid,createtxg", "-s", "createtxg", "-d", "1", ds)
	s, _ := zfsrepl.ParseSnaps(r.Stdout)
	return s
}

// bookmarkGuid reads a bookmark guid. UNVERIFIED: `zfs list -t bookmark -o guid` is supported.
func bookmarkGuid(e *harness.Env, bm string) string {
	ds, _, _ := strings.Cut(bm, "#")
	r := e.Quiet("zfs", "list", "-H", "-p", "-t", "bookmark", "-o", "name,guid", "-d", "1", ds)
	m, _ := zfsrepl.ParseNameGuid(r.Stdout)
	return m[bm]
}

func send(e *harness.Env, sendArgs []string, recvArgs []string) (harness.Result, harness.Result) {
	return e.Pipe(append([]string{"zfs", "send"}, sendArgs...), append([]string{"zfs", "recv"}, recvArgs...))
}

func okBoth(a, b harness.Result) bool { return a.OK() && b.OK() }

func run(e *harness.Env) {
	e.Require("zfs")
	for _, x := range zfsrepl.Expectations() {
		e.Note("%s: %s", x.ID, x.Claim)
	}
	if e.DatasetExists(e.Root) {
		e.Abort("%s already exists; run with --cleanup first", e.Root)
	}
	R := e.Root
	e.Must("create root", e.Zfs("create", "-p", "-o", "mountpoint=none", "-o", "jailops:proto=4", R).OK() &&
		e.Zfs("create", "-o", "mountpoint=none", R+"/dst").OK(), "dataset root and receive area created")
	if !e.DryRun {
		os.MkdirAll(tmpDir, 0o700)
	}

	safe(e, "T1 clone replication", func() { t1(e, R) })
	safe(e, "T2 bookmark chain, guid continuity, recv refusals", func() { t2(e, R) })
	safe(e, "T3 holds", func() { t3(e, R) })
	safe(e, "T4 resume tokens", func() { t4(e, R) })
	safe(e, "T5 raw encrypted send", func() { t5(e, R) })
	e.Note("datasets are left in place for inspection; run with --cleanup to remove them")
}

func t1(e *harness.Env, R string) {
	mp := func(n string) string { return "mountpoint=" + filepath.Join(tmpDir, n) }
	e.Must("T1 create image", e.Zfs("create", "-p", "-o", mp("img"), R+"/tree/img").OK(), "image dataset")
	write(e, filepath.Join(tmpDir, "img", "base"), "base\n")
	e.Zfs("snapshot", R+"/tree/img@ro")
	e.Must("T1 clone", e.Zfs("clone", "-o", mp("jail"), R+"/tree/img@ro", R+"/tree/jail").OK(), "clone of img@ro")
	write(e, filepath.Join(tmpDir, "jail", "new"), "jail data\n")
	e.Zfs("snapshot", "-r", R+"/tree@t1")
	origin := e.ZfsGet("origin", R+"/tree/jail")
	e.Check("T1 source clone has origin", origin == R+"/tree/img@ro" || e.DryRun, "origin="+origin)

	// E1 plain send
	a, b := send(e, []string{R + "/tree/jail@t1"}, []string{"-u", R + "/dst/plain"})
	o := e.ZfsGet("origin", R+"/dst/plain")
	e.Check("E1 plain send of clone needs no origin", okBoth(a, b) && o == "-",
		fmt.Sprintf("send rc=%d recv rc=%d receiver origin=%q (expect \"-\": full stream, independent dataset)", a.Code, b.Code, o))
	e.Resolve("E1: plain zfs send of a clone is self-contained (observed)")

	// E2 -R of the clone alone
	a, b = send(e, []string{"-R", R + "/tree/jail@t1"}, []string{"-u", R + "/dst/rjail"})
	k := zfsrepl.ClassifyRecv(b.Code, b.Stderr)
	e.Check("E2 send -R of clone alone (observation)", true,
		fmt.Sprintf("OBSERVED send rc=%d recv rc=%d kind=%s receiver origin=%q stderr=%q", a.Code, b.Code, k, e.ZfsGet("origin", R+"/dst/rjail"), oneLine(b.Stderr)))
	// Seed the image first, then -R of the clone.
	a, b = send(e, []string{R + "/tree/img@ro"}, []string{"-u", R + "/dst/seed"})
	a2, b2 := send(e, []string{"-R", R + "/tree/jail@t1"}, []string{"-u", R + "/dst/rjail2"})
	e.Check("E2 send -R of clone after seeding origin elsewhere (observation)", true,
		fmt.Sprintf("OBSERVED seed rc=%d/%d; -R rc=%d/%d kind=%s receiver origin=%q stderr=%q",
			a.Code, b.Code, a2.Code, b2.Code, zfsrepl.ClassifyRecv(b2.Code, b2.Stderr), e.ZfsGet("origin", R+"/dst/rjail2"), oneLine(b2.Stderr)))
	e.Resolve("E2: whether a receiver needs the origin for send -R of a lone clone (see detail)")

	// E3 whole tree
	a, b = send(e, []string{"-R", R + "/tree@t1"}, []string{"-u", R + "/dst/tree"})
	o = e.ZfsGet("origin", R+"/dst/tree/jail")
	e.Check("E3 send -R of tree recreates clone", okBoth(a, b) && o == R+"/dst/tree/img@ro",
		fmt.Sprintf("rc=%d/%d receiver origin=%q stderr=%q", a.Code, b.Code, o, oneLine(b.Stderr)))
	e.Resolve("E3: send -R of a tree with image and clone keeps the clone relationship")
}

func t2(e *harness.Env, R string) {
	C := R + "/chain"
	e.Must("T2 create", e.Zfs("create", "-o", "mountpoint="+filepath.Join(tmpDir, "chain"), C).OK(), "chain dataset")
	f := filepath.Join(tmpDir, "chain", "f")
	write(e, f, "1\n")
	e.Zfs("snapshot", C+"@a")
	D := R + "/dst/chain"
	a, b := send(e, []string{C + "@a"}, []string{"-u", D})
	e.Check("T2 full send @a", okBoth(a, b), fmt.Sprintf("rc=%d/%d %s", a.Code, b.Code, oneLine(b.Stderr)))

	// guid equality (E5)
	sg, dg := e.ZfsGet("guid", C+"@a"), e.ZfsGet("guid", D+"@a")
	e.Check("E5 received snapshot guid equals source guid", sg == dg && sg != "", fmt.Sprintf("src=%s dst=%s", sg, dg))

	// bookmark, destroy source snapshot, incremental from bookmark (E4)
	e.Check("T2 create bookmark", e.Zfs("bookmark", C+"@a", C+"#a").OK(), "zfs bookmark @a #a")
	bg := bookmarkGuid(e, C+"#a")
	e.Check("E4 bookmark guid equals snapshot guid", bg == sg && bg != "", "bookmark guid="+bg)
	write(e, f, "2\n")
	e.Zfs("snapshot", C+"@b")
	e.Check("T2 destroy source @a", e.Zfs("destroy", C+"@a").OK(), "source snapshot gone, bookmark must remain")
	e.Check("T2 bookmark still exists", bookmarkGuid(e, C+"#a") != "" || e.DryRun, "")
	lat, _ := zfsrepl.Latest(snaps(e, D))
	cerr := zfsrepl.Continuity(bg, lat.Guid)
	e.Check("E5 continuity check passes before incremental", cerr == nil || e.DryRun, fmt.Sprintf("receiver latest=%s err=%v", lat.Name, cerr))
	a, b = send(e, []string{"-i", C + "#a", C + "@b"}, []string{"-u", D})
	e.Check("E4 incremental from bookmark after source @a destroyed", okBoth(a, b), fmt.Sprintf("rc=%d/%d %s", a.Code, b.Code, oneLine(a.Stderr+b.Stderr)))
	e.Check("E4 receiver @b guid equals source", e.ZfsGet("guid", C+"@b") == e.ZfsGet("guid", D+"@b") && e.ZfsGet("guid", D+"@b") != "" || e.DryRun, "")
	e.Resolve("E4: bookmark works as incremental base after the source snapshot is destroyed")

	// guid detects a recreated snapshot of the same name
	e.Zfs("bookmark", C+"@b", C+"#b")
	oldB := e.ZfsGet("guid", C+"@b")
	e.Zfs("destroy", C+"@b")
	write(e, f, "3 different history\n")
	e.Zfs("snapshot", C+"@b")
	newB := e.ZfsGet("guid", C+"@b")
	dstB := e.ZfsGet("guid", D+"@b")
	e.Check("E5 same name, different guid is detected", (oldB != newB && zfsrepl.Continuity(newB, dstB) != nil) || e.DryRun,
		fmt.Sprintf("old=%s new=%s receiver=%s; name-based matching would have been fooled", oldB, newB, dstB))
	// incremental from the recreated @b must be refused by the receiver
	e.Zfs("snapshot", C+"@c")
	a, b = send(e, []string{"-i", C + "@b", C + "@c"}, []string{"-u", D})
	k := zfsrepl.ClassifyRecv(b.Code, b.Stderr)
	e.Check("E9 recv refuses incremental from a forked base", (!b.OK() && k != zfsrepl.RecvOK) || e.DryRun,
		fmt.Sprintf("recv rc=%d kind=%s stderr=%q", b.Code, k, oneLine(b.Stderr)))
	// incremental from the real bookmark #b still works (guid of #b = old @b)
	a, b = send(e, []string{"-i", C + "#b", C + "@c"}, []string{"-u", D})
	e.Check("T2 incremental from original bookmark #b still works", okBoth(a, b), fmt.Sprintf("rc=%d/%d %s", a.Code, b.Code, oneLine(a.Stderr+b.Stderr)))

	// E9: full stream onto existing dataset without -F
	a, b = send(e, []string{C + "@c"}, []string{"-u", D})
	k = zfsrepl.ClassifyRecv(b.Code, b.Stderr)
	e.Check("E9 recv without -F refuses existing dataset", !b.OK() || e.DryRun, fmt.Sprintf("kind=%s stderr=%q", k, oneLine(b.Stderr)))

	// E9: divergent receiver: a snapshot made on the receiver after @c
	e.Zfs("snapshot", D+"@rogue")
	write(e, f, "4\n")
	e.Zfs("snapshot", C+"@d")
	lat, _ = zfsrepl.Latest(snaps(e, D))
	srcC := e.ZfsGet("guid", C+"@c")
	cerr = zfsrepl.Continuity(srcC, lat.Guid)
	e.Check("E5 continuity check flags receiver-side divergence", cerr != nil || e.DryRun, fmt.Sprintf("receiver latest=%s err=%v", lat.Name, cerr))
	a, b = send(e, []string{"-i", C + "@c", C + "@d"}, []string{"-u", D})
	k = zfsrepl.ClassifyRecv(b.Code, b.Stderr)
	e.Check("E9 recv without -F refuses divergent receiver", !b.OK() || e.DryRun, fmt.Sprintf("kind=%s stderr=%q", k, oneLine(b.Stderr)))
	e.Resolve("E9: recv without -F refuses existing and divergent destinations")
	e.Resolve("E5: guid comparison (not names) detects forks and recreated snapshots")
}

func t3(e *harness.Env, R string) {
	H := R + "/hold"
	e.Must("T3 create", e.Zfs("create", "-o", "mountpoint=none", H).OK(), "dataset")
	e.Zfs("snapshot", H+"@h")
	e.Check("T3 hold", e.Zfs("hold", "keep", H+"@h").OK(), "zfs hold keep")
	h := zfsrepl.ParseHolds(e.Quiet("zfs", "holds", "-H", H+"@h").Stdout)
	e.Check("T3 holds lists the tag", zfsrepl.HasHold(h, "keep") || e.DryRun, fmt.Sprint(h))
	r := e.Zfs("destroy", H+"@h")
	e.Check("E6 destroy of held snapshot is refused", !r.OK() || e.DryRun, fmt.Sprintf("rc=%d stderr=%q", r.Code, oneLine(r.Stderr)))
	e.Zfs("destroy", "-d", H+"@h")
	e.Check("E6 destroy -d defers: snapshot still exists", len(snaps(e, H)) == 1 || e.DryRun, fmt.Sprintf("snapshots=%d userrefs=%s", len(snaps(e, H)), e.ZfsGet("userrefs", H+"@h")))
	e.Check("E6 release", e.Zfs("release", "keep", H+"@h").OK(), "zfs release keep")
	e.Check("E6 after release the deferred destroy completes", len(snaps(e, H)) == 0 || e.DryRun, fmt.Sprintf("snapshots=%d", len(snaps(e, H))))
	// a held snapshot on the receiver blocks recv -F rollback/destroy of that snapshot
	e.Resolve("E6: zfs hold blocks destroy; destroy -d defers until release")
}

func t4(e *harness.Env, R string) {
	B := R + "/big"
	e.Must("T4 create", e.Zfs("create", "-o", "mountpoint="+filepath.Join(tmpDir, "big"), B).OK(), "dataset")
	if !e.DryRun {
		f, err := os.Create(filepath.Join(tmpDir, "big", "random"))
		if err != nil {
			e.Abort("%v", err)
		}
		io.CopyN(f, rand.Reader, 48<<20) // incompressible so the stream is large
		f.Close()
	} else {
		e.Note("would write 48 MiB of random data")
	}
	e.Zfs("snapshot", B+"@r")
	self, _ := os.Executable()
	part := filepath.Join(tmpDir, "partial.stream")
	D := R + "/dst/big"

	// truncated stream: send | __head 8MiB > file; then recv -s < file
	a, _ := e.Pipe([]string{"zfs", "send", B + "@r"}, []string{self, "__head", "8388608", part})
	e.Note("helper kept the first 8 MiB of the stream (send rc=%d, drained)", a.Code)
	rr := e.RunStdinFile(part, "zfs", "recv", "-s", "-u", D)
	e.Check("E7 truncated recv -s fails", !rr.OK() || e.DryRun, fmt.Sprintf("rc=%d stderr=%q", rr.Code, oneLine(rr.Stderr)))
	tok, has := zfsrepl.ResumeToken(e.ZfsGet("receive_resume_token", D))
	e.Check("E7 receive_resume_token set", (has && zfsrepl.ValidToken(tok)) || e.DryRun, "token="+shorten(tok))
	if has && zfsrepl.ValidToken(tok) {
		a, b := e.Pipe([]string{"zfs", "send", "-t", tok}, []string{"zfs", "recv", "-s", "-u", D})
		e.Check("E7 send -t resumes and completes", okBoth(a, b), fmt.Sprintf("rc=%d/%d %s", a.Code, b.Code, oneLine(a.Stderr+b.Stderr)))
		e.Check("E7 resumed snapshot guid equals source", e.ZfsGet("guid", B+"@r") == e.ZfsGet("guid", D+"@r") && e.ZfsGet("guid", D+"@r") != "", "")
		_, still := zfsrepl.ResumeToken(e.ZfsGet("receive_resume_token", D))
		e.Check("E7 token cleared after completion", !still, "")
	}
	// abort path: recv -A
	D2 := R + "/dst/big2"
	e.Pipe([]string{"zfs", "send", B + "@r"}, []string{self, "__head", "8388608", part})
	e.RunStdinFile(part, "zfs", "recv", "-s", "-u", D2)
	if _, has2 := zfsrepl.ResumeToken(e.ZfsGet("receive_resume_token", D2)); has2 || e.DryRun {
		r := e.Zfs("recv", "-A", D2)
		e.Check("E7 recv -A aborts and discards partial state", r.OK() && !e.DatasetExists(D2) || e.DryRun, fmt.Sprintf("rc=%d exists=%v", r.Code, e.DatasetExists(D2)))
	} else {
		e.Check("E7 second partial left a token", false, "no token on "+D2)
	}
	e.Resolve("E7: recv -s / send -t / recv -A resume protocol")
}

func t5(e *harness.Env, R string) {
	key := filepath.Join(tmpDir, "enc.key")
	if !e.DryRun {
		k := make([]byte, 32)
		rand.Read(k)
		if err := os.WriteFile(key, k, 0o600); err != nil {
			e.Abort("%v", err)
		}
	}
	E := R + "/enc"
	D := R + "/dst/enc"
	mp := filepath.Join(tmpDir, "enc")
	e.Must("T5 create encrypted", e.Zfs("create", "-o", "encryption=on", "-o", "keyformat=raw", "-o", "keylocation=file://"+key,
		"-o", "mountpoint="+mp, E).OK(), "UNVERIFIED: raw keyformat with file:// keylocation on FreeBSD 15")
	secret := "secret plaintext\n"
	write(e, filepath.Join(mp, "s"), secret)
	e.Zfs("snapshot", E+"@e1")
	write(e, filepath.Join(mp, "s2"), "more\n")
	e.Zfs("snapshot", E+"@e2")

	a, b := send(e, []string{"-w", E + "@e1"}, []string{"-u", D})
	e.Check("E8 raw send full", okBoth(a, b), fmt.Sprintf("rc=%d/%d %s", a.Code, b.Code, oneLine(a.Stderr+b.Stderr)))
	ks := e.ZfsGet("keystatus", D)
	e.Check("E8 receiver has no key", ks == "unavailable" || e.DryRun, "keystatus="+ks)
	m := e.Zfs("mount", D)
	e.Check("E8 receiver cannot mount without key", !m.OK() || e.DryRun, fmt.Sprintf("rc=%d stderr=%q", m.Code, oneLine(m.Stderr)))
	a, b = send(e, []string{"-w", "-i", E + "@e1", E + "@e2"}, []string{"-u", D})
	e.Check("E8 raw incremental without key", okBoth(a, b), fmt.Sprintf("rc=%d/%d %s", a.Code, b.Code, oneLine(a.Stderr+b.Stderr)))
	e.Check("E8 guid preserved by raw send", e.ZfsGet("guid", E+"@e2") == e.ZfsGet("guid", D+"@e2") && e.ZfsGet("guid", D+"@e2") != "" || e.DryRun, "")
	e.Check("E8 receiver key still unavailable after incremental", e.ZfsGet("keystatus", D) == "unavailable" || e.DryRun, "")
	e.Note("receiver encryptionroot=%q keylocation=%q (informational)", e.ZfsGet("encryptionroot", D), e.ZfsGet("keylocation", D))

	// the key arrives later
	lk := e.Zfs("load-key", "-L", "file://"+key, D)
	e.Check("E8 load-key with the original key", lk.OK(), fmt.Sprintf("rc=%d %s", lk.Code, oneLine(lk.Stderr)))
	dm := filepath.Join(tmpDir, "dstenc")
	e.Zfs("set", "mountpoint="+dm, D)
	mm := e.Zfs("mount", D)
	got := ""
	if !e.DryRun {
		bts, _ := os.ReadFile(filepath.Join(dm, "s"))
		got = string(bts)
	}
	e.Check("E8 data readable after key load", (mm.OK() && got == secret) || e.DryRun, fmt.Sprintf("mount rc=%d content=%q", mm.Code, got))
	e.Resolve("E8: raw send needs no key on the receiver; key can be loaded later")

	// E10 plain send of encrypted data lands as plaintext on an unencrypted parent.
	// The source key is loaded so a plain send is possible.
	a, b = send(e, []string{E + "@e1"}, []string{"-u", R + "/dst/plainenc"})
	enc := e.ZfsGet("encryption", R+"/dst/plainenc")
	e.Check("E10 non-raw send of encrypted dataset is plaintext on receiver", (okBoth(a, b) && enc == "off") || e.DryRun,
		fmt.Sprintf("rc=%d/%d receiver encryption=%q (\"off\" means the backup node would see plaintext)", a.Code, b.Code, enc))
	e.Resolve("E10: non-raw send of encrypted dataset yields plaintext on an unencrypted receiver")
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 240 {
		s = s[:240] + "..."
	}
	return s
}

func shorten(s string) string {
	if len(s) > 24 {
		return s[:24] + "..."
	}
	return s
}

var _ = flag.ContinueOnError
