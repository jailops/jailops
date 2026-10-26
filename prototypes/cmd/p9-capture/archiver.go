// archiver.go is prototype 9 test 3's minimal archiver/extractor: "a minimal archiver/extractor
// in Go (stdlib + golang.org/x/sys/unix, no repository code) that reads a snapshot tree and
// restores into a fresh dataset preserving everything above" (design 9.4, section 16 rows 11-12).
// It does not import internal/capture or call walk.go/flags.go/ops.go: its own stat, xattr, ACL
// and flag handling (here and in archiver_freebsd.go/archiver_other.go) is independent so that a
// bug in the L0 walker cannot silently cancel out against the round trip that checks it.
//
// Format: a sequence of records, each a 4-byte big-endian JSON header length, the JSON header,
// and, for a regular "primary" file, an 8-byte big-endian content length followed by that many
// raw bytes. Not meant to be a general-purpose archive format, only to prove the round trip.
package main

import (
	"bufio"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// archEntry is one archived filesystem object.
type archEntry struct {
	Path       string            `json:"path"`
	Kind       string            `json:"kind"` // "f","d","l","h" (hardlink), "p","s" (recorded, not recreated)
	Mode       uint32            `json:"mode"`
	Uid        uint32            `json:"uid"`
	Gid        uint32            `json:"gid"`
	MtimeNs    int64             `json:"mtime_ns"`
	Flags      uint32            `json:"flags"`
	LinkTarget string            `json:"link_target,omitempty"` // symlink target
	HardlinkOf string            `json:"hardlink_of,omitempty"` // path of the primary (kind "h" only)
	Xattr      map[string]string `json:"xattr,omitempty"`       // name -> base64(value)
	Acl        string            `json:"acl,omitempty"`         // getfacl -q text
	Size       int64             `json:"size"`                  // content length that follows, kind "f" only
}

// buildArchive walks src and writes one archive file to dst.
func buildArchive(src, dst string) (int, error) {
	out, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	defer out.Close()
	w := bufio.NewWriter(out)
	defer w.Flush()

	seen := map[uint64]string{} // inode -> first archived relative path
	var paths []string
	if err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if p == src {
			return nil
		}
		rel, _ := filepath.Rel(src, p)
		paths = append(paths, rel)
		return nil
	}); err != nil {
		return 0, err
	}
	sort.Strings(paths)

	n := 0
	for _, rel := range paths {
		full := filepath.Join(src, rel)
		fi, err := os.Lstat(full)
		if err != nil {
			return n, err
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			return n, fmt.Errorf("no syscall.Stat_t for %s", full)
		}
		e := archEntry{
			Path: filepath.ToSlash(rel), Mode: uint32(st.Mode), Uid: st.Uid, Gid: st.Gid,
			MtimeNs: fi.ModTime().UnixNano(),
		}
		if flags, err := archGetFlags(full); err == nil {
			e.Flags = flags
		}
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			e.Kind = "l"
			t, err := os.Readlink(full)
			if err != nil {
				return n, err
			}
			e.LinkTarget = t
		case fi.IsDir():
			e.Kind = "d"
		case fi.Mode().IsRegular():
			ino := uint64(st.Ino)
			if prior, dup := seen[ino]; dup && st.Nlink > 1 {
				e.Kind = "h"
				e.HardlinkOf = prior
			} else {
				e.Kind = "f"
				e.Size = st.Size
				seen[ino] = e.Path
				if names, err := archListXattr(full); err == nil {
					for _, name := range names {
						v, err := archGetXattr(full, name)
						if err != nil {
							continue
						}
						if e.Xattr == nil {
							e.Xattr = map[string]string{}
						}
						e.Xattr[name] = b64(v)
					}
				}
				e.Acl = getfaclText(full)
			}
		default:
			e.Kind = "?" // device/fifo/socket: recorded, not recreated by extractArchive
		}
		if err := writeEntry(w, e); err != nil {
			return n, err
		}
		if e.Kind == "f" {
			f, err := os.Open(full)
			if err != nil {
				return n, err
			}
			if _, err := io.Copy(w, io.LimitReader(f, e.Size)); err != nil {
				f.Close()
				return n, err
			}
			f.Close()
		}
		n++
	}
	return n, nil
}

func writeEntry(w io.Writer, e archEntry) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(b)))
	if _, err := w.Write(lenBuf[:]); err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// extractArchive reads an archive file written by buildArchive and recreates its tree under dst
// (which must already exist as an empty directory).
func extractArchive(src, dst string) (int, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	r := bufio.NewReader(in)
	n := 0
	var deferredFlags []struct {
		path  string
		flags uint32
	}
	for {
		var lenBuf [4]byte
		if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
			if err == io.EOF {
				break
			}
			return n, err
		}
		hb := make([]byte, binary.BigEndian.Uint32(lenBuf[:]))
		if _, err := io.ReadFull(r, hb); err != nil {
			return n, err
		}
		var e archEntry
		if err := json.Unmarshal(hb, &e); err != nil {
			return n, err
		}
		full := filepath.Join(dst, filepath.FromSlash(e.Path))
		switch e.Kind {
		case "d":
			if err := os.MkdirAll(full, 0o755); err != nil {
				return n, err
			}
		case "l":
			if err := archSymlink(e.LinkTarget, full); err != nil {
				return n, err
			}
			archLchown(full, int(e.Uid), int(e.Gid))
		case "h":
			if err := os.Link(filepath.Join(dst, filepath.FromSlash(e.HardlinkOf)), full); err != nil {
				return n, err
			}
		case "f":
			if err := extractFile(r, full, e); err != nil {
				return n, err
			}
			for name, v64 := range e.Xattr {
				v, err := unb64(v64)
				if err == nil {
					archSetXattr(full, name, v)
				}
			}
			if e.Acl != "" {
				restoreACL(full, e.Acl)
			}
		default:
			// device/fifo/socket: not recreated (needs privilege / mknod semantics out of scope).
		}
		if e.Flags != 0 {
			deferredFlags = append(deferredFlags, struct {
				path  string
				flags uint32
			}{full, e.Flags})
		}
		if e.Kind == "d" || e.Kind == "f" {
			os.Chmod(full, os.FileMode(e.Mode&0o7777))
			os.Chown(full, int(e.Uid), int(e.Gid))
			archLutimes(full, e.MtimeNs)
		}
		n++
	}
	// Flags last (schg/uappnd would block the chmod/chown/xattr/ACL steps above).
	for i := len(deferredFlags) - 1; i >= 0; i-- {
		archSetFlags(deferredFlags[i].path, deferredFlags[i].flags)
	}
	return n, nil
}

func extractFile(r io.Reader, path string, e archEntry) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.CopyN(f, r, e.Size)
	return err
}

func b64(b []byte) string            { return base64.StdEncoding.EncodeToString(b) }
func unb64(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }

// getfaclText runs `getfacl -q` if present; "" otherwise (not a failure: a trivial ACL, or no
// getfacl on this build host).
func getfaclText(path string) string {
	out, err := exec.Command("getfacl", "-q", path).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// restoreACL feeds a saved `getfacl -q` text back with `setfacl -M -`. UNVERIFIED: FreeBSD's
// setfacl(1) -M reading its own getfacl -q output verbatim.
func restoreACL(path, acl string) {
	cmd := exec.Command("setfacl", "-M", "-", path)
	cmd.Stdin = strings.NewReader(acl + "\n")
	_ = cmd.Run()
}
