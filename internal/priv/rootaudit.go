package priv

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/crypto/blake2b"
	"jailops.net/jailops/internal/jailaudit"
)

// RootAuditReq asks jail.audit_root to read the root of a jail for `jailops jail audit`: Files are the files of its
// base packages (from a copy of its package database), absolute in the jail, with pkg's checksum for the programs and
// libraries to compare.
type RootAuditReq struct {
	Root  string           `json:"root"`
	Files []jailaudit.File `json:"files"`
}

// AuditRoot implements Host: it reads a jail's root and changes nothing. The root is the root of a managed jail (as
// for CheckRoot). Every file is opened through an os.Root of the jail's root, which no link leads out of, and the last
// component is never followed; the walk for set-id files stays on the root's file system and is bounded. The answer
// holds no password hash, only what the checks need of /etc/master.passwd (jailaudit.Accounts). A running jail can
// change its root while it is read: this is a look, not a proof.
func (l *Local) AuditRoot(ctx context.Context, q RootAuditReq) (jailaudit.Facts, error) {
	root := filepath.Clean(q.Root)
	if !l.jailRoot(root) {
		return jailaudit.Facts{}, forbidden("%s is not the root of a jail", q.Root)
	}
	if len(q.Files) > maxRootCheckPaths {
		return jailaudit.Facts{}, invalid("more than %d files to look at", maxRootCheckPaths)
	}
	paths := make([]string, 0, len(q.Files))
	for _, f := range q.Files {
		paths = append(paths, f.Path)
	}
	found, err := rootFindings(l, root, paths)
	if err != nil {
		return jailaudit.Facts{}, err
	}
	jr, err := os.OpenRoot(root)
	if err != nil {
		return jailaudit.Facts{}, mapFSErr(err)
	}
	defer jr.Close()
	out := jailaudit.Facts{Suspected: found, Texts: map[string]string{}, Unread: map[string]string{}}
	out.Accounts = readAccounts(jr)
	for _, p := range jailaudit.TextFiles {
		readText(jr, p, &out)
	}
	for _, dir := range jailaudit.RepoDirs {
		names, _ := readDirNames(jr, strings.TrimPrefix(dir, "/"))
		n := 0
		for _, name := range names {
			if strings.HasSuffix(name, ".conf") && n < jailaudit.MaxRepoFiles {
				n++
				readText(jr, dir+"/"+name, &out)
			}
		}
	}
	if fi, err := jr.Lstat("usr/sbin/sshd"); err == nil && fi.Mode().IsRegular() {
		out.SSHD = true
	}
	out.ShrcPrompt = hasLine(jr, "root/.shrc", jailaudit.ShrcBlock)
	base := make(map[string]bool, len(q.Files))
	for _, f := range q.Files {
		base[path.Clean("/"+f.Path)] = true
	}
	var owners *jailOwners
	if uids, ok := readIDs(jr, "etc/master.passwd"); ok {
		if gids, ok := readIDs(jr, "etc/group"); ok {
			owners = &jailOwners{uids: uids, gids: gids}
			out.OwnersRead = true
		}
	}
	if err := walkRoot(ctx, jr, base, owners, &out); err != nil {
		return jailaudit.Facts{}, err
	}
	budget := int64(jailaudit.MaxHashTotal)
	for _, f := range q.Files {
		if err := ctx.Err(); err != nil {
			return jailaudit.Facts{}, err
		}
		if !strings.HasPrefix(f.Sum, "1$") && !strings.HasPrefix(f.Sum, "2$") {
			continue
		}
		if budget <= 0 {
			out.NotCompared++
			continue
		}
		p := path.Clean("/" + f.Path)
		got, n, why, err := fileSum(ctx, jr, strings.TrimPrefix(p, "/"), f.Sum[:2])
		if err != nil {
			if ctx.Err() != nil {
				return jailaudit.Facts{}, ctx.Err()
			}
			continue // missing, a link, not a plain file: nothing to compare
		}
		budget -= n
		out.Verified++
		if why != "" || got != f.Sum {
			if why != "" {
				p += " (" + why + ")"
			}
			if len(out.Modified) < jailaudit.MaxListed {
				out.Modified = append(out.Modified, p)
			} else {
				out.ModifiedMore++
			}
		}
	}
	out.Mounts = mountsBelow(root)
	return out, nil
}

// openPlain opens a regular file below the jail's root for reading. os.Root keeps every step inside the root but follows
// a link as the last component, so the file is looked at first and a link is refused; the opened file must be the one
// looked at (a link swapped in meanwhile is refused too). A fifo does not block (O_NONBLOCK).
func openPlain(jr *os.Root, rel string) (*os.File, os.FileInfo, error) {
	lfi, err := jr.Lstat(rel)
	if err != nil {
		return nil, nil, err
	}
	if !lfi.Mode().IsRegular() {
		return nil, nil, errors.New("not a regular file")
	}
	f, err := jr.OpenFile(rel, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	if !fi.Mode().IsRegular() || !os.SameFile(lfi, fi) {
		f.Close()
		return nil, nil, errors.New("not a regular file")
	}
	return f, fi, nil
}

// readText puts the text of the file p (absolute in the jail) into out.Texts, or says in out.Unread why a file that is
// there was not read.
func readText(jr *os.Root, p string, out *jailaudit.Facts) {
	rel := strings.TrimPrefix(p, "/")
	if fi, err := jr.Lstat(rel); err != nil {
		return // missing
	} else if fi.Mode()&os.ModeSymlink != 0 {
		out.Unread[p] = "it is a symbolic link"
		return
	}
	f, fi, err := openPlain(jr, rel)
	if err != nil {
		out.Unread[p] = err.Error()
		return
	}
	defer f.Close()
	if fi.Size() > jailaudit.MaxText {
		out.Unread[p] = "it is larger than 256 KiB"
		return
	}
	b, err := io.ReadAll(io.LimitReader(f, jailaudit.MaxText))
	if err != nil {
		out.Unread[p] = err.Error()
		return
	}
	// the answer is JSON: a control byte takes six bytes there, so it counts for six
	size := len(b)
	for _, c := range b {
		if c < 0x20 && c != '\n' && c != '\t' {
			size += 5
		}
	}
	for _, t := range out.Texts {
		size += len(t)
	}
	if size > jailaudit.MaxTexts {
		out.Unread[p] = "the configuration files together are larger than 1 MiB"
		return
	}
	out.Texts[p] = string(b)
}

// hasLine says whether the plain file rel has a line that starts with prefix; it reads at most MaxText bytes.
func hasLine(jr *os.Root, rel, prefix string) bool {
	f, _, err := openPlain(jr, rel)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(io.LimitReader(f, jailaudit.MaxText))
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), prefix) {
			return true
		}
	}
	return false
}

func readDirNames(jr *os.Root, rel string) ([]string, error) {
	if fi, err := jr.Lstat(rel); err != nil || !fi.IsDir() {
		return nil, err
	}
	d, err := jr.Open(rel)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	names, err := d.Readdirnames(4096) // a directory of millions of names is not read whole
	if errors.Is(err, io.EOF) {
		err = nil
	}
	sort.Strings(names)
	return names, err
}

// readAccounts reads /etc/master.passwd of the jail and keeps what the checks need: the accounts with uid 0, whether
// root and toor are locked, the accounts with an empty password, root's shell. No hash leaves this function.
func readAccounts(jr *os.Root) jailaudit.Accounts {
	var a jailaudit.Accounts
	f, _, err := openPlain(jr, "etc/master.passwd")
	if err != nil {
		return a
	}
	defer f.Close()
	a.Read = true
	sc := bufio.NewScanner(io.LimitReader(f, 16<<20))
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	for sc.Scan() {
		l := sc.Text()
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		// name:password:uid:gid:class:change:expire:gecos:home:shell
		fl := strings.Split(l, ":")
		if len(fl) < 10 {
			continue
		}
		name, pw, uid := fl[0], fl[1], fl[2]
		state := jailaudit.PassSet
		switch {
		case pw == "":
			state = jailaudit.PassEmpty
		case strings.HasPrefix(pw, "*"):
			state = jailaudit.PassLocked // "*" and "*LOCKED*..." take no password
		}
		switch name {
		case "root":
			a.Root, a.RootShell = state, fl[9]
		case "toor":
			a.Toor = state
		default:
			if uid == "0" {
				a.UID0 = append(a.UID0, name)
			}
			if state == jailaudit.PassEmpty {
				a.Empty = append(a.Empty, name)
			}
		}
	}
	return a
}

// readIDs reads the ids (the third field) of /etc/master.passwd or /etc/group of the jail; ok is false when the file
// cannot be read.
func readIDs(jr *os.Root, rel string) (map[uint32]bool, bool) {
	f, _, err := openPlain(jr, rel)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	ids := map[uint32]bool{}
	sc := bufio.NewScanner(io.LimitReader(f, 16<<20))
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	for sc.Scan() {
		l := sc.Text()
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		fl := strings.SplitN(l, ":", 4)
		if len(fl) < 3 {
			continue
		}
		if n, err := strconv.ParseUint(fl[2], 10, 32); err == nil {
			ids[uint32(n)] = true
		}
	}
	return ids, sc.Err() == nil
}

// jailOwners are the uids of the jail's accounts and the gids of its groups.
type jailOwners struct{ uids, gids map[uint32]bool }

// unowned says what of fi's owner and group the jail does not have ("" when it has both).
func (o *jailOwners) unowned(fi fs.FileInfo) string {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	var miss []string
	if !o.uids[st.Uid] {
		miss = append(miss, "uid "+strconv.FormatUint(uint64(st.Uid), 10))
	}
	if !o.gids[st.Gid] {
		miss = append(miss, "gid "+strconv.FormatUint(uint64(st.Gid), 10))
	}
	return strings.Join(miss, ", ")
}

// maxTmpDirs bounds the directories with the sticky bit that anyone may write to, below which a file anyone may
// write to is not listed: a jail that makes more has the files below the others listed.
const maxTmpDirs = 64

// walkRoot walks the jail's root, on its own file system only, once for every list of the Facts that comes from a
// walk: the regular files with the set-user-ID or set-group-ID bit that are not among base, the directories anyone may
// write to without the sticky bit, the regular files anyone may write to outside a directory with the sticky bit that
// anyone may write to (/tmp, /var/tmp), and with owners the files and directories whose owner or group the jail does
// not have. A mount point below the root (a volume, devfs) is not looked at, nor anything under it.
func walkRoot(ctx context.Context, jr *os.Root, base map[string]bool, owners *jailOwners, out *jailaudit.Facts) error {
	top, err := jr.Lstat(".")
	if err != nil {
		return mapFSErr(err)
	}
	dev, _ := devOf(top)
	var tmpDirs []string
	underTmp := func(p string) bool {
		for _, d := range tmpDirs {
			if strings.HasPrefix(p, d) {
				return true
			}
		}
		return false
	}
	add := func(list *[]string, more *int, limit int, s string) {
		if len(*list) < limit {
			*list = append(*list, s)
		} else {
			*more++
		}
	}
	n := 0
	err = fs.WalkDir(jr.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // a directory that cannot be read is skipped
		}
		if n++; n > jailaudit.MaxWalk {
			out.SetIDCut = true
			return fs.SkipAll
		}
		if n%4096 == 0 && ctx.Err() != nil {
			return ctx.Err()
		}
		regular := d.Type().IsRegular()
		if !d.IsDir() && !regular && owners == nil {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		abs := path.Clean("/" + p)
		mode := fi.Mode()
		if d.IsDir() {
			if p != "." {
				if dd, _ := devOf(fi); dd != dev {
					return fs.SkipDir // another file system mounted here: a volume, devfs
				}
			}
			if mode.Perm()&0o002 != 0 {
				if mode&fs.ModeSticky == 0 {
					add(&out.WorldDirs, &out.WorldDirsMore, jailaudit.MaxListed, abs)
				} else if len(tmpDirs) < maxTmpDirs {
					tmpDirs = append(tmpDirs, strings.TrimSuffix(abs, "/")+"/")
				}
			}
		}
		if owners != nil {
			if miss := owners.unowned(fi); miss != "" {
				add(&out.Unowned, &out.UnownedMore, jailaudit.MaxListed, abs+" ("+miss+")")
			}
		}
		if !regular {
			return nil
		}
		if mode.Perm()&0o002 != 0 && !underTmp(abs) {
			add(&out.WorldFiles, &out.WorldFilesMore, jailaudit.MaxListed, abs)
		}
		if mode&(fs.ModeSetuid|fs.ModeSetgid) != 0 && !base[abs] {
			add(&out.SetID, &out.SetIDMore, jailaudit.MaxSetID, abs)
		}
		return nil
	})
	return err
}

// fileSum is the checksum of a regular file below the root as pkg records it, kind first ("1$" and the SHA-256 in hex,
// "2$" and the BLAKE2b-512 in pkg's base32, as pkg 2 writes it), and the bytes it read. A file larger than
// MaxHashFile is not read: why says so, and no base program is that large. A sparse file is not told apart by its
// blocks, which on a compressed dataset are few for any file. The read stops when ctx ends.
func fileSum(ctx context.Context, jr *os.Root, rel, kind string) (sum string, n int64, why string, err error) {
	f, fi, err := openPlain(jr, rel)
	if err != nil {
		return "", 0, "", err
	}
	defer f.Close()
	if fi.Size() > jailaudit.MaxHashFile {
		return "", 0, "larger than any base program", nil
	}
	var h hash.Hash
	if kind == "2$" {
		h, _ = blake2b.New512(nil)
	} else {
		h = sha256.New()
	}
	n, err = io.Copy(h, ctxReader{ctx, f})
	if err != nil {
		return "", n, "", err
	}
	if kind == "2$" {
		return kind + pkgBase32(h.Sum(nil)), n, "", nil
	}
	return kind + hex.EncodeToString(h.Sum(nil)), n, "", nil
}

// pkgBase32 is pkg's own base32 (pkg_checksum_encode_base32 of libpkg): its alphabet, and the bits of each byte from
// the lowest.
func pkgBase32(in []byte) string {
	const b32 = "ybndrfg8ejkmcpqxot1uwisza345h769"
	out := make([]byte, 0, len(in)*8/5+1)
	remain := -1
	for i, c := range in {
		var x int
		switch i % 5 {
		case 0:
			x, remain = int(c), int(c)>>5
			out = append(out, b32[x&0x1f])
		case 1:
			x = remain | int(c)<<3
			out = append(out, b32[x&0x1f], b32[x>>5&0x1f])
			remain = x >> 10
		case 2:
			x = remain | int(c)<<1
			out = append(out, b32[x&0x1f])
			remain = x >> 5
		case 3:
			x = remain | int(c)<<4
			out = append(out, b32[x&0x1f], b32[x>>5&0x1f])
			remain = x >> 10 & 0x3
		case 4:
			x = remain | int(c)<<2
			out = append(out, b32[x&0x1f], b32[x>>5&0x1f])
			remain = -1
		}
	}
	if remain >= 0 {
		out = append(out, b32[remain])
	}
	return string(out)
}

// ctxReader reads from r until ctx ends.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) > 1<<20 {
		p = p[:1<<20]
	}
	return c.r.Read(p)
}

// mountsBelow lists the nullfs mounts below root (the volumes and host directories of a running jail) with whether
// they have nosuid. A table that cannot be read is no mount.
func mountsBelow(root string) []jailaudit.Mount {
	table, err := mountTableReader()
	if err != nil {
		return nil
	}
	var out []jailaudit.Mount
	for _, m := range table {
		if m.typ != "nullfs" || !within(root, m.mp) || m.mp == root {
			continue
		}
		out = append(out, jailaudit.Mount{Path: "/" + strings.TrimPrefix(m.mp, root+"/"), NoSUID: m.nosuid})
	}
	return out
}
